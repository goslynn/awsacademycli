package vocareum

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/goslynn/awsacademycli/internal/httpx"
)

// Endpoints are the URLs that operate the lab.
//
// Vocareum exposes a single entry point, /util/vcput.php, and distinguishes the
// operation with the a= parameter. The URLs are stored complete and absolute
// because they carry session-specific parameters — stepid, version, mode, and
// sometimes an encd token — that cannot be reconstructed from outside: they
// have to be read from the lab page.
type Endpoints struct {
	Status      string `json:"status,omitempty"`
	Start       string `json:"start,omitempty"`
	Stop        string `json:"stop,omitempty"`
	Credentials string `json:"credentials,omitempty"`
}

// The vcput.php actions we care about. Vocareum serves several cloud providers
// from the same page (startaws, startazure, startgcp); only the AWS ones count
// here.
const (
	actionStart  = "startaws"
	actionStop   = "endaws"
	actionStatus = "getawsstatus"
	actionCreds  = "getaws"
)

// reVcput captures the API calls inside the page's HTML and JavaScript, with
// all of their parameters.
var reVcput = regexp.MustCompile(`["'\x60]([^"'\x60\s]*util/vcput\.php\?a=([A-Za-z_]+)[^"'\x60\s]*)["'\x60]`)

// DetectEndpoints reads from the lab page the URLs its own buttons use.
//
// It is the only reliable way to obtain them: they carry session identifiers,
// so they can neither be compiled in as constants nor survive from one course
// to the next.
func DetectEndpoints(page *httpx.Response) Endpoints {
	var found Endpoints

	for _, m := range reVcput.FindAllStringSubmatch(page.String(), -1) {
		raw, action := m[1], m[2]

		// They are stored absolute: the ones on the page are relative to /main/
		// and resolving them later, against a different base, would give the
		// wrong URL.
		ref, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		abs := page.URL.ResolveReference(ref).String()

		switch action {
		case actionStart:
			setIfEmpty(&found.Start, abs)
		case actionStop:
			setIfEmpty(&found.Stop, abs)
		case actionStatus:
			setIfEmpty(&found.Status, abs)
		case actionCreds:
			setIfEmpty(&found.Credentials, abs)
		}
	}
	return found
}

// setIfEmpty keeps the first occurrence: the page repeats some calls with
// partial parameters, and the first one is usually the complete one.
func setIfEmpty(dst *string, value string) {
	if *dst == "" {
		*dst = value
	}
}

// Complete reports whether we have everything needed to operate the lab.
func (e Endpoints) Complete() bool {
	return e.Status != "" && e.Start != "" && e.Stop != "" && e.Credentials != ""
}

// Missing names what is absent, so it can be said in an error.
func (e Endpoints) Missing() []string {
	var missing []string
	for _, f := range []struct {
		name, value string
	}{
		{"status", e.Status},
		{"start", e.Start},
		{"stop", e.Stop},
		{"credentials", e.Credentials},
	} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	return missing
}

// Merge overlays other endpoints on top of these; whatever other carries wins.
func (e Endpoints) Merge(other Endpoints) Endpoints {
	setIfNotEmpty(&e.Status, other.Status)
	setIfNotEmpty(&e.Start, other.Start)
	setIfNotEmpty(&e.Stop, other.Stop)
	setIfNotEmpty(&e.Credentials, other.Credentials)
	return e
}

func setIfNotEmpty(dst *string, value string) {
	if value != "" {
		*dst = value
	}
}

// resolve turns a path into an absolute URL against the Vocareum origin.
// The detected ones already come absolute and pass through unchanged.
func (e Endpoints) resolve(base, path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	ref, err := url.Parse(path)
	if err != nil {
		return base + path
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return base + path
	}
	return baseURL.ResolveReference(ref).String()
}

// StateDirHint names where the confirmed endpoints are stored. It lives here so
// that the diagnostic messages point at the right place.
func StateDirHint() string { return "~/.local/state/awsacademy" }
