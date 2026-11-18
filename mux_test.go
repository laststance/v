package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

func TestLastShellReclaimsTheControlsRowAfterCloseOrExit(t *testing.T) {
	// Arrange
	m := newMux(81, 4)
	m.shell = "/bin/sh"
	defer m.shutdown()
	if err := m.start(); err != nil {
		t.Fatal(err)
	}
	original := m.panes[0]
	outer := vt.NewEmulator(81, 4)
	defer outer.Close()
	var keys inputDecoder
	drawBottomRow := func() string {
		t.Helper()
		if err := m.draw(outer); err != nil {
			t.Fatal(err)
		}
		rows := strings.Split(outer.String(), "\n")
		if len(rows) != 4 {
			t.Fatalf("wanted four rendered rows, got %q", rows)
		}
		return strings.TrimSpace(rows[3])
	}
	assertSize := func(p *pane, wantRows, wantCols int) {
		t.Helper()
		rows, cols, err := pty.Getsize(p.master)
		if err != nil {
			t.Fatal(err)
		}
		if rows != wantRows || cols != wantCols {
			t.Errorf("shell PTY is %dx%d, want %dx%d (rows x columns)", rows, cols, wantRows, wantCols)
		}
	}
	assertOriginalFillsWindow := func() {
		t.Helper()
		assertSize(original, 4, 81)
		m.handleOutput(paneEvent{pane: original, output: []byte("\x1b[4;1Hlast row")})
		if row := drawBottomRow(); row != "last row" {
			t.Errorf("bottom row displays %q, want original shell output %q", row, "last row")
		}
	}
	if row := drawBottomRow(); !strings.HasPrefix(row, "1/2  Ctrl+G") {
		t.Fatalf("initial bottom row displays %q, want split controls", row)
	}

	// Act: close the right shell with the keyboard.
	keys.feed([]byte("\a\x1b[C\ax"), false, m.handleInput)

	// Assert
	assertOriginalFillsWindow()

	// Act: split the surviving shell again.
	keys.feed([]byte("\as"), false, m.handleInput)

	// Assert
	if row := drawBottomRow(); !strings.HasPrefix(row, "2/2  Ctrl+G") {
		t.Fatalf("bottom row after splitting displays %q, want split controls", row)
	}
	assertSize(original, 3, 40)
	assertSize(m.panes[1], 3, 40)

	// Act: the new shell exits on its own.
	m.handleOutput(paneEvent{pane: m.panes[1], exited: true})

	// Assert
	assertOriginalFillsWindow()
}

func TestNarrowShellShowsSplitErrorUntilTheNextKey(t *testing.T) {
	// Arrange
	m := newMux(8, 4)
	m.shell = "/bin/sh"
	defer m.shutdown()
	if err := m.start(); err != nil {
		t.Fatal(err)
	}
	var keys inputDecoder
	keys.feed([]byte("\ax"), false, m.handleInput)
	p := m.panes[0]
	m.handleOutput(paneEvent{pane: p, output: []byte("\x1b[4;1Hprompt")})
	outer := vt.NewEmulator(8, 4)
	defer outer.Close()
	assertBottomRow := func(want string) {
		t.Helper()
		// The event loop only draws dirty frames, even when the shell has echo disabled.
		if m.dirty {
			if err := m.draw(outer); err != nil {
				t.Fatal(err)
			}
			m.dirty = false
		}
		lines := strings.Split(outer.String(), "\n")
		if got := strings.TrimSpace(lines[len(lines)-1]); got != want {
			t.Errorf("bottom row displays %q, want %q", got, want)
		}
	}
	assertBottomRow("prompt")

	// Act
	keys.feed([]byte("\a"), false, m.handleInput)
	assertBottomRow("s split")
	keys.feed([]byte("s"), false, m.handleInput)

	// Assert
	assertBottomRow("No room")
	rows, cols, err := pty.Getsize(p.master)
	if err != nil || rows != 4 || cols != 8 {
		t.Fatalf("shell PTY with error visible is %dx%d, want 4x8, error %v", rows, cols, err)
	}
	m.handleOutput(paneEvent{pane: p, output: []byte("\x1b[4;1Hready!")})
	assertBottomRow("No room")
	keys.feed([]byte("z"), false, m.handleInput)
	assertBottomRow("ready!")
}

func TestClosingARightHandSplitRestoresTheOriginalShellAndItsWidth(t *testing.T) {
	// Arrange
	m := newMux(81, 16)
	m.shell = "/bin/sh"
	defer m.shutdown()
	if err := m.start(); err != nil {
		t.Fatal(err)
	}
	left, right := m.panes[0], m.panes[1]

	// Act
	m.handleInput(splitPane, nil)
	if len(m.panes) != 3 || m.active != 1 {
		t.Fatalf("new split should be focused between the original shells")
	}
	m.handleInput(widenPane, nil)
	if width := m.panes[1].screen.Width(); width != 23 {
		t.Fatalf("widened middle pane has %d columns, want 23", width)
	}
	m.handleInput(narrowPane, nil)
	m.handleInput(closePane, nil)

	// Assert
	if len(m.panes) != 2 || m.active != 0 || m.panes[0] != left || m.panes[1] != right {
		t.Fatal("closing the split replaced an original shell or moved focus away from the left shell")
	}
	if left.screen.Width() != 40 || right.screen.Width() != 40 {
		t.Fatalf("restored widths are %d and %d, want 40 each", left.screen.Width(), right.screen.Width())
	}
}

func TestWideCharactersAndTheCursorStayInsideTheirRespectivePanes(t *testing.T) {
	// Arrange
	left := &pane{screen: vt.NewEmulator(10, 3), visible: true}
	right := &pane{screen: vt.NewEmulator(10, 3), visible: true, offset: 11}
	m := &mux{columns: 21, rows: 4, active: 1, panes: []*pane{left, right}}
	left.screen.WriteString("\x1b[31m日本\x1b[0m")
	right.screen.WriteString("R")
	var output bytes.Buffer
	outer := vt.NewEmulator(21, 4)

	// Act
	if err := m.draw(&output); err != nil {
		t.Fatal(err)
	}
	outer.Write(output.Bytes())

	// Assert
	for column, want := range map[int]string{0: "日", 2: "本", 10: "│", 11: "R"} {
		if got := outer.CellAt(column, 0).Content; got != want {
			t.Errorf("column %d displays %q, want %q", column, got, want)
		}
	}
	if pos := outer.CursorPosition(); pos.X != 12 || pos.Y != 0 {
		t.Fatalf("cursor is at %v, want column 12 on row 0", pos)
	}
}
