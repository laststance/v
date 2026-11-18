package main

import (
	"bytes"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// A pane owns one interactive shell, its controlling PTY, and its terminal screen.
type pane struct {
	screen      *vt.Emulator
	master      *os.File
	cmd         *exec.Cmd
	directory   string
	weight      int
	offset      int
	visible     bool
	bracketed   bool
	cursorKeys  bool
	stopped     chan struct{}
	exited      chan struct{}
	repliesDone chan struct{}
	wakeWriter  chan struct{}
	writeMu     sync.Mutex
	queued      bytes.Buffer
}

// paneEvent carries PTY output or a shell exit to the single owner of the layout.
type paneEvent struct {
	pane   *pane
	output []byte
	exited bool
}

// openPane starts a shell with its true dimensions before its first prompt is drawn.
func openPane(shell, directory string, width, height int, events chan<- paneEvent) (*pane, error) {
	cmd := exec.Command(shell)
	cmd.Dir = directory
	cmd.Env = append(cmd.Environ(), "TERM=xterm-256color", "TERM_PROGRAM=v")
	master, err := pty.StartWithSize(cmd, windowSize(width, height))
	if err != nil {
		return nil, err
	}
	// A pollable descriptor lets closing a pane interrupt blocked PTY writes and reads.
	fd, err := unix.FcntlInt(master.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err == nil {
		err = unix.SetNonblock(fd, true)
		if err != nil {
			unix.Close(fd)
		}
	}
	master.Close()
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		return nil, err
	}
	p := &pane{
		screen: vt.NewEmulator(width, height), master: os.NewFile(uintptr(fd), "v-pty"),
		cmd: cmd, directory: directory, weight: width, stopped: make(chan struct{}),
		exited: make(chan struct{}), repliesDone: make(chan struct{}), wakeWriter: make(chan struct{}, 1),
	}
	// The compositor displays the current screen, so it only needs the minimum history.
	p.screen.SetScrollbackSize(1)
	p.screen.SetCallbacks(vt.Callbacks{
		WorkingDirectory: p.noteDirectory,
		EnableMode:       func(mode ansi.Mode) { p.noteMode(mode, true) },
		DisableMode:      func(mode ansi.Mode) { p.noteMode(mode, false) },
	})
	go p.readOutput(events)
	go p.writeInput()
	go p.readReplies()
	go func() {
		cmd.Wait()
		close(p.exited)
		select {
		case events <- paneEvent{pane: p, exited: true}:
		case <-p.stopped:
		}
	}()
	return p, nil
}

// windowSize translates the layout's columns and rows to a PTY window size.
func windowSize(width, height int) *pty.Winsize {
	return &pty.Winsize{Cols: uint16(width), Rows: uint16(height)}
}

// noteDirectory follows local OSC 7 shell notifications for subsequent splits.
func (p *pane) noteDirectory(raw string) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "file" || !filepath.IsAbs(u.Path) {
		return
	}
	host, _ := os.Hostname()
	if u.Host == "" || u.Host == "localhost" || u.Host == host {
		p.directory = u.Path
	}
}

// noteMode tracks the two child modes that affect how outer keyboard input is delivered.
func (p *pane) noteMode(mode ansi.Mode, enabled bool) {
	switch mode {
	case ansi.ModeBracketedPaste:
		p.bracketed = enabled
	case ansi.ModeCursorKeys:
		p.cursorKeys = enabled
	}
}

// readOutput sends independent PTY reads to the compositor in arrival order.
func (p *pane) readOutput(events chan<- paneEvent) {
	for {
		buf := make([]byte, 16*1024)
		n, err := p.master.Read(buf)
		if n > 0 {
			select {
			case events <- paneEvent{pane: p, output: buf[:n]}:
			case <-p.stopped:
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// readReplies sends VT query replies back to the process that asked for them.
func (p *pane) readReplies() {
	defer close(p.repliesDone)
	buf := make([]byte, 4096)
	for {
		n, err := p.screen.Read(buf)
		if n > 0 {
			p.send(buf[:n])
		}
		if err != nil {
			return
		}
	}
}

// send queues input so a pane that stops reading cannot freeze its neighbors.
func (p *pane) send(data []byte) {
	p.writeMu.Lock()
	p.queued.Write(data)
	p.writeMu.Unlock()
	select {
	case p.wakeWriter <- struct{}{}:
	default:
	}
}

// writeInput is the sole writer to the PTY, preserving key, paste, and reply order.
func (p *pane) writeInput() {
	for {
		select {
		case <-p.stopped:
			return
		case <-p.wakeWriter:
			p.writeMu.Lock()
			pending := p.queued
			p.queued = bytes.Buffer{}
			p.writeMu.Unlock()
			if _, err := pending.WriteTo(p.master); err != nil {
				return
			}
		}
	}
}

// close hangs up this pane's foreground job and shell, then releases its descriptors.
func (p *pane) close() {
	close(p.stopped)
	if group, err := unix.IoctlGetInt(int(p.master.Fd()), unix.TIOCGPGRP); err == nil && group > 1 {
		unix.Kill(-group, syscall.SIGHUP)
	}
	p.cmd.Process.Signal(syscall.SIGHUP)
	p.master.Close()
	// Closing the reply pipe first wakes Read before the emulator is disposed.
	p.screen.InputPipe().(io.Closer).Close()
	<-p.repliesDone
	p.screen.Close()
	select {
	case <-p.exited:
	case <-time.After(300 * time.Millisecond):
		p.cmd.Process.Kill()
		<-p.exited
	}
}

// resize keeps the child application's reported size in sync with its emulator.
func (p *pane) resize(width, height int) error {
	if p.screen.Width() == width && p.screen.Height() == height {
		return nil
	}
	if err := pty.Setsize(p.master, windowSize(width, height)); err != nil && !errors.Is(err, syscall.EIO) {
		return err
	}
	p.screen.Resize(width, height)
	return nil
}
