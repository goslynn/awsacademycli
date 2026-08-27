// Package awscreds connects the lab credentials to the AWS CLI.
//
// There are two paths, and the tool supports both:
//
//   - credential_process: the CLI invokes this binary when it needs
//     credentials, caches them and renews them on its own. Nothing expired is
//     left on disk.
//   - Writing ~/.aws/credentials: the classic mode, for tools that read the INI
//     file directly instead of using the SDK chain.
package awscreds

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goslynn/awsacademycli/internal/atomicfile"
	"github.com/goslynn/awsacademycli/internal/state"
	"gopkg.in/ini.v1"
)

// CredentialsPath returns the path of ~/.aws/credentials, honouring
// AWS_SHARED_CREDENTIALS_FILE.
func CredentialsPath() string {
	if p := os.Getenv("AWS_SHARED_CREDENTIALS_FILE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".aws/credentials"
	}
	return filepath.Join(home, ".aws", "credentials")
}

// ConfigPath returns the path of ~/.aws/config, honouring AWS_CONFIG_FILE.
func ConfigPath() string {
	if p := os.Getenv("AWS_CONFIG_FILE"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".aws/config"
	}
	return filepath.Join(home, ".aws", "config")
}

// loadINI opens an AWS file, treating its absence as an empty file.
func loadINI(path string) (*ini.File, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		raw = nil
	} else if err != nil {
		return nil, err
	}
	f, err := ini.Load(raw)
	if err != nil {
		return nil, fmt.Errorf("%s could not be parsed: %w", path, err)
	}
	return f, nil
}

func saveINI(f *ini.File, path string, perm os.FileMode) error {
	var sb strings.Builder
	if _, err := f.WriteTo(&sb); err != nil {
		return err
	}
	return atomicfile.Write(path, []byte(sb.String()), perm)
}

// WriteSharedCredentials writes the profile into ~/.aws/credentials.
//
// The whole file is rewritten, so it is loaded first and only the profile's
// section is touched: the user's other profiles have to survive intact.
func WriteSharedCredentials(profile string, creds *state.Credentials) error {
	path := CredentialsPath()
	f, err := loadINI(path)
	if err != nil {
		return err
	}

	sec, err := f.NewSection(profile) // returns the existing one if present
	if err != nil {
		return err
	}
	sec.Key("aws_access_key_id").SetValue(creds.AccessKeyID)
	sec.Key("aws_secret_access_key").SetValue(creds.SecretAccessKey)
	sec.Key("aws_session_token").SetValue(creds.SessionToken)

	return saveINI(f, path, 0o600)
}

// RemoveSharedCredentials deletes the profile from ~/.aws/credentials.
//
// It is needed when enabling credential_process: within a single profile, the
// static keys in the credentials file win over the credential_process declared
// in config, so leaving them turns the provider into decoration and the user
// would keep using dead credentials.
func RemoveSharedCredentials(profile string) error {
	path := CredentialsPath()
	f, err := loadINI(path)
	if err != nil {
		return err
	}
	if f.Section(profile).Key("aws_access_key_id").String() == "" {
		return nil
	}
	f.DeleteSection(profile)
	return saveINI(f, path, 0o600)
}

// HasStaticCredentials reports whether the profile has static keys written.
func HasStaticCredentials(profile string) bool {
	f, err := loadINI(CredentialsPath())
	if err != nil {
		return false
	}
	return f.Section(profile).Key("aws_access_key_id").String() != ""
}

// configSectionName translates a profile into the ~/.aws/config section name,
// where everything other than "default" carries the "profile " prefix.
func configSectionName(profile string) string {
	if profile == "default" {
		return "default"
	}
	return "profile " + profile
}

// ConfigureCredentialProcess declares this binary as the profile's provider,
// preserving the region and any other settings the user already had.
func ConfigureCredentialProcess(profile, command, region string) error {
	path := ConfigPath()
	f, err := loadINI(path)
	if err != nil {
		return err
	}
	sec, err := f.NewSection(configSectionName(profile))
	if err != nil {
		return err
	}
	sec.Key("credential_process").SetValue(command)
	if region != "" && sec.Key("region").String() == "" {
		sec.Key("region").SetValue(region)
	}
	return saveINI(f, path, 0o644)
}

// CredentialProcessCommand returns the configured credential_process, if any.
func CredentialProcessCommand(profile string) string {
	f, err := loadINI(ConfigPath())
	if err != nil {
		return ""
	}
	return f.Section(configSectionName(profile)).Key("credential_process").String()
}

// ProfileRegion returns the region configured for the profile.
func ProfileRegion(profile string) string {
	f, err := loadINI(ConfigPath())
	if err != nil {
		return ""
	}
	return f.Section(configSectionName(profile)).Key("region").String()
}

// The "default" profile is the one the AWS CLI and every SDK use when none is
// given. Pointing it at this provider is the portable way to avoid having to
// type --profile: it lives in a configuration file, so it does not depend on
// the shell, the distribution or any environment variable.
const DefaultProfileName = "default"

// DefaultProfileConflict describes what is already configured as the default
// profile, or an empty string if it is free.
//
// Clobbering someone's default profile would silently break the rest of their
// AWS commands, so we have to look before writing.
func DefaultProfileConflict(ourCommand string) string {
	if creds, err := loadINI(CredentialsPath()); err == nil {
		if creds.Section(DefaultProfileName).Key("aws_access_key_id").String() != "" {
			return fmt.Sprintf("there are static keys in %s", CredentialsPath())
		}
	}

	cfg, err := loadINI(ConfigPath())
	if err != nil {
		return ""
	}
	sec := cfg.Section(DefaultProfileName)

	if cmd := sec.Key("credential_process").String(); cmd != "" {
		if cmd == ourCommand {
			return "" // it is already us: rewriting it breaks nothing
		}
		return fmt.Sprintf("there is already a credential_process: %s", cmd)
	}
	// Other common ways of defining the default profile.
	for _, key := range []string{"sso_session", "sso_start_url", "role_arn", "source_profile", "credential_source"} {
		if v := sec.Key(key).String(); v != "" {
			return fmt.Sprintf("it is already configured with %s = %s", key, v)
		}
	}
	return ""
}

// ConfigureDefaultProfile points the default profile at this provider.
func ConfigureDefaultProfile(command, region string) error {
	return ConfigureCredentialProcess(DefaultProfileName, command, region)
}

// RemoveDefaultProfile undoes the above.
//
// Only what we put there is withdrawn: if the credential_process is a different
// one, it is not ours and it is not touched. The section is deleted only if it
// ends up empty, so as not to take down a region the person set by hand.
func RemoveDefaultProfile(ourCommand string) (bool, error) {
	cfg, err := loadINI(ConfigPath())
	if err != nil {
		return false, err
	}
	sec := cfg.Section(DefaultProfileName)
	if sec.Key("credential_process").String() != ourCommand {
		return false, nil
	}

	sec.DeleteKey("credential_process")
	if len(sec.Keys()) == 0 {
		cfg.DeleteSection(DefaultProfileName)
	}
	return true, saveINI(cfg, ConfigPath(), 0o644)
}

// IsDefaultProfileOurs reports whether the default profile already points at us.
func IsDefaultProfileOurs(ourCommand string) bool {
	cfg, err := loadINI(ConfigPath())
	if err != nil {
		return false
	}
	return cfg.Section(DefaultProfileName).Key("credential_process").String() == ourCommand
}
