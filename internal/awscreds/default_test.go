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
		t.Errorf("conflicto = %q, esperaba ninguno", got)
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
			// Las claves estáticas ganan sobre credential_process: si están,
			// configurarlo no serviría de nada.
			name:       "claves estáticas",
			creds:      "[default]\naws_access_key_id = AKIAREAL\naws_secret_access_key = s\n",
			wantSubstr: "claves estáticas",
		},
		{
			name:       "otro credential_process",
			config:     "[default]\ncredential_process = /usr/bin/otra-herramienta\n",
			wantSubstr: "otra-herramienta",
		},
		{
			name:       "sesión SSO",
			config:     "[default]\nsso_session = trabajo\nsso_account_id = 1234\n",
			wantSubstr: "sso_session",
		},
		{
			name:       "rol asumido",
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
				t.Fatal("esperaba detectar un conflicto")
			}
			if !strings.Contains(got, tt.wantSubstr) {
				t.Errorf("conflicto = %q, esperaba que mencionara %q", got, tt.wantSubstr)
			}
		})
	}
}

func TestDefaultProfileOursIsNotAConflict(t *testing.T) {
	useTempAWSDir(t)
	if err := ConfigureDefaultProfile(ourCmd, "us-east-1"); err != nil {
		t.Fatal(err)
	}
	// Volver a configurarlo cuando ya somos nosotros no debe pedir confirmación.
	if got := DefaultProfileConflict(ourCmd); got != "" {
		t.Errorf("conflicto = %q, no debería haberlo con nuestro propio comando", got)
	}
	if !IsDefaultProfileOurs(ourCmd) {
		t.Error("IsDefaultProfileOurs debería ser verdadero")
	}
}

func TestConfigureDefaultProfilePreservesOtherProfiles(t *testing.T) {
	useTempAWSDir(t)
	existing := `[profile trabajo]
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
	for _, want := range []string{"[profile trabajo]", "sso_session", "eu-west-1", "[profile academy]", "[default]"} {
		if !strings.Contains(got, want) {
			t.Errorf("falta %q en:\n%s", want, got)
		}
	}
}

func TestRemoveDefaultProfileOnlyRemovesOurs(t *testing.T) {
	useTempAWSDir(t)
	os.WriteFile(ConfigPath(), []byte("[default]\ncredential_process = /usr/bin/ajeno\n"), 0o644)

	// No es nuestro, así que no se toca.
	removed, err := RemoveDefaultProfile(ourCmd)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("no deberíamos retirar un credential_process ajeno")
	}
	if !strings.Contains(readConfig(t), "/usr/bin/ajeno") {
		t.Error("el credential_process ajeno debería seguir ahí")
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
		t.Fatal("esperaba que lo retirara")
	}

	got := readConfig(t)
	if strings.Contains(got, "credential_process") {
		t.Errorf("debería haberse quitado:\n%s", got)
	}
	// La region que la persona puso a mano no es nuestra: se queda.
	if !strings.Contains(got, "sa-east-1") || !strings.Contains(got, "table") {
		t.Errorf("se perdieron ajustes del usuario:\n%s", got)
	}
}

func TestRemoveDefaultProfileDropsEmptySection(t *testing.T) {
	useTempAWSDir(t)
	// Sin region previa, la sección la creamos nosotros enteramente...
	os.WriteFile(ConfigPath(), []byte(""), 0o644)
	if err := ConfigureDefaultProfile(ourCmd, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := RemoveDefaultProfile(ourCmd); err != nil {
		t.Fatal(err)
	}
	// ...así que al retirarnos no debería quedar una sección vacía de recuerdo.
	if got := readConfig(t); strings.Contains(got, "[default]") {
		t.Errorf("la sección vacía debería desaparecer:\n%s", got)
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
