package ui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// blocking simulates an input that never delivers data, like a terminal waiting
// for someone to type.
type blocking struct{}

func (blocking) Read([]byte) (int, error) {
	select {} // never returns
}

func TestPromptRespondsToCancellation(t *testing.T) {
	// This is the bug that motivated all of it: without honouring the context,
	// a Ctrl+C during a question left the process hanging forever, because
	// installing a signal handler disables the default termination.
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		_, err := Prompt(ctx, blocking{}, "Email: ")
		done <- err
	}()

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, ErrCancelled) {
			t.Errorf("err = %v, expected ErrCancelled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Prompt did not honour the cancellation: it would still be hanging")
	}
}

func TestPromptReadsLine(t *testing.T) {
	got, err := Prompt(context.Background(), strings.NewReader("ada@example.com\n"), "Email: ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ada@example.com" {
		t.Errorf("= %q", got)
	}
}

func TestPromptTrimsWhitespace(t *testing.T) {
	got, err := Prompt(context.Background(), strings.NewReader("  ada@example.com  \n"), "Email: ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ada@example.com" {
		t.Errorf("= %q, expected no leftover whitespace", got)
	}
}

func TestPromptTreatsEOFAsCancellation(t *testing.T) {
	// Ctrl+D at a question means giving up, not an error to report.
	_, err := Prompt(context.Background(), strings.NewReader(""), "Email: ")
	if !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, expected ErrCancelled", err)
	}
}

func TestConfirmDefaults(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		defaultYes bool
		want       bool
	}{
		{"enter with default yes", "\n", true, true},
		{"enter with default no", "\n", false, false},
		{"y", "y\n", false, true},
		{"yes", "yes\n", false, true},
		{"uppercase", "Y\n", false, true},
		{"n", "n\n", true, false},
		{"anything else is no", "maybe\n", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Confirm(context.Background(), strings.NewReader(tt.input), "yes? ", tt.defaultYes)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("= %v, expected %v", got, tt.want)
			}
		})
	}
}

func TestConfirmPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Confirm(ctx, blocking{}, "yes? ", false)
	if !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, expected ErrCancelled", err)
	}
}

func TestSelectNumberedRespondsToCancellation(t *testing.T) {
	// The variant without a terminal also reads stdin in a blocking way.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := selectNumbered(ctx, "which one?", []Option{{Label: "a"}, {Label: "b"}}, 0)
	if !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, expected ErrCancelled", err)
	}
}

var _ io.Reader = blocking{}
