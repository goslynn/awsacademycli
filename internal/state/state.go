// Package state persists what the tool learns between runs: the Canvas
// session, where the lab lives and the latest credentials.
//
// All of this is a rebuildable cache, not configuration: it lives in
// $XDG_STATE_HOME/awsacademy and can be deleted without losing anything.
package state

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/adrg/xdg"
	"github.com/goslynn/awsacademycli/internal/atomicfile"
)

// Dir is the state directory.
func Dir() string { return filepath.Join(xdg.StateHome, "awsacademy") }

func path(name string) string { return filepath.Join(Dir(), name) }

// Session holds the Canvas and Vocareum cookies between runs.
//
// Canvas issues a persistent cookie when remember_me is requested, so a saved
// session usually survives weeks and the password is almost never used.
type Session struct {
	// Cookies maps host -> cookies for that host.
	Cookies  map[string][]*http.Cookie `json:"cookies"`
	SavedAt  time.Time                 `json:"saved_at"`
	UserID   int64                     `json:"user_id,omitempty"`
	UserName string                    `json:"user_name,omitempty"`
}

// Discovery is where we found the lab last time.
//
// None of this is hardcoded: the course changes every term and Vocareum's
// endpoints can move, so it is rediscovered whenever it fails.
type Discovery struct {
	CourseID     string `json:"course_id"`
	CourseName   string `json:"course_name,omitempty"`
	ModuleItemID string `json:"module_item_id"`
	ItemTitle    string `json:"item_title,omitempty"`
	// LaunchURL is the Canvas endpoint that triggers the LTI launch.
	LaunchURL string `json:"launch_url,omitempty"`
	// VocareumBase is the Vocareum origin the launch took us to.
	VocareumBase string `json:"vocareum_base,omitempty"`
	// Endpoints are the already-confirmed Vocareum paths. They are stored here
	// so they can be corrected by editing a file, without rebuilding the binary.
	Endpoints    Endpoints `json:"vocareum_endpoints,omitempty"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

// Endpoints are the Vocareum paths the lab uses. It duplicates the shape of
// vocareum.Endpoints on purpose: state is the lower layer and must not depend
// on the clients that use it.
type Endpoints struct {
	Status      string `json:"status,omitempty"`
	Start       string `json:"start,omitempty"`
	Stop        string `json:"stop,omitempty"`
	Credentials string `json:"credentials,omitempty"`
	Budget      string `json:"budget,omitempty"`
}

// Credentials are the STS credentials Vocareum exposes for the lab.
type Credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`
	Region          string `json:"region,omitempty"`
	// Expiration is estimated by the caller: Vocareum does not publish it
	// directly, it is derived from the lab session countdown.
	Expiration time.Time `json:"expiration"`
	FetchedAt  time.Time `json:"fetched_at"`
}

// Expired reports whether the credentials are no longer usable. A one-minute
// margin avoids handing out credentials that will die mid-request.
func (c *Credentials) Expired() bool {
	if c == nil || c.AccessKeyID == "" {
		return true
	}
	if c.Expiration.IsZero() {
		return false
	}
	return time.Now().Add(time.Minute).After(c.Expiration)
}

// ErrNotFound means the state file does not exist yet.
var ErrNotFound = errors.New("state not found")

func load(name string, dst any) error {
	raw, err := os.ReadFile(path(name))
	if errors.Is(err, os.ErrNotExist) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

func save(name string, src any, perm os.FileMode) error {
	raw, err := json.MarshalIndent(src, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(path(name), raw, perm)
}

func LoadSession() (*Session, error) {
	var s Session
	if err := load("session.json", &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// SaveSession uses 0600: session cookies are as sensitive as the password.
func (s *Session) Save() error { return save("session.json", s, 0o600) }

func LoadDiscovery() (*Discovery, error) {
	var d Discovery
	if err := load("discovery.json", &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Save uses 0644: these are public identifiers, not secrets.
func (d *Discovery) Save() error { return save("discovery.json", d, 0o644) }

func LoadCredentials() (*Credentials, error) {
	var c Credentials
	if err := load("creds.json", &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Credentials) Save() error { return save("creds.json", c, 0o600) }

// ClearSession deletes the saved session; the next login starts from scratch.
func ClearSession() error {
	err := os.Remove(path("session.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ClearDiscovery forces a rediscovery of the course and the lab.
func ClearDiscovery() error {
	err := os.Remove(path("discovery.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
