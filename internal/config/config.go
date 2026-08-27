// Package config loads and persists the user configuration.
//
// The file lives in $XDG_CONFIG_HOME/awsacademy/config.toml and contains the
// AWS Academy password in the clear, so the package refuses to read it if the
// permissions are looser than 0600.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/adrg/xdg"
	"github.com/goslynn/awsacademycli/internal/atomicfile"
	"github.com/pelletier/go-toml/v2"
)

// DefaultCanvasBaseURL is the AWS Academy Canvas. Configurable in case the
// domain changes, but it should never need touching under normal conditions.
const DefaultCanvasBaseURL = "https://awsacademy.instructure.com"

// DefaultAWSProfile is the ~/.aws profile the tool maintains.
const DefaultAWSProfile = "academy"

// ErrNotConfigured means `awsacademy setup` has not been run yet.
var ErrNotConfigured = errors.New("no configuration: run 'awsacademy setup'")

// Config is the contents of config.toml.
type Config struct {
	// Email is the AWS Academy username (Canvas calls it "Email").
	Email string `toml:"email"`
	// Password is stored in the clear. Empty when PasswordCommand is used.
	Password string `toml:"password,omitempty"`
	// PasswordCommand is run with `sh -c` and its (trimmed) stdout is the
	// password. It takes precedence over Password.
	PasswordCommand string `toml:"password_command,omitempty"`

	CanvasBaseURL string `toml:"canvas_base_url"`
	AWSProfile    string `toml:"aws_profile"`
	Region        string `toml:"region"`

	// CourseID pins the course that has the lab. It is always written, even
	// when empty: were it omitted, nobody would discover the key exists right
	// when they need to use it.
	CourseID string `toml:"course_id"`
}

// Path returns the path of config.toml.
func Path() string {
	return filepath.Join(xdg.ConfigHome, "awsacademy", "config.toml")
}

// Load reads the configuration and validates its permissions.
func Load() (*Config, error) {
	path := Path()
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotConfigured
	}
	if err != nil {
		return nil, err
	}
	// The password lives here in the clear: any bit outside the owner is a
	// problem, and we would rather stop than leak it silently.
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf(
			"insecure permissions on %s: %04o (expected 0600) — fix with: chmod 600 %s",
			path, perm, path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("invalid config.toml: %w", err)
	}
	cfg.applyDefaults()
	return &cfg, cfg.validate()
}

func (c *Config) applyDefaults() {
	if c.CanvasBaseURL == "" {
		c.CanvasBaseURL = DefaultCanvasBaseURL
	}
	if c.AWSProfile == "" {
		c.AWSProfile = DefaultAWSProfile
	}
	if c.Region == "" {
		c.Region = "us-east-1"
	}
	c.CanvasBaseURL = strings.TrimRight(c.CanvasBaseURL, "/")
}

func (c *Config) validate() error {
	if c.Email == "" {
		return errors.New("config.toml: 'email' is missing")
	}
	if c.Password == "" && c.PasswordCommand == "" {
		return errors.New("config.toml: 'password' or 'password_command' is missing")
	}
	return nil
}

// Save writes config.toml with permissions 0600, creating the directory if needed.
func (c *Config) Save() error {
	c.applyDefaults()
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	return atomicfile.Write(path, raw, 0o600)
}

// ResolvePassword returns the password, running PasswordCommand if it is set.
func (c *Config) ResolvePassword() (string, error) {
	if c.PasswordCommand == "" {
		return c.Password, nil
	}
	out, err := exec.Command("sh", "-c", c.PasswordCommand).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("password_command failed: %s", strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("password_command failed: %w", err)
	}
	pw := strings.TrimRight(string(out), "\r\n")
	if pw == "" {
		return "", errors.New("password_command returned nothing")
	}
	return pw, nil
}
