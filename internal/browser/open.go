// Package browser opens a URL in whatever the desktop uses to show web pages.
//
// It shells out to the system's own opener rather than trying to locate a
// browser: which one is the right one is the desktop's business, not ours, and
// the user may well have configured it.
package browser

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// ErrUnavailable means there is nothing here that could show a web page: a
// remote session over SSH, a container, a machine with no graphical session.
// The caller is expected to print the URL instead of failing.
var ErrUnavailable = errors.New("no browser available on this machine")

// Open shows a URL in the user's browser and returns as soon as it has been
// handed over.
//
// It does not wait for the opener to finish: xdg-open sometimes execs the
// browser itself and only returns when the browser is closed, and blocking the
// command until then would be absurd.
func Open(url string) error {
	if url == "" {
		return errors.New("there is no URL to open")
	}

	candidates := openers()
	if len(candidates) == 0 {
		return ErrUnavailable
	}

	var lastErr error = ErrUnavailable
	for _, argv := range candidates {
		path, err := exec.LookPath(argv[0])
		if err != nil {
			continue
		}
		cmd := exec.Command(path, append(argv[1:], url)...)
		// The opener's own chatter would land in the middle of our output, and
		// it has nothing to say that the user needs.
		cmd.Stdout, cmd.Stderr = nil, nil
		if err := cmd.Start(); err != nil {
			lastErr = err
			continue
		}
		// The child outlives us on purpose; reaping it in the background keeps
		// no zombie around in the meantime.
		go func() { _ = cmd.Wait() }()
		return nil
	}
	return lastErr
}

// openers lists the commands to try, in order of preference.
func openers() [][]string {
	var candidates [][]string

	// $BROWSER wins: someone who set it means it. It is a colon-separated list
	// and its entries may carry a %s placeholder, which we do not support —
	// those are skipped rather than launched with the URL in the wrong place.
	for _, entry := range strings.Split(os.Getenv("BROWSER"), ":") {
		entry = strings.TrimSpace(entry)
		if entry == "" || strings.Contains(entry, "%") {
			continue
		}
		candidates = append(candidates, strings.Fields(entry))
	}

	switch runtime.GOOS {
	case "darwin":
		candidates = append(candidates, []string{"open"})
	case "windows":
		candidates = append(candidates, []string{"rundll32", "url.dll,FileProtocolHandler"})
	default:
		// Without a display server there is no browser to open, and trying
		// anyway prints a stack trace from xdg-open. WSL is the exception: it
		// has no display of its own and hands the URL to Windows.
		if !hasDisplay() && !hasCommand("wslview") {
			return candidates
		}
		candidates = append(candidates,
			[]string{"xdg-open"},
			[]string{"gio", "open"},
			[]string{"wslview"},
			[]string{"x-www-browser"},
			[]string{"sensible-browser"},
		)
	}
	return candidates
}

func hasDisplay() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func hasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
