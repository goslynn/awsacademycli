package awscreds

import (
	"os"
	"strings"
	"testing"
)

const ourCmd = "/usr/local/bin/awsacademy creds"

func TestDefaultProfileFreeWhenNothingConfigured(t *testing.T) {
	useTempAWSDir(t)
	if got := DefaultProfileConflict(ourCmd); got != "" {
		t.Errorf("conflict = %q, expected none", got)
	}
}

func TestDefaultProfileDetectsConflicts(t *testing.T) {
	tests := []struct {
		name       string
		config     string
		creds      string
		wantSubstr string
	}{
		{
			// The static keys win over credential_process: if they are there,
			// configuring it would achieve nothing.
			name:       "static keys",
			creds:      "[default]\naws_access_key_id = AKIAREAL\naws_secret_access_key = s\n",
			wantSubstr: "static keys",
		},
		{
			name:       "another credential_process",
			config:     "[default]\ncredential_process = /usr/bin/another-tool\n",
			wantSubstr: "another-tool",
		},
		{
			name:       "SSO session",
			config:     "[default]\nsso_session = work\nsso_account_id = 1234\n",
			wantSubstr: "sso_session",
		},
		{
			name:       "assumed role",
			config:     "[default]\nrole_arn = arn:aws:iam::1234:role/Admin\nsource_profile = base\n",
			wantSubstr: "role_arn",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useTempAWSDir(t)
			if tt.config != "" {
				os.WriteFile(ConfigPath(), []byte(tt.config), 0o644)
			}
			if tt.creds != "" {
				os.WriteFile(CredentialsPath(), []byte(tt.creds), 0o600)
			}

			got := DefaultProfileConflict(ourCmd)
			if got == "" {
				t.Fatal("expected a conflict to be detected")
			}
			if !strings.Contains(got, tt.wantSubstr) {
				t.Errorf("conflict = %q, expected it to mention %q", got, tt.wantSubstr)
			}
		})
	}
}

func TestDefaultProfileOursIsNotAConflict(t *testing.T) {
	useTempAWSDir(t)
	if err := ConfigureDefaultProfile(ourCmd, "us-east-1"); err != nil {
		t.Fatal(err)
	}
	// Configuring it again when it is already us must not ask for confirmation.
	if got := DefaultProfileConflict(ourCmd); got != "" {
		t.Errorf("conflict = %q, there should be none with our own command", got)
	}
	if !IsDefaultProfileOurs(ourCmd) {
		t.Error("IsDefaultProfileOurs should be true")
	}
}

func TestConfigureDefaultProfilePreservesOtherProfiles(t *testing.T) {
	useTempAWSDir(t)
	existing := `[profile work]
region = eu-west-1
sso_session = corp

[profile academy]
region = us-east-1
credential_process = /usr/local/bin/awsacademy creds
`
	os.WriteFile(ConfigPath(), []byte(existing), 0o644)

	if err := ConfigureDefaultProfile(ourCmd, "us-east-1"); err != nil {
		t.Fatal(err)
	}
	got := readConfig(t)
	for _, want := range []string{"[profile work]", "sso_session", "eu-west-1", "[profile academy]", "[default]"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is missing from:\n%s", want, got)
		}
	}
}

func TestRemoveDefaultProfileOnlyRemovesOurs(t *testing.T) {
	useTempAWSDir(t)
	os.WriteFile(ConfigPath(), []byte("[default]\ncredential_process = /usr/bin/someone-else\n"), 0o644)

	// It is not ours, so it is left alone.
	removed, err := RemoveDefaultProfile(ourCmd)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("we should not withdraw someone else's credential_process")
	}
	if !strings.Contains(readConfig(t), "/usr/bin/someone-else") {
		t.Error("the other credential_process should still be there")
	}
}

func TestRemoveDefaultProfileKeepsUserSettings(t *testing.T) {
	useTempAWSDir(t)
	os.WriteFile(ConfigPath(), []byte("[default]\nregion = sa-east-1\noutput = table\n"), 0o644)
	if err := ConfigureDefaultProfile(ourCmd, "us-east-1"); err != nil {
		t.Fatal(err)
	}

	removed, err := RemoveDefaultProfile(ourCmd)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("expected it to be withdrawn")
	}

	got := readConfig(t)
	if strings.Contains(got, "credential_process") {
		t.Errorf("it should have been removed:\n%s", got)
	}
	// The region the person set by hand is not ours: it stays.
	if !strings.Contains(got, "sa-east-1") || !strings.Contains(got, "table") {
		t.Errorf("user settings were lost:\n%s", got)
	}
}

func TestRemoveDefaultProfileDropsEmptySection(t *testing.T) {
	useTempAWSDir(t)
	// With no prior region, the section is entirely ours...
	os.WriteFile(ConfigPath(), []byte(""), 0o644)
	if err := ConfigureDefaultProfile(ourCmd, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveDefaultProfile(ourCmd); err != nil {
		t.Fatal(err)
	}
	// ...so once we withdraw, no empty section should be left behind.
	if got := readConfig(t); strings.Contains(got, "[default]") {
		t.Errorf("the empty section should be gone:\n%s", got)
	}
}

func readConfig(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
