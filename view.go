package main

import (
	"fmt"
	"io"
	"strings"
)

// controls supplies draw with a persistent split footer or a single shell's command feedback.
func (m *mux) controls() string {
	if len(m.panes) == 1 {
		if m.awaiting {
			return "s split   x close"
		}
		return m.message
	}
	if m.rows == 1 {
		return ""
	}
	hint := "Ctrl+G then  ←/→ focus   s split   -/+ width   x close"
	if m.awaiting {
		hint = "Choose  ←/Ctrl+B  →/Ctrl+F   s split   -/+ width   x close"
	}
	if m.message != "" {
		hint = m.message
	}
	return fmt.Sprintf(" %d/%d  %s", m.active+1, len(m.panes), hint)
}

// draw updates changed rows and positions the hardware cursor inside the focused shell.
func (m *mux) draw(out io.Writer) error {
	lines := make([]strings.Builder, m.rows)
	height := m.height()
	for _, p := range m.panes {
		if !p.visible {
			continue
		}
		// Each emulator renders its own attributes, Unicode cells, and alternate screen.
		for y, content := range strings.Split(p.screen.Render(), "\n") {
			if y >= height {
				break
			}
			if p.offset > 0 {
				fmt.Fprintf(&lines[y], "\x1b[%dG\x1b[90m│\x1b[0m", p.offset)
			}
			lines[y].WriteString(content)
		}
	}
	if controls := m.controls(); controls != "" {
		// Overlay only the composed frame so the single shell keeps updating underneath.
		row := &lines[m.rows-1]
		row.Reset()
		label := []rune(controls)
		fmt.Fprintf(row, "\x1b[7m%s\x1b[0m", string(label[:min(len(label), m.columns)]))
	}

	var changes strings.Builder
	next := make([]string, len(lines))
	for y := range lines {
		next[y] = lines[y].String()
		if y >= len(m.previous) || next[y] != m.previous[y] {
			fmt.Fprintf(&changes, "\x1b[%d;1H\x1b[2K%s", y+1, next[y])
		}
	}
	m.previous = next
	p := m.panes[m.active]
	pos := p.screen.CursorPosition()
	cursor := fmt.Sprintf("\x1b[%d;%dH", pos.Y+1, p.offset+pos.X+1)
	if p.cursorKeys {
		cursor += "\x1b[?1h"
	} else {
		cursor += "\x1b[?1l"
	}
	if !p.screen.CursorHidden() {
		cursor += "\x1b[?25h"
	}
	if changes.Len() == 0 && cursor == m.cursor {
		return nil
	}
	m.cursor = cursor
	_, err := io.WriteString(out, "\x1b[?25l"+changes.String()+cursor)
	return err
}
