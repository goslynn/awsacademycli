// Package config carga y persiste la configuración del usuario.
//
// El fichero vive en $XDG_CONFIG_HOME/awsacademy/config.toml y contiene la
// contraseña de AWS Academy en claro, así que el paquete se niega a leerlo si
// los permisos son más laxos que 0600.
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

// DefaultCanvasBaseURL es el Canvas de AWS Academy. Configurable por si cambia
// de dominio, pero nunca hay que tocarlo en condiciones normales.
const DefaultCanvasBaseURL = "https://awsacademy.instructure.com"

// DefaultAWSProfile es el perfil de ~/.aws que mantiene la herramienta.
const DefaultAWSProfile = "academy"

// ErrNotConfigured indica que todavía no se corrió `awsacademy setup`.
var ErrNotConfigured = errors.New("no hay configuración: ejecutá 'awsacademy setup'")

// Config es el contenido de config.toml.
type Config struct {
	// Email es el usuario de AWS Academy (Canvas lo llama "Email").
	Email string `toml:"email"`
	// Password se guarda en claro. Vacío si se usa PasswordCommand.
	Password string `toml:"password,omitempty"`
	// PasswordCommand se ejecuta con `sh -c` y su stdout (recortado) es la
	// contraseña. Tiene precedencia sobre Password.
	PasswordCommand string `toml:"password_command,omitempty"`

	CanvasBaseURL string `toml:"canvas_base_url"`
	AWSProfile    string `toml:"aws_profile"`
	Region        string `toml:"region"`

	// CourseID fija el curso que tiene el laboratorio. Se escribe siempre,
	// aunque esté vacío: si se omitiera, nadie descubriría que la clave
	// existe justo cuando hace falta usarla.
	CourseID string `toml:"course_id"`
}

// Path devuelve la ruta de config.toml.
func Path() string {
	return filepath.Join(xdg.ConfigHome, "awsacademy", "config.toml")
}

// Load lee la configuración y valida sus permisos.
func Load() (*Config, error) {
	path := Path()
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotConfigured
	}
	if err != nil {
		return nil, err
	}
	// La contraseña vive acá en claro: cualquier bit fuera del dueño es un
	// problema, y preferimos frenar antes que filtrarla en silencio.
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf(
			"permisos inseguros en %s: %04o (esperado 0600) — corregí con: chmod 600 %s",
			path, perm, path)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := toml.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("config.toml inválido: %w", err)
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
		return errors.New("config.toml: falta 'email'")
	}
	if c.Password == "" && c.PasswordCommand == "" {
		return errors.New("config.toml: falta 'password' o 'password_command'")
	}
	return nil
}

// Save escribe config.toml con permisos 0600, creando el directorio si hace falta.
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

// ResolvePassword devuelve la contraseña, ejecutando PasswordCommand si está definido.
func (c *Config) ResolvePassword() (string, error) {
	if c.PasswordCommand == "" {
		return c.Password, nil
	}
	out, err := exec.Command("sh", "-c", c.PasswordCommand).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && len(exit.Stderr) > 0 {
			return "", fmt.Errorf("password_command falló: %s", strings.TrimSpace(string(exit.Stderr)))
		}
		return "", fmt.Errorf("password_command falló: %w", err)
	}
	pw := strings.TrimRight(string(out), "\r\n")
	if pw == "" {
		return "", errors.New("password_command no devolvió nada")
	}
	return pw, nil
}
