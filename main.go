package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"
)

const usage = `v — side-by-side shells

Run v in a terminal to open two shells in the current directory.
Press Ctrl+G, release it, then press one key:

  Left or Ctrl+B    Focus the pane on the left
  Right or Ctrl+F   Focus the pane on the right
  s                 Split the focused pane to its right
  - / +             Narrow / widen the focused pane
  x                 Close the focused pane
  Ctrl+G            Send a literal Ctrl+G to the shell
  Esc               Cancel

You can also type exit in a shell. Closing the last pane returns to your prompt.
In Terminal.app, Option-drag selects a rectangle for Command+C. Command+V pastes.
`

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		fmt.Print(usage)
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: v  (v --help for controls)")
		os.Exit(2)
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "v:", err)
		os.Exit(1)
	}
}

// run owns the outer TTY for one invocation and restores it after every exit path.
func run() error {
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if !term.IsTerminal(in) || !term.IsTerminal(out) {
		return errors.New("run v at an interactive terminal prompt")
	}
	columns, rows, err := term.GetSize(out)
	if err != nil {
		return err
	}
	if columns < 3 || rows < 2 {
		return errors.New("enlarge the terminal to display two panes")
	}
	m := newMux(columns, rows)
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGWINCH, syscall.SIGHUP, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(signals)

	saved, err := term.MakeRaw(in)
	if err != nil {
		return err
	}
	defer term.Restore(in, saved)
	defer fmt.Fprint(os.Stdout, "\x1b[?2004l\x1b[?1l\x1b[0m\x1b[?25h\x1b[?1049l")
	if _, err := io.WriteString(os.Stdout, "\x1b[?1049h\x1b[?2004h\x1b[0m\x1b[2J"); err != nil {
		return err
	}
	defer m.shutdown()
	if err := m.start(); err != nil {
		return err
	}

	input := make(chan []byte, 4)
	go func() {
		defer close(input)
		// Copy each read so the event loop owns its input even during fast pastes.
		for {
			buf := make([]byte, 8192)
			n, err := os.Stdin.Read(buf)
			if n > 0 {
				select {
				case input <- buf[:n]:
				case <-m.done:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	var decoder inputDecoder
	var escapeTimeout, frame <-chan time.Time
	for len(m.panes) > 0 {
		// Coalesce output bursts and leave an idle screen untouched for native selection.
		if m.dirty && frame == nil {
			frame = time.After(time.Second / 60)
		}
		select {
		case keyBytes, open := <-input:
			if !open {
				return nil
			}
			decoder.feed(keyBytes, false, m.handleInput)
			escapeTimeout = nil
			if len(decoder.pending) > 0 && !decoder.pasting {
				escapeTimeout = time.After(50 * time.Millisecond)
			}
		case <-escapeTimeout:
			escapeTimeout = nil
			decoder.feed(nil, true, m.handleInput)
		case event := <-m.events:
			m.handleOutput(event)
		case sig := <-signals:
			if sig != syscall.SIGWINCH {
				return nil
			}
			columns, rows, err := term.GetSize(out)
			if err != nil {
				return err
			}
			if columns > 0 && rows > 0 {
				m.columns, m.rows = columns, rows
				m.previous = nil
				m.layout()
			}
		case <-frame:
			frame = nil
			if err := m.draw(os.Stdout); err != nil {
				return err
			}
			m.dirty = false
		}
	}
	return nil
}
