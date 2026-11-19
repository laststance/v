package main

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// palette holds the SGR parameters of v's own chrome: the pane divider and the controls row.
// Every color lives here so a future theme can restyle the chrome by swapping one value.
type palette struct {
	divider   string // the │ between panes, from the top row through the controls row
	position  string // the focused pane number, such as 1/2
	separator string // the · after the pane number
	prefix    string // the resting ^G cue
	hint      string // the words after the resting cue
	key       string // a key to press, including ^G while v waits for the next key
	label     string // what a key does
	message   string // feedback such as "No room to split"
}

// quietPalette is the default look: dim grayscale text with no fill, so the chrome stays behind shell output.
var quietPalette = palette{
	divider:   "38;5;238",
	position:  "38;5;245",
	separator: "38;5;239",
	prefix:    "38;5;250",
	hint:      "38;5;243",
	key:       "1;38;5;255",
	label:     "38;5;245",
	message:   "38;5;252",
}

// span is a run of controls-row text drawn in one SGR style.
type span struct{ text, style string }

// spanWidth counts the terminal cells a group occupies, so a wide character in a message counts twice.
// ← → · › count as one cell, which matches Terminal.app unless its ambiguous-width option is on.
func spanWidth(group []span) int {
	n := 0
	for _, s := range group {
		n += ansi.StringWidth(s.text)
	}
	return n
}

// controls lists the controls-row groups that draw shows from the second column.
// While split, the row rests on the pane number and a ^G cue; after Ctrl+G it names the keys
// that act next, and a message replaces both until the next command. A single shell shows
// the row only while v waits for a key or has a message. draw keeps or drops each group whole.
func (m *mux) controls() [][]span {
	style := quietPalette
	split := len(m.panes) > 1
	if split && m.rows == 1 {
		return nil
	}
	var groups [][]span
	lead := ""
	if split {
		groups = append(groups, []span{{fmt.Sprintf("%d/%d", m.active+1, len(m.panes)), style.position}})
		lead = " · "
	}
	// withLead separates the first group after the pane number from it.
	withLead := func(spans ...span) []span {
		if lead == "" {
			return spans
		}
		return append([]span{{lead, style.separator}}, spans...)
	}
	switch {
	case m.message != "":
		// Shorten a long message instead of dropping it, so the row still says what happened.
		room := m.columns - 1 - ansi.StringWidth(lead)
		for _, g := range groups {
			room -= spanWidth(g)
		}
		// Drop control characters: an error can quote a path a shell reported through OSC 7,
		// and its escape sequences must never reach the real terminal.
		visible := strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return -1
			}
			return r
		}, m.message)
		// A window too narrow for any of the message keeps just the pane number, not a bare separator.
		if message := ansi.Truncate(visible, max(0, room), ""); message != "" {
			groups = append(groups, withLead(span{message, style.message}))
		}
	case m.awaiting:
		bindings := [][2]string{{"←/→", "focus"}, {"s", "split"}, {"-/+", "width"}, {"x", "close"}, {"esc", "cancel"}}
		if !split {
			// Focus and width keys do nothing for a single shell.
			bindings = [][2]string{{"s", "split"}, {"x", "close"}, {"esc", "cancel"}}
		}
		groups = append(groups, withLead(span{"^G", style.key}))
		gap := " › "
		for _, b := range bindings {
			groups = append(groups, []span{{gap, style.label}, {b[0], style.key}, {" " + b[1], style.label}})
			gap = "  "
		}
	case split:
		groups = append(groups, withLead(span{"^G", style.prefix}, span{" for keys", style.hint}))
	default:
		return nil
	}
	return groups
}

// draw updates changed rows and positions the hardware cursor inside the focused shell.
func (m *mux) draw(out io.Writer) error {
	style := quietPalette
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
				fmt.Fprintf(&lines[y], "\x1b[%dG\x1b[%sm│\x1b[0m", p.offset, style.divider)
			}
			lines[y].WriteString(content)
		}
	}
	end := 0
	if groups := m.controls(); groups != nil {
		// Overlay only the composed frame so the single shell keeps updating underneath.
		row := &lines[m.rows-1]
		row.Reset()
		row.WriteByte(' ')
		end = 1
		// Keep or drop each group whole so a narrow window never shows half a key.
		for _, g := range groups {
			n := spanWidth(g)
			if end+n > m.columns {
				break
			}
			for _, s := range g {
				fmt.Fprintf(row, "\x1b[%sm%s\x1b[0m", s.style, s.text)
			}
			end += n
		}
	}
	if height < m.rows {
		// Carry each divider through the reserved controls row, leaving a blank cell after the text.
		row := &lines[m.rows-1]
		for _, p := range m.panes {
			if p.visible && p.offset > 0 && p.offset-1 > end {
				fmt.Fprintf(row, "\x1b[%dG\x1b[%sm│\x1b[0m", p.offset, style.divider)
			}
		}
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
