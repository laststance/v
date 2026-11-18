package main

import "bytes"

const (
	prefixKey  = '\a' // Ctrl+G
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

type inputKind uint8

const (
	shellInput inputKind = iota
	commandPrefix
	cancelCommand
	focusLeft
	focusRight
	splitPane
	closePane
	narrowPane
	widenPane
	beginPaste
	pasteInput
	endPaste
)

// inputDecoder recognizes v's controls while preserving the shell's other input bytes.
// A partial arrow or paste delimiter may span multiple reads from Terminal.app.
type inputDecoder struct {
	pending []byte
	command bool
	pasting bool
}

// feed delivers complete actions in order. expired flushes a lone Escape to its shell.
func (d *inputDecoder) feed(data []byte, expired bool, emit func(inputKind, []byte)) {
	d.pending = append(d.pending, data...)
	for len(d.pending) > 0 {
		b := d.pending
		marker, payload := pasteStart, shellInput
		if d.pasting {
			marker, payload = pasteEnd, pasteInput
		}
		// Consume paste markers before interpreting any command-prefix bytes.
		if bytes.HasPrefix(b, []byte(marker)) {
			if d.pasting {
				emit(endPaste, nil)
			} else {
				emit(beginPaste, nil)
			}
			d.pasting, d.command = !d.pasting, false
			d.pending = b[len(marker):]
			continue
		}
		if !expired && bytes.HasPrefix([]byte(marker), b) {
			return
		}

		n := 1
		switch {
		case d.command && !d.pasting:
			// Both normal and application-cursor mode encode arrows on macOS.
			if b[0] == 0x1b && len(b) > 1 && (b[1] == '[' || b[1] == 'O') {
				if len(b) < 3 && !expired {
					return
				}
				n = min(3, len(b))
				switch b[n-1] {
				case 'D':
					emit(focusLeft, nil)
				case 'C':
					emit(focusRight, nil)
				default:
					emit(cancelCommand, nil)
				}
			} else {
				switch b[0] {
				case '\x02': // Ctrl+B
					emit(focusLeft, nil)
				case '\x06': // Ctrl+F
					emit(focusRight, nil)
				case 's':
					emit(splitPane, nil)
				case 'x':
					emit(closePane, nil)
				case '-':
					emit(narrowPane, nil)
				case '+':
					emit(widenPane, nil)
				case prefixKey:
					emit(cancelCommand, nil)
					emit(shellInput, b[:1])
				default:
					emit(cancelCommand, nil)
				}
			}
			d.command = false
		case !d.pasting && b[0] == prefixKey:
			d.command = true
			emit(commandPrefix, nil)
		default:
			// Forward ordinary input in runs, including UTF-8 and unrecognized escape sequences.
			n = len(b)
			special := "\x1b\a"
			if d.pasting {
				special = "\x1b"
			}
			if next := bytes.IndexAny(b[1:], special); next >= 0 {
				n = next + 1
			}
			emit(payload, b[:n])
		}
		d.pending = b[n:]
	}
}
