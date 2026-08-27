package awscreds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goslynn/awsacademycli/internal/state"
)

// useTempAWSDir points the AWS paths at a test directory.
func useTempAWSDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	return dir
}

func TestWriteSharedCredentialsPreservesOtherProfiles(t *testing.T) {
	useTempAWSDir(t)

	// The user already has profiles of their own that we must not touch.
	existing := `[default]
aws_access_key_id = AKIADEFAULT
aws_secret_access_key = defaultsecret

[work]
aws_access_key_id = AKIAWORK
aws_secret_access_key = worksecret

[academy]
aws_access_key_id = ASIAOLD
aws_secret_access_key = oldsecret
aws_session_token = oldtoken
`
	if err := os.WriteFile(CredentialsPath(), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	err := WriteSharedCredentials("academy", &state.Credentials{
		AccessKeyID:     "ASIANEW",
		SecretAccessKey: "newsecret",
		SessionToken:    "newtoken",
	})
	if err != nil {
		t.Fatalf("WriteSharedCredentials: %v", err)
	}

	raw, err := os.ReadFile(CredentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)

	for _, want := range []string{"AKIADEFAULT", "AKIAWORK", "worksecret", "ASIANEW", "newtoken"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q is missing from the resulting file:\n%s", want, got)
		}
	}
	for _, gone := range []string{"ASIAOLD", "oldtoken"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q should have been replaced:\n%s", gone, got)
		}
	}
}

func TestWriteSharedCredentialsPermissions(t *testing.T) {
	useTempAWSDir(t)

	err := WriteSharedCredentials("academy", &state.Credentials{
		AccessKeyID: "ASIA1", SecretAccessKey: "s", SessionToken: "t",
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(CredentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	// These are credentials: nobody but the owner should be able to read them.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions = %04o, expected 0600", perm)
	}
}

func TestRemoveSharedCredentialsForCredentialProcess(t *testing.T) {
	useTempAWSDir(t)

	existing := `[default]
aws_access_key_id = AKIADEFAULT

[academy]
aws_access_key_id = ASIAOLD
aws_secret_access_key = s
aws_session_token = t
`
	if err := os.WriteFile(CredentialsPath(), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if !HasStaticCredentials("academy") {
		t.Fatal("HasStaticCredentials should detect the static block")
	}
	if err := RemoveSharedCredentials("academy"); err != nil {
		t.Fatal(err)
	}

	// The static keys win over credential_process, so they have to disappear
	// entirely for the provider to be of any use.
	if HasStaticCredentials("academy") {
		t.Error("the academy profile should be gone")
	}
	raw, _ := os.ReadFile(CredentialsPath())
	if !strings.Contains(string(raw), "AKIADEFAULT") {
		t.Errorf("the default profile should not have been touched:\n%s", raw)
	}
}

func TestConfigureCredentialProcessKeepsExistingSettings(t *testing.T) {
	useTempAWSDir(t)

	existing := `[profile academy]
region = sa-east-1
output = json

[profile other]
region = eu-west-1
`
	if err := os.WriteFile(ConfigPath(), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := ConfigureCredentialProcess("academy", "/usr/bin/awsacademy creds", "us-east-1"); err != nil {
		t.Fatal(err)
	}

	if got := CredentialProcessCommand("academy"); got != "/usr/bin/awsacademy creds" {
		t.Errorf("credential_process = %q", got)
	}
	// We do not clobber the region the user chose by hand.
	if got := ProfileRegion("academy"); got != "sa-east-1" {
		t.Errorf("region = %q, expected sa-east-1 (the user's)", got)
	}
	raw, _ := os.ReadFile(ConfigPath())
	if !strings.Contains(string(raw), "eu-west-1") {
		t.Errorf("the 'other' profile should still be intact:\n%s", raw)
	}
}

func TestProcessOutputFormat(t *testing.T) {
	exp := time.Date(2026, 8, 25, 18, 30, 0, 0, time.UTC)
	raw, err := ProcessOutput(&state.Credentials{
		AccessKeyID:     "ASIA123",
		SecretAccessKey: "secret",
		SessionToken:    "token",
		Expiration:      exp,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	// The CLI demands Version 1 and these exact keys, in CamelCase.
	for _, want := range []string{
		`"Version":1`,
		`"AccessKeyId":"ASIA123"`,
		`"SecretAccessKey":"secret"`,
		`"SessionToken":"token"`,
		`"Expiration":"2026-08-25T18:30:00Z"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("%s is missing from %s", want, got)
		}
	}
}

func TestCredentialsExpiry(t *testing.T) {
	tests := []struct {
		name string
		c    *state.Credentials
		want bool
	}{
		{"nil", nil, true},
		{"empty", &state.Credentials{}, true},
		{"expired", &state.Credentials{AccessKeyID: "A", Expiration: time.Now().Add(-time.Hour)}, true},
		// Margin: we do not hand out credentials that die mid-request.
		{"about to expire", &state.Credentials{AccessKeyID: "A", Expiration: time.Now().Add(30 * time.Second)}, true},
		{"valid", &state.Credentials{AccessKeyID: "A", Expiration: time.Now().Add(2 * time.Hour)}, false},
		{"expiry unknown", &state.Credentials{AccessKeyID: "A"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.Expired(); got != tt.want {
				t.Errorf("Expired() = %v, expected %v", got, tt.want)
			}
		})
	}
}
