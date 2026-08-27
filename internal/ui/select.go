// Package ui holds the terminal interactions that do not fit in a flag.
package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"

	"golang.org/x/term"
)

// ErrCancelled means the person abandoned the selection.
var ErrCancelled = errors.New("selection cancelled")

// Option is a menu entry.
type Option struct {
	// Label is the main line.
	Label string
	// Hint is the detail that helps decide; it is shown dimmed.
	Hint string
}

// Select shows a menu and returns the chosen index.
//
// With a terminal you navigate with the arrows and confirm with Enter. Without
// one — a pipe, a script, a test — it falls back to a numbered list, because
// raw mode requires a real terminal and we do not want the tool to become
// unusable outside an interactive session.
func Select(ctx Context, prompt string, options []Option, defaultIdx int) (int, error) {
	if len(options) == 0 {
		return 0, errors.New("there are no options to choose from")
	}
	if defaultIdx < 0 || defaultIdx >= len(options) {
		defaultIdx = 0
	}
	if len(options) == 1 {
		return 0, nil
	}

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return selectNumbered(ctx, prompt, options, defaultIdx)
	}
	return selectInteractive(ctx, fd, prompt, options, defaultIdx)
}

func selectInteractive(ctx Context, fd int, prompt string, options []Option, cursor int) (int, error) {
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return selectNumbered(ctx, prompt, options, cursor)
	}
	// The terminal is left unusable if we exit without restoring it, even on an
	// error or a panic, so it is restored no matter what.
	defer term.Restore(fd, oldState)

	out := os.Stderr
	fmt.Fprintf(out, "%s\r\n", prompt)
	fmt.Fprint(out, "\x1b[?25l")       // hide the cursor while we navigate
	defer fmt.Fprint(out, "\x1b[?25h") // and give it back when we are done

	render(out, options, cursor)

	// Repainting is always the same: go back to the start of the block and draw
	// it again on top. Doing it without going up first leaves a copy of the
	// list below the previous one.
	repaint := func() {
		fmt.Fprintf(out, "\x1b[%dA", len(options))
		render(out, options, cursor)
	}

	// Raw mode disables ISIG, so Ctrl+C does not arrive as a signal but as byte
	// 3; that is handled below. The context is watched anyway in case the
	// cancellation comes from somewhere else.
	type keypress struct {
		buf []byte
		n   int
		err error
	}
	keys := make(chan keypress, 1)
	go func() {
		for {
			buf := make([]byte, 3)
			n, err := os.Stdin.Read(buf)
			keys <- keypress{buf, n, err}
			if err != nil {
				return
			}
		}
	}()

	for {
		var buf []byte
		var n int
		select {
		case <-ctx.Done():
			return 0, ErrCancelled
		case k := <-keys:
			if k.err != nil {
				return 0, k.err
			}
			buf, n = k.buf, k.n
		}

		switch {
		case n == 1 && (buf[0] == '\r' || buf[0] == '\n'):
			repaint()
			return cursor, nil

		case n == 1 && (buf[0] == 3 || buf[0] == 'q'): // Ctrl-C or q
			repaint()
			return 0, ErrCancelled

		case n == 1 && buf[0] == 'k', n == 3 && buf[0] == 0x1b && buf[2] == 'A':
			cursor = (cursor - 1 + len(options)) % len(options)

		case n == 1 && buf[0] == 'j', n == 3 && buf[0] == 0x1b && buf[2] == 'B':
			cursor = (cursor + 1) % len(options)

		case n == 1 && buf[0] >= '1' && buf[0] <= '9':
			// A shortcut: typing the number jumps straight to that option.
			if idx := int(buf[0] - '1'); idx < len(options) {
				cursor = idx
			}
		}
		repaint()
	}
}

// render draws the list with the current option marked.
func render(out io.Writer, options []Option, cursor int) {
	for i, opt := range options {
		// \x1b[K clears the rest of the line: without it, a short label would
		// leave the tail of the previous, longer one on screen.
		if i == cursor {
			fmt.Fprintf(out, "\x1b[K  \x1b[1;36m❯ %s\x1b[0m", opt.Label)
		} else {
			fmt.Fprintf(out, "\x1b[K    %s", opt.Label)
		}
		if opt.Hint != "" {
			fmt.Fprintf(out, "  \x1b[2m%s\x1b[0m", opt.Hint)
		}
		fmt.Fprint(out, "\r\n")
	}
}

// selectNumbered is the variant for when there is no interactive terminal.
func selectNumbered(ctx Context, prompt string, options []Option, defaultIdx int) (int, error) {
	fmt.Fprintf(os.Stderr, "%s\n", prompt)
	for i, opt := range options {
		line := fmt.Sprintf("  %d) %s", i+1, opt.Label)
		if opt.Hint != "" {
			line += "  " + opt.Hint
		}
		fmt.Fprintln(os.Stderr, line)
	}
	answer, err := Prompt(ctx, os.Stdin,
		fmt.Sprintf("\nChoose [1-%d, enter = %d]: ", len(options), defaultIdx+1))
	if err != nil {
		return 0, err
	}
	if answer == "" {
		return defaultIdx, nil
	}
	n, err := strconv.Atoi(answer)
	if err != nil || n < 1 || n > len(options) {
		return 0, fmt.Errorf("choose a number between 1 and %d", len(options))
	}
	return n - 1, nil
}
