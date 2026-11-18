# v

Run `v` at a shell prompt to open two independent shells side by side in the current terminal tab. Both start in the current directory using `$SHELL`. Closing the last pane returns to the original prompt.

## Install

With Go 1.26 or later on macOS, run:

```sh
go install laststance.io/v@latest
```

Run `v` from the directory you want both shells to open in. If your shell cannot find `v`, add Go's binary directory to your `PATH` (`$GOBIN`, or `$(go env GOPATH)/bin` by default).

## Controls

Press **Ctrl+G**, release it, then press one key. While the terminal is split, the bottom row shows the controls and the focused pane number. When one pane remains, its shell uses the full height. Press **Ctrl+G**, then `s`, to split it again.

| Key | Action |
| --- | --- |
| Left arrow or Ctrl+B | Focus the pane on the left |
| Right arrow or Ctrl+F | Focus the pane on the right |
| `s` | Open a new shell to the right of the focused pane |
| `-` or `+` | Narrow or widen the focused pane by up to four columns |
| `x` | Close the focused pane and hang up its running job |
| Ctrl+G | Send a literal Ctrl+G to the focused shell |

Typing `exit` in a shell also closes its pane. Other panes keep running.

In Terminal.app, use **⌘V** to paste into the focused shell. For a single line, drag to select and press **⌘C**. For several visible lines from one pane, hold **Option** while dragging to make a rectangular selection. The mouse remains available for Terminal.app text selection.

## Build

From this directory on macOS, run `go build -o v .`, then `./v`. Run `./v --help` for the controls.
