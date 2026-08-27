package ui

import (
	"context"
	"strings"
	"testing"
)

func TestRenderEmitsOneLinePerOption(t *testing.T) {
	// This is the invariant the repaint rests on: we go back up with \x1b[NA,
	// where N is the number of options. If render emitted a different number of
	// lines, every repaint would leave a copy of the list behind.
	options := []Option{
		{Label: "164446   AWS Academy Learner Lab", Hint: "created 2026-03-15"},
		{Label: "182613   AWS Academy Learner Lab", Hint: "created 2026-08-10"},
		{Label: "999999   Another course"},
	}

	var sb strings.Builder
	render(&sb, options, 1)

	if got := strings.Count(sb.String(), "\r\n"); got != len(options) {
		t.Errorf("render emitted %d line breaks, expected %d", got, len(options))
	}
}

func TestRenderMarksOnlyTheCursor(t *testing.T) {
	options := []Option{{Label: "one"}, {Label: "two"}, {Label: "three"}}

	var sb strings.Builder
	render(&sb, options, 2)
	out := sb.String()

	if got := strings.Count(out, "❯"); got != 1 {
		t.Errorf("there are %d markers, expected exactly 1", got)
	}
	// The marker has to be on the cursor's line, not on another one.
	lines := strings.Split(strings.TrimSuffix(out, "\r\n"), "\r\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, there are %d", len(lines))
	}
	if !strings.Contains(lines[2], "❯") || !strings.Contains(lines[2], "three") {
		t.Errorf("the marker is not on the selected option: %q", lines[2])
	}
}

func TestRenderClearsEachLine(t *testing.T) {
	// Without clearing the line, going from a long label to a short one would
	// leave the tail of the previous one visible.
	options := []Option{{Label: "a fairly long label"}, {Label: "short"}}

	var sb strings.Builder
	render(&sb, options, 0)

	if got := strings.Count(sb.String(), "\x1b[K"); got != len(options) {
		t.Errorf("there are %d line clears, expected one per option (%d)", got, len(options))
	}
}

func TestSelectSingleOptionDoesNotPrompt(t *testing.T) {
	// With a single option there is nothing to choose: asking would be noise.
	idx, err := Select(context.Background(), "which one?", []Option{{Label: "the only one"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if idx != 0 {
		t.Errorf("idx = %d", idx)
	}
}

func TestSelectRejectsEmptyOptions(t *testing.T) {
	if _, err := Select(context.Background(), "which one?", nil, 0); err == nil {
		t.Error("expected an error with no options")
	}
}
