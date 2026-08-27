package awscreds

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goslynn/awsacademycli/internal/state"
)

// useTempAWSDir apunta las rutas de AWS a un directorio de test.
func useTempAWSDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "credentials"))
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "config"))
	return dir
}

func TestWriteSharedCredentialsPreservesOtherProfiles(t *testing.T) {
	useTempAWSDir(t)

	// El usuario ya tiene perfiles suyos que no debemos tocar.
	existing := `[default]
aws_access_key_id = AKIADEFAULT
aws_secret_access_key = defaultsecret

[trabajo]
aws_access_key_id = AKIATRABAJO
aws_secret_access_key = trabajosecret

[academy]
aws_access_key_id = ASIAVIEJA
aws_secret_access_key = viejasecret
aws_session_token = tokenviejo
`
	if err := os.WriteFile(CredentialsPath(), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	err := WriteSharedCredentials("academy", &state.Credentials{
		AccessKeyID:     "ASIANUEVA",
		SecretAccessKey: "nuevasecret",
		SessionToken:    "tokennuevo",
	})
	if err != nil {
		t.Fatalf("WriteSharedCredentials: %v", err)
	}

	raw, err := os.ReadFile(CredentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)

	for _, want := range []string{"AKIADEFAULT", "AKIATRABAJO", "trabajosecret", "ASIANUEVA", "tokennuevo"} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %q en el fichero resultante:\n%s", want, got)
		}
	}
	for _, gone := range []string{"ASIAVIEJA", "tokenviejo"} {
		if strings.Contains(got, gone) {
			t.Errorf("%q debería haberse reemplazado:\n%s", gone, got)
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
	// Son credenciales: nadie más que el dueño debería poder leerlas.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permisos = %04o, esperaba 0600", perm)
	}
}

func TestRemoveSharedCredentialsForCredentialProcess(t *testing.T) {
	useTempAWSDir(t)

	existing := `[default]
aws_access_key_id = AKIADEFAULT

[academy]
aws_access_key_id = ASIAVIEJA
aws_secret_access_key = s
aws_session_token = t
`
	if err := os.WriteFile(CredentialsPath(), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if !HasStaticCredentials("academy") {
		t.Fatal("HasStaticCredentials debería detectar el bloque estático")
	}
	if err := RemoveSharedCredentials("academy"); err != nil {
		t.Fatal(err)
	}

	// Las claves estáticas ganan sobre credential_process, así que tienen que
	// desaparecer del todo para que el proveedor sirva de algo.
	if HasStaticCredentials("academy") {
		t.Error("el perfil academy debería haber desaparecido")
	}
	raw, _ := os.ReadFile(CredentialsPath())
	if !strings.Contains(string(raw), "AKIADEFAULT") {
		t.Errorf("el perfil default no debería haberse tocado:\n%s", raw)
	}
}

func TestConfigureCredentialProcessKeepsExistingSettings(t *testing.T) {
	useTempAWSDir(t)

	existing := `[profile academy]
region = sa-east-1
output = json

[profile otro]
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
	// No pisamos la region que el usuario eligió a mano.
	if got := ProfileRegion("academy"); got != "sa-east-1" {
		t.Errorf("region = %q, esperaba sa-east-1 (la del usuario)", got)
	}
	raw, _ := os.ReadFile(ConfigPath())
	if !strings.Contains(string(raw), "eu-west-1") {
		t.Errorf("el perfil 'otro' debería seguir intacto:\n%s", raw)
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
	// El CLI exige Version 1 y estas claves exactas, en CamelCase.
	for _, want := range []string{
		`"Version":1`,
		`"AccessKeyId":"ASIA123"`,
		`"SecretAccessKey":"secret"`,
		`"SessionToken":"token"`,
		`"Expiration":"2026-08-25T18:30:00Z"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %s en %s", want, got)
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
		{"vacías", &state.Credentials{}, true},
		{"vencidas", &state.Credentials{AccessKeyID: "A", Expiration: time.Now().Add(-time.Hour)}, true},
		// Margen: no entregamos credenciales que mueren en pleno request.
		{"por vencer", &state.Credentials{AccessKeyID: "A", Expiration: time.Now().Add(30 * time.Second)}, true},
		{"vigentes", &state.Credentials{AccessKeyID: "A", Expiration: time.Now().Add(2 * time.Hour)}, false},
		{"sin expiración conocida", &state.Credentials{AccessKeyID: "A"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.c.Expired(); got != tt.want {
				t.Errorf("Expired() = %v, esperaba %v", got, tt.want)
			}
		})
	}
}
