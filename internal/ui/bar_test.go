package ui

import "testing"

func TestBar(t *testing.T) {
	tests := []struct {
		name     string
		fraction float64
		width    int
		want     string
	}{
		{"empty", 0, 4, "░░░░"},
		{"full", 1, 4, "████"},
		{"half", 0.5, 4, "██░░"},
		{"rounds to the nearest cell", 0.4, 10, "████░░░░░░"},
		{"clamps below", -1, 4, "░░░░"},
		{"clamps above", 2, 4, "████"},
		{"zero width", 0.5, 0, ""},
		// A tiny but real spend still shows one cell, and an almost-full one
		// still shows one empty cell: 0% and 100% mean something exact.
		{"tiny is visible", 0.001, 10, "█░░░░░░░░░"},
		{"almost full is not full", 0.999, 10, "█████████░"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Bar(tt.fraction, tt.width); got != tt.want {
				t.Errorf("Bar(%v, %d) = %q, expected %q", tt.fraction, tt.width, got, tt.want)
			}
		})
	}
}

func TestPaintIsInertWithoutATerminal(t *testing.T) {
	// Tests do not run against a terminal, so colour must be off: escape codes
	// leaking into a pipe is exactly what Paint is meant to prevent.
	if got := Paint(Red, "text"); got != "text" {
		t.Errorf("Paint = %q, expected it to leave the text alone", got)
	}
}
