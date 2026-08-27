package vocareum

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/goslynn/awsacademycli/internal/httpx"
)

// Endpoints son las URLs que operan el laboratorio.
//
// Vocareum expone una sola entrada, /util/vcput.php, y distingue la operación
// con el parámetro a=. Las URLs se guardan completas y absolutas porque llevan
// parámetros propios de la sesión —stepid, version, mode, y a veces un token
// encd— que no se pueden reconstruir desde fuera: hay que leerlas de la página
// del laboratorio.
type Endpoints struct {
	Status      string `json:"status,omitempty"`
	Start       string `json:"start,omitempty"`
	Stop        string `json:"stop,omitempty"`
	Credentials string `json:"credentials,omitempty"`
}

// Las acciones de vcput.php que nos interesan. Vocareum sirve varios
// proveedores de nube desde la misma página (startaws, startazure, startgcp);
// aquí solo valen las de AWS.
const (
	actionStart  = "startaws"
	actionStop   = "endaws"
	actionStatus = "getawsstatus"
	actionCreds  = "getaws"
)

// reVcput captura las llamadas a la API dentro del HTML y el JavaScript de la
// página, con todos sus parámetros.
var reVcput = regexp.MustCompile(`["'\x60]([^"'\x60\s]*util/vcput\.php\?a=([A-Za-z_]+)[^"'\x60\s]*)["'\x60]`)

// DetectEndpoints lee de la página del laboratorio las URLs que usan sus
// propios botones.
//
// Es la única forma fiable de obtenerlas: llevan identificadores de sesión, así
// que ni se pueden compilar como constantes ni sobreviven de un curso a otro.
func DetectEndpoints(page *httpx.Response) Endpoints {
	var found Endpoints

	for _, m := range reVcput.FindAllStringSubmatch(page.String(), -1) {
		raw, action := m[1], m[2]

		// Se guardan absolutas: las de la página son relativas a /main/ y
		// resolverlas más tarde, contra otra base, daría una URL equivocada.
		ref, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		abs := page.URL.ResolveReference(ref).String()

		switch action {
		case actionStart:
			setIfEmpty(&found.Start, abs)
		case actionStop:
			setIfEmpty(&found.Stop, abs)
		case actionStatus:
			setIfEmpty(&found.Status, abs)
		case actionCreds:
			setIfEmpty(&found.Credentials, abs)
		}
	}
	return found
}

// setIfEmpty conserva la primera aparición: la página repite algunas llamadas
// con parámetros parciales, y la primera suele ser la completa.
func setIfEmpty(dst *string, value string) {
	if *dst == "" {
		*dst = value
	}
}

// Complete indica si tenemos todo lo necesario para operar el laboratorio.
func (e Endpoints) Complete() bool {
	return e.Status != "" && e.Start != "" && e.Stop != "" && e.Credentials != ""
}

// Missing nombra lo que falta, para poder decirlo en un error.
func (e Endpoints) Missing() []string {
	var missing []string
	for _, f := range []struct {
		name, value string
	}{
		{"status", e.Status},
		{"start", e.Start},
		{"stop", e.Stop},
		{"credentials", e.Credentials},
	} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	return missing
}

// Merge superpone otros endpoints sobre estos; gana lo que traiga other.
func (e Endpoints) Merge(other Endpoints) Endpoints {
	setIfNotEmpty(&e.Status, other.Status)
	setIfNotEmpty(&e.Start, other.Start)
	setIfNotEmpty(&e.Stop, other.Stop)
	setIfNotEmpty(&e.Credentials, other.Credentials)
	return e
}

func setIfNotEmpty(dst *string, value string) {
	if value != "" {
		*dst = value
	}
}

// resolve convierte una ruta en URL absoluta contra el origen de Vocareum.
// Las detectadas ya vienen absolutas y pasan tal cual.
func (e Endpoints) resolve(base, path string) string {
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}
	ref, err := url.Parse(path)
	if err != nil {
		return base + path
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return base + path
	}
	return baseURL.ResolveReference(ref).String()
}

// StateDirHint nombra dónde se guardan los endpoints confirmados. Vive aquí
// para que los mensajes de diagnóstico apunten al sitio correcto.
func StateDirHint() string { return "~/.local/state/awsacademy" }
