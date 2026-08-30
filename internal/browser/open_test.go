package browser

import (
	"runtime"
	"testing"
)

func TestOpenersPrefersBrowserEnv(t *testing.T) {
	t.Setenv("BROWSER", "firefox --new-tab: : bad%sopener :lynx")

	got := openers()
	if len(got) < 2 {
		t.Fatalf("expected the two usable $BROWSER entries, got %v", got)
	}
	if got[0][0] != "firefox" || len(got[0]) != 2 || got[0][1] != "--new-tab" {
		t.Errorf("first opener = %v, expected firefox with its flag", got[0])
	}
	if got[1][0] != "lynx" {
		// The %s entry has to be skipped: we append the URL at the end, which
		// would put it in the wrong place for a placeholder command.
		t.Errorf("second opener = %v, expected lynx (the %%s entry skipped)", got[1])
	}
}

func TestOpenersGiveUpWithoutADisplay(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the display check only applies to the X11/Wayland desktops")
	}
	t.Setenv("BROWSER", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")

	// On a headless machine — an SSH session, a container — there is nothing
	// to open, and the caller is meant to print the URL instead.
	if got := openers(); len(got) != 0 && !hasCommand("wslview") {
		t.Errorf("expected no opener without a display, got %v", got)
	}
}

func TestOpenRejectsAnEmptyURL(t *testing.T) {
	if err := Open(""); err == nil {
		t.Fatal("expected an empty URL to be refused")
	}
}
