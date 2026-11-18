package main

import (
	"bytes"
	"slices"
	"testing"
)

func TestPastingShortcutsDoesNotCloseOrSwitchPanes(t *testing.T) {
	// Arrange
	var d inputDecoder
	var kinds []inputKind
	var received bytes.Buffer
	emit := func(kind inputKind, data []byte) {
		if kind == pasteInput {
			received.Write(data)
		} else {
			kinds = append(kinds, kind)
		}
	}

	// Act: Terminal may split either bracketed-paste marker between reads.
	for _, part := range []string{"\x1b[2", "00~one\n", "\ax\a\x06\a\x02日本語", "\x1b[20", "1~"} {
		d.feed([]byte(part), false, emit)
	}

	// Assert
	if !slices.Equal(kinds, []inputKind{beginPaste, endPaste}) || received.String() != "one\n\ax\a\x06\a\x02日本語" {
		t.Fatalf("paste actions %v, text %q", kinds, received.String())
	}
}

func TestFocusArrowCanArriveInSeparateReads(t *testing.T) {
	// Arrange
	var d inputDecoder
	var actions []inputKind
	emit := func(kind inputKind, _ []byte) { actions = append(actions, kind) }

	// Act
	for _, part := range []string{"\a", "\x1b", "[", "C", "\a\x1bO", "D"} {
		d.feed([]byte(part), false, emit)
	}

	// Assert
	want := []inputKind{commandPrefix, focusRight, commandPrefix, focusLeft}
	if !slices.Equal(actions, want) {
		t.Fatalf("focus actions %v, want %v", actions, want)
	}
}

func TestCtrlGThenCtrlFOrCtrlBFocusesTheAdjacentPane(t *testing.T) {
	// Arrange
	var d inputDecoder
	var actions []inputKind
	emit := func(kind inputKind, _ []byte) { actions = append(actions, kind) }

	// Act
	d.feed([]byte("\a"), false, emit)
	d.feed([]byte("\x06\a\x02"), false, emit)

	// Assert
	want := []inputKind{commandPrefix, focusRight, commandPrefix, focusLeft}
	if !slices.Equal(actions, want) {
		t.Fatalf("focus actions %v, want %v", actions, want)
	}
}

func TestEscapeAndEditingKeysReachTheShellUnchanged(t *testing.T) {
	// Arrange
	var d inputDecoder
	var received bytes.Buffer
	emit := func(kind inputKind, data []byte) {
		if kind == shellInput {
			received.Write(data)
		}
	}

	// Act
	d.feed([]byte("\x1b"), false, emit)
	d.feed(nil, true, emit)
	d.feed([]byte("日本語\x1b[1;5D\x03\x06\x02\a\a"), false, emit)

	// Assert
	if received.String() != "\x1b日本語\x1b[1;5D\x03\x06\x02\a" {
		t.Fatalf("shell received %q", received.String())
	}
}
