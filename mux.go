package main

import (
	"os"
	"slices"
)

const minSplitWidth = 4

// mux owns the column order, input focus, and visible state on one event loop.
type mux struct {
	panes        []*pane
	active       int
	columns      int
	rows         int
	shell        string
	events       chan paneEvent
	done         chan struct{}
	dirty        bool
	awaiting     bool
	message      string
	pasteTarget  *pane
	pasteWrapped bool
	previous     []string
	cursor       string
}

// newMux prepares the workspace using the shell chosen by the user's login environment.
func newMux(columns, rows int) *mux {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return &mux{columns: columns, rows: rows, shell: shell, events: make(chan paneEvent, 32), done: make(chan struct{})}
}

// start opens both initial shells in the command's working directory.
func (m *mux) start() error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	left := (m.columns - 1) / 2
	for _, width := range []int{left, m.columns - 1 - left} {
		p, err := openPane(m.shell, dir, width, m.splitHeight(), m.events)
		if err != nil {
			return err
		}
		m.panes = append(m.panes, p)
	}
	m.layout()
	return nil
}

// splitHeight reserves the controls row while keeping at least one shell row.
// New shells need this size before they join the layout and draw their first prompt.
func (m *mux) splitHeight() int { return max(1, m.rows-1) }

// height is the number of rows that layout and draw give each shell.
func (m *mux) height() int {
	if len(m.panes) == 1 {
		return m.rows
	}
	return m.splitHeight()
}

// layout scales the preferred widths to the outer TTY and notifies each visible shell.
func (m *mux) layout() {
	m.dirty = true
	cells := m.columns - len(m.panes) + 1
	compact := cells < len(m.panes)
	remainingWeight := 0
	for _, p := range m.panes {
		remainingWeight += p.weight
	}
	offset := 0
	for i, p := range m.panes {
		// In an unusually narrow window, keep every shell alive and show the focused one.
		p.visible = !compact || i == m.active
		if !p.visible {
			continue
		}
		width := m.columns
		if !compact {
			reserved := len(m.panes) - i - 1
			width = max(1, min((cells*p.weight+remainingWeight/2)/remainingWeight, cells-reserved))
			cells -= width
			remainingWeight -= p.weight
		}
		p.offset = offset
		if err := p.resize(width, m.height()); err != nil {
			m.message = err.Error()
		}
		offset += width + 1
	}
}

// rememberWidths makes the current column sizes the baseline for a manual layout change.
func (m *mux) rememberWidths() {
	if m.columns < 2*len(m.panes)-1 {
		return
	}
	for _, p := range m.panes {
		p.weight = p.screen.Width()
	}
}

// split opens a new shell to the right without restarting the focused shell or its job.
func (m *mux) split() {
	p := m.panes[m.active]
	width := p.screen.Width()
	if width < minSplitWidth*2+1 || m.columns < 2*len(m.panes)-1 {
		m.message = "No room to split"
		return
	}
	right := (width - 1) / 2
	added, err := openPane(m.shell, p.directory, right, m.splitHeight(), m.events)
	if err != nil {
		m.message = err.Error()
		return
	}
	m.rememberWidths()
	p.weight = width - 1 - right
	m.active++
	m.panes = slices.Insert(m.panes, m.active, added)
	m.layout()
}

// adjust transfers columns between the focused pane and its nearest neighbor.
func (m *mux) adjust(delta int) {
	if len(m.panes) < 2 || m.columns < 2*len(m.panes)-1 {
		return
	}
	neighbor := m.active + 1
	if neighbor == len(m.panes) {
		neighbor = m.active - 1
	}
	m.rememberWidths()
	p, other := m.panes[m.active], m.panes[neighbor]
	if delta < 0 {
		p, other = other, p
	}
	step := min(4, other.weight-minSplitWidth)
	if step > 0 {
		p.weight += step
		other.weight -= step
		m.layout()
	}
}

// remove gives the closed pane's space to a neighbor and preserves the remaining jobs.
func (m *mux) remove(index int) {
	m.rememberWidths()
	p := m.panes[index]
	if m.pasteTarget == p {
		m.pasteTarget = nil
	}
	m.panes = slices.Delete(m.panes, index, index+1)
	if len(m.panes) > 0 {
		// A right-hand split gives its width and focus back to the pane on its left.
		neighbor := max(0, index-1)
		m.panes[neighbor].weight += p.weight + 1
		if m.active == index {
			m.active = neighbor
		} else if m.active > index {
			m.active--
		}
		m.layout()
	}
	p.close()
}

// handleOutput applies data only while the originating pane still belongs to this session.
func (m *mux) handleOutput(event paneEvent) {
	index := slices.Index(m.panes, event.pane)
	if index < 0 {
		return
	}
	if event.exited {
		m.remove(index)
		return
	}
	event.pane.screen.Write(event.output)
	m.dirty = true
}

// handleInput routes raw keys and complete commands. A paste stays with its original pane.
func (m *mux) handleInput(kind inputKind, data []byte) {
	if len(m.panes) == 0 {
		return
	}
	p := m.panes[m.active]
	if kind == shellInput {
		// Typing dismisses an error covering the single shell's bottom row.
		if len(m.panes) == 1 && m.message != "" {
			m.message, m.dirty = "", true
		}
		p.send(data)
		return
	}
	m.awaiting, m.message, m.dirty = false, "", true
	switch kind {
	case commandPrefix:
		m.awaiting = true
	case focusLeft, focusRight:
		if kind == focusLeft {
			m.active = max(0, m.active-1)
		} else {
			m.active = min(len(m.panes)-1, m.active+1)
		}
		m.layout()
	case splitPane:
		m.split()
	case closePane:
		m.remove(m.active)
	case narrowPane:
		m.adjust(-1)
	case widenPane:
		m.adjust(1)
	case beginPaste:
		m.pasteTarget, m.pasteWrapped = p, p.bracketed
		if m.pasteWrapped {
			p.send([]byte(pasteStart))
		}
	case pasteInput:
		if m.pasteTarget != nil {
			m.pasteTarget.send(data)
		}
	case endPaste:
		if m.pasteTarget != nil && m.pasteWrapped {
			m.pasteTarget.send([]byte(pasteEnd))
		}
		m.pasteTarget = nil
	}
}

// shutdown cleans up partially started sessions as well as a normal terminal disconnect.
func (m *mux) shutdown() {
	close(m.done)
	for _, p := range m.panes {
		p.close()
	}
}
