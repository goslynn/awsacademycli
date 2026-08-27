// Package state persiste lo que la herramienta aprende entre ejecuciones:
// la sesión de Canvas, dónde vive el laboratorio y las últimas credenciales.
//
// Todo esto es caché reconstruible, no configuración: vive en
// $XDG_STATE_HOME/awsacademy y se puede borrar sin perder nada.
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

// Dir es el directorio de estado.
func Dir() string { return filepath.Join(xdg.StateHome, "awsacademy") }

func path(name string) string { return filepath.Join(Dir(), name) }

// Session son las cookies de Canvas y Vocareum entre ejecuciones.
//
// Canvas emite una cookie persistente cuando se pide remember_me, así que una
// sesión guardada suele sobrevivir semanas y la contraseña casi nunca se usa.
type Session struct {
	// Cookies mapea host -> cookies de ese host.
	Cookies  map[string][]*http.Cookie `json:"cookies"`
	SavedAt  time.Time                 `json:"saved_at"`
	UserID   int64                     `json:"user_id,omitempty"`
	UserName string                    `json:"user_name,omitempty"`
}

// Discovery es dónde encontramos el laboratorio la última vez.
//
// Nada de esto se hardcodea: el curso cambia cada término y los endpoints de
// Vocareum pueden moverse, así que se re-descubre cuando falla.
type Discovery struct {
	CourseID     string `json:"course_id"`
	CourseName   string `json:"course_name,omitempty"`
	ModuleItemID string `json:"module_item_id"`
	ItemTitle    string `json:"item_title,omitempty"`
	// LaunchURL es el endpoint de Canvas que dispara el LTI launch.
	LaunchURL string `json:"launch_url,omitempty"`
	// VocareumBase es el origen de Vocareum al que nos llevó el launch.
	VocareumBase string `json:"vocareum_base,omitempty"`
	// Endpoints son las rutas de Vocareum ya confirmadas. Se guardan aquí para
	// poder corregirlas editando un fichero, sin recompilar el binario.
	Endpoints    Endpoints `json:"vocareum_endpoints,omitempty"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

// Endpoints son las rutas de Vocareum que usa el laboratorio. Duplica la forma
// de vocareum.Endpoints a propósito: state es la capa de abajo y no debe
// depender de los clientes que la usan.
type Endpoints struct {
	Status      string `json:"status,omitempty"`
	Start       string `json:"start,omitempty"`
	Stop        string `json:"stop,omitempty"`
	Credentials string `json:"credentials,omitempty"`
}

// Credentials son las credenciales STS que Vocareum expone para el laboratorio.
type Credentials struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`
	Region          string `json:"region,omitempty"`
	// Expiration la estima el llamador: Vocareum no la publica directamente,
	// se deriva del countdown de la sesión del laboratorio.
	Expiration time.Time `json:"expiration"`
	FetchedAt  time.Time `json:"fetched_at"`
}

// Expired indica si las credenciales ya no sirven. Un margen de un minuto
// evita entregar credenciales que van a morir en pleno request.
func (c *Credentials) Expired() bool {
	if c == nil || c.AccessKeyID == "" {
		return true
	}
	if c.Expiration.IsZero() {
		return false
	}
	return time.Now().Add(time.Minute).After(c.Expiration)
}

// ErrNotFound indica que el fichero de estado todavía no existe.
var ErrNotFound = errors.New("estado no encontrado")

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

// SaveSession usa 0600: las cookies de sesión son tan sensibles como la contraseña.
func (s *Session) Save() error { return save("session.json", s, 0o600) }

func LoadDiscovery() (*Discovery, error) {
	var d Discovery
	if err := load("discovery.json", &d); err != nil {
		return nil, err
	}
	return &d, nil
}

// Save usa 0644: son identificadores públicos, no secretos.
func (d *Discovery) Save() error { return save("discovery.json", d, 0o644) }

func LoadCredentials() (*Credentials, error) {
	var c Credentials
	if err := load("creds.json", &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Credentials) Save() error { return save("creds.json", c, 0o600) }

// ClearSession borra la sesión guardada; el próximo login parte de cero.
func ClearSession() error {
	err := os.Remove(path("session.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// ClearDiscovery fuerza un re-descubrimiento del curso y del laboratorio.
func ClearDiscovery() error {
	err := os.Remove(path("discovery.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
