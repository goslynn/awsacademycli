package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Reads from stdin block and do not honour context cancellation, so each one
// runs in its own goroutine and races against ctx.Done(). Without this, a
// Ctrl+C during a question would do nothing: the program installs its own
// SIGINT handler — which disables the default termination — and would wait for
// a line forever.
//
// The goroutine left reading is abandoned on purpose: cancelling means
// finishing, so nobody is going to use that input again.

// Prompt asks for a line of text, honouring cancellation.
func Prompt(ctx Context, in io.Reader, label string) (string, error) {
	fmt.Fprint(os.Stderr, label)

	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := bufio.NewReader(in).ReadString('\n')
		ch <- result{line, err}
	}()

	select {
	case <-ctx.Done():
		fmt.Fprintln(os.Stderr)
		return "", ErrCancelled
	case r := <-ch:
		if r.err != nil && r.line == "" {
			if errors.Is(r.err, io.EOF) {
				// Ctrl+D at a question means giving up, not a failure.
				fmt.Fprintln(os.Stderr)
				return "", ErrCancelled
			}
			return "", r.err
		}
		return strings.TrimSpace(r.line), nil
	}
}

// PromptPassword asks for a password without echo, honouring cancellation.
func PromptPassword(ctx Context, label string) (string, error) {
	fmt.Fprint(os.Stderr, label)

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		fmt.Fprintln(os.Stderr)
		return Prompt(ctx, os.Stdin, "")
	}

	// The state is saved so echo can be restored if we cancel: leaving the
	// terminal mute on exit would force the person to run `reset`.
	state, err := term.GetState(fd)
	if err != nil {
		return "", err
	}

	type result struct {
		raw []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		raw, err := term.ReadPassword(fd)
		ch <- result{raw, err}
	}()

	select {
	case <-ctx.Done():
		_ = term.Restore(fd, state)
		fmt.Fprintln(os.Stderr)
		return "", ErrCancelled
	case r := <-ch:
		fmt.Fprintln(os.Stderr)
		if r.err != nil {
			return "", r.err
		}
		return strings.TrimSpace(string(r.raw)), nil
	}
}

// Confirm asks a yes-or-no question. defaultYes decides what an empty enter
// means.
func Confirm(ctx Context, in io.Reader, label string, defaultYes bool) (bool, error) {
	answer, err := Prompt(ctx, in, label)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(answer) {
	case "":
		return defaultYes, nil
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// Context is the only thing we need from context.Context. Declaring it this way
// keeps the package focused on the terminal and makes tests trivial.
type Context interface {
	Done() <-chan struct{}
}
