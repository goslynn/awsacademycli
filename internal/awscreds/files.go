// Package awscreds conecta las credenciales del laboratorio con el AWS CLI.
//
// Hay dos caminos, y la herramienta soporta los dos:
//
//   - credential_process: el CLI invoca a este binario cuando necesita
//     credenciales, las cachea y las renueva solo. Nada expirado queda en disco.
//   - Escribir ~/.aws/credentials: el modo clásico, para herramientas que leen
//     el fichero INI directamente en vez de usar la cadena del SDK.
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

// CredentialsPath devuelve la ruta de ~/.aws/credentials, respetando
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

// ConfigPath devuelve la ruta de ~/.aws/config, respetando AWS_CONFIG_FILE.
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

// loadINI abre un fichero de AWS, tratando la ausencia como fichero vacío.
func loadINI(path string) (*ini.File, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		raw = nil
	} else if err != nil {
		return nil, err
	}
	f, err := ini.Load(raw)
	if err != nil {
		return nil, fmt.Errorf("%s no se pudo parsear: %w", path, err)
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

// WriteSharedCredentials escribe el perfil en ~/.aws/credentials.
//
// Se reescribe el fichero entero, así que se carga primero y solo se toca la
// sección del perfil: los demás perfiles del usuario tienen que sobrevivir intactos.
func WriteSharedCredentials(profile string, creds *state.Credentials) error {
	path := CredentialsPath()
	f, err := loadINI(path)
	if err != nil {
		return err
	}

	sec, err := f.NewSection(profile) // devuelve la existente si ya está
	if err != nil {
		return err
	}
	sec.Key("aws_access_key_id").SetValue(creds.AccessKeyID)
	sec.Key("aws_secret_access_key").SetValue(creds.SecretAccessKey)
	sec.Key("aws_session_token").SetValue(creds.SessionToken)

	return saveINI(f, path, 0o600)
}

// RemoveSharedCredentials borra el perfil de ~/.aws/credentials.
//
// Hace falta al activar credential_process: dentro de un mismo perfil, las
// claves estáticas del fichero de credenciales ganan sobre el
// credential_process declarado en config, así que dejarlas convierte al
// proveedor en decorado y el usuario seguiría usando credenciales muertas.
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

// HasStaticCredentials indica si el perfil tiene claves estáticas escritas.
func HasStaticCredentials(profile string) bool {
	f, err := loadINI(CredentialsPath())
	if err != nil {
		return false
	}
	return f.Section(profile).Key("aws_access_key_id").String() != ""
}

// configSectionName traduce un perfil al nombre de sección de ~/.aws/config,
// donde todo lo que no sea "default" lleva el prefijo "profile ".
func configSectionName(profile string) string {
	if profile == "default" {
		return "default"
	}
	return "profile " + profile
}

// ConfigureCredentialProcess declara este binario como proveedor del perfil,
// preservando region y demás ajustes que el usuario ya tuviera.
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

// CredentialProcessCommand devuelve el credential_process configurado, si lo hay.
func CredentialProcessCommand(profile string) string {
	f, err := loadINI(ConfigPath())
	if err != nil {
		return ""
	}
	return f.Section(configSectionName(profile)).Key("credential_process").String()
}

// ProfileRegion devuelve la region configurada para el perfil.
func ProfileRegion(profile string) string {
	f, err := loadINI(ConfigPath())
	if err != nil {
		return ""
	}
	return f.Section(configSectionName(profile)).Key("region").String()
}

// El perfil "default" es el que usan el AWS CLI y todos los SDKs cuando no se
// indica ninguno. Apuntarlo a este proveedor es la forma portable de no tener
// que escribir --profile: vive en un fichero de configuración, así que no
// depende del shell, de la distribución ni de ninguna variable de entorno.
const DefaultProfileName = "default"

// DefaultProfileConflict describe qué hay ya configurado como perfil por
// defecto, o cadena vacía si está libre.
//
// Pisar el perfil por defecto de alguien le rompería en silencio el resto de
// sus comandos de AWS, así que hay que mirar antes de escribir.
func DefaultProfileConflict(ourCommand string) string {
	if creds, err := loadINI(CredentialsPath()); err == nil {
		if creds.Section(DefaultProfileName).Key("aws_access_key_id").String() != "" {
			return fmt.Sprintf("hay claves estáticas en %s", CredentialsPath())
		}
	}

	cfg, err := loadINI(ConfigPath())
	if err != nil {
		return ""
	}
	sec := cfg.Section(DefaultProfileName)

	if cmd := sec.Key("credential_process").String(); cmd != "" {
		if cmd == ourCommand {
			return "" // ya somos nosotros: reescribirlo no rompe nada
		}
		return fmt.Sprintf("ya hay un credential_process: %s", cmd)
	}
	// Otras formas habituales de definir el perfil por defecto.
	for _, key := range []string{"sso_session", "sso_start_url", "role_arn", "source_profile", "credential_source"} {
		if v := sec.Key(key).String(); v != "" {
			return fmt.Sprintf("ya está configurado con %s = %s", key, v)
		}
	}
	return ""
}

// ConfigureDefaultProfile apunta el perfil por defecto a este proveedor.
func ConfigureDefaultProfile(command, region string) error {
	return ConfigureCredentialProcess(DefaultProfileName, command, region)
}

// RemoveDefaultProfile deshace lo anterior.
//
// Solo se retira lo que pusimos nosotros: si el credential_process es otro, no
// es nuestro y no se toca. La sección se borra únicamente si queda vacía, para
// no llevarse por delante una region que la persona hubiera puesto a mano.
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

// IsDefaultProfileOurs indica si el perfil por defecto ya apunta a nosotros.
func IsDefaultProfileOurs(ourCommand string) bool {
	cfg, err := loadINI(ConfigPath())
	if err != nil {
		return false
	}
	return cfg.Section(DefaultProfileName).Key("credential_process").String() == ourCommand
}
