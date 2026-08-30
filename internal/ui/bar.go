package ui

import (
	"os"
	"strings"

	"golang.org/x/term"
)

// The gauge is drawn with block characters rather than '#' and '-' because the
// filled and empty halves have to be told apart at a glance, which is the whole
// point of drawing it instead of printing two numbers.
const (
	barFilled = '█'
	barEmpty  = '░'
)

// Bar draws a horizontal gauge of the given width for a fraction in [0,1].
//
// Values outside the range are clamped: a budget can be reported over its cap,
// and a bar that overflows the line is worse than one that is simply full.
func Bar(fraction float64, width int) string {
	if width <= 0 {
		return ""
	}
	switch {
	case fraction < 0:
		fraction = 0
	case fraction > 1:
		fraction = 1
	}

	filled := int(fraction*float64(width) + 0.5)
	// Anything spent at all deserves a visible mark: rounding a real but tiny
	// fraction down to an empty bar reads as "nothing spent yet", which is a
	// different fact.
	if filled == 0 && fraction > 0 {
		filled = 1
	}
	if filled == width && fraction < 1 {
		filled = width - 1
	}

	return strings.Repeat(string(barFilled), filled) +
		strings.Repeat(string(barEmpty), width-filled)
}

// Colour is the ANSI attribute a piece of text is painted with.
type Colour string

const (
	Green  Colour = "\x1b[32m"
	Yellow Colour = "\x1b[33m"
	Red    Colour = "\x1b[31m"
	Dim    Colour = "\x1b[2m"
	reset  string = "\x1b[0m"
)

// colourEnabled is resolved once, at start-up: it depends on where stdout
// points, and that does not change while the process runs.
var colourEnabled = detectColour()

func detectColour() bool {
	// https://no-color.org: any non-empty value disables colour.
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// Paint colours a piece of text, and leaves it alone when the output is not a
// terminal: escape codes in a pipe or in a log are noise, not colour.
func Paint(c Colour, text string) string {
	if !colourEnabled || text == "" {
		return text
	}
	return string(c) + text + reset
}
