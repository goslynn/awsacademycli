// Package vocareum controla el laboratorio detrás del lanzamiento LTI.
//
// Vocareum es una aplicación PHP con jQuery: sus botones llaman a endpoints
// planos bajo /util/*.php, así que se maneja con HTTP normal sin necesidad de
// un navegador. Su login propio sí tiene reCAPTCHA, pero nunca pasamos por ahí:
// entramos siempre por el lanzamiento LTI desde Canvas, que no lo atraviesa.
package vocareum

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/goslynn/awsacademycli/internal/state"
)

// El panel "AWS Details" muestra un bloque en formato INI listo para pegar en
// ~/.aws/credentials. Lo leemos de ahí en vez de intentar reconstruirlo.
var (
	reAccessKey    = regexp.MustCompile(`(?i)aws_access_key_id\s*=\s*([A-Z0-9]{16,})`)
	reSecretKey    = regexp.MustCompile(`(?i)aws_secret_access_key\s*=\s*([A-Za-z0-9/+=]{20,})`)
	reSessionToken = regexp.MustCompile(`(?i)aws_session_token\s*=\s*([A-Za-z0-9/+=]{50,})`)
)

// ParseCredentials extrae las credenciales STS del bloque que muestra Vocareum.
//
// Acepta el texto tal cual, con o sin cabecera de perfil y con cualquier
// espaciado alrededor del '=', porque el formato exacto varía entre laboratorios.
func ParseCredentials(text string) (*state.Credentials, error) {
	find := func(re *regexp.Regexp, what string) (string, error) {
		m := re.FindStringSubmatch(text)
		if m == nil {
			return "", fmt.Errorf("no encontré %s en el panel de AWS Details", what)
		}
		return strings.TrimSpace(m[1]), nil
	}

	accessKey, err := find(reAccessKey, "aws_access_key_id")
	if err != nil {
		return nil, err
	}
	secretKey, err := find(reSecretKey, "aws_secret_access_key")
	if err != nil {
		return nil, err
	}
	// El session token es lo que distingue a un laboratorio activo de unas
	// credenciales permanentes; sin él, algo salió mal en la lectura.
	sessionToken, err := find(reSessionToken, "aws_session_token")
	if err != nil {
		return nil, err
	}

	return &state.Credentials{
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
		SessionToken:    sessionToken,
		FetchedAt:       time.Now(),
	}, nil
}

// reLabStatus captura la respuesta de a=getawsstatus, que es texto plano:
// "Lab status: ready<br>".
var reLabStatus = regexp.MustCompile(`(?i)lab\s+status\s*:\s*([a-z ]+)`)

// ParseLabStatus interpreta la palabra de estado que devuelve Vocareum.
func ParseLabStatus(text string) (LabState, bool) {
	m := reLabStatus.FindStringSubmatch(text)
	if m == nil {
		return StateUnknown, false
	}
	word := strings.ToLower(strings.TrimSpace(m[1]))
	switch {
	case strings.HasPrefix(word, "ready"):
		return StateRunning, true
	case strings.HasPrefix(word, "start"), strings.HasPrefix(word, "provision"),
		strings.HasPrefix(word, "creating"), strings.HasPrefix(word, "pending"),
		strings.HasPrefix(word, "in progress"):
		return StateStarting, true
	case strings.HasPrefix(word, "stopping"), strings.HasPrefix(word, "terminating"),
		strings.HasPrefix(word, "ending"):
		return StateStopping, true
	case strings.HasPrefix(word, "stopped"), strings.HasPrefix(word, "not "),
		strings.HasPrefix(word, "off"), strings.HasPrefix(word, "ended"):
		return StateStopped, true
	}
	return StateUnknown, false
}

// reExpiry captura el instante exacto de expiración, que Vocareum publica como
// marca de tiempo Unix en un span oculto. Es preferible a deducirla del
// contador: es la que gobierna de verdad cuándo mueren las credenciales.
var reExpiry = regexp.MustCompile(`id=["']vlab-expiretime["'][^>]*>\s*(\d{9,})`)

// ParseExpiry devuelve el instante en que expira la sesión del laboratorio.
func ParseExpiry(text string) (time.Time, bool) {
	m := reExpiry.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}, false
	}
	secs, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(secs, 0), true
}

// reRemainingLabeled captura el contador con su etiqueta. Importa distinguirlo
// del "Accumulated lab time", que aparece en la misma página y mide otra cosa.
var reRemainingLabeled = regexp.MustCompile(`(?i)remaining session time\s*:\s*(\d{1,3}):([0-5]\d):([0-5]\d)`)

// reClock reconoce el contador de sesión: "3:59:30", "03:59", "3h 59m".
var (
	reClockHMS = regexp.MustCompile(`\b(\d{1,2}):([0-5]\d):([0-5]\d)\b`)
	reClockHM  = regexp.MustCompile(`\b(\d{1,2}):([0-5]\d)\b`)
	reClockTxt = regexp.MustCompile(`(?i)\b(\d{1,3})\s*h(?:ours?)?\b(?:\s*(\d{1,2})\s*m)?`)
)

// ParseRemaining interpreta el contador de sesión del laboratorio.
//
// Es el número que de verdad importa: dice cuánto falta para que las
// credenciales mueran y el trabajo sin guardar se pierda.
func ParseRemaining(text string) (time.Duration, bool) {
	// La forma etiquetada primero: en la página del laboratorio hay varios
	// relojes y solo uno cuenta lo que queda de sesión.
	if m := reRemainingLabeled.FindStringSubmatch(text); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		s, _ := strconv.Atoi(m[3])
		return time.Duration(h)*time.Hour + time.Duration(min)*time.Minute + time.Duration(s)*time.Second, true
	}
	if m := reClockHMS.FindStringSubmatch(text); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		s, _ := strconv.Atoi(m[3])
		return time.Duration(h)*time.Hour + time.Duration(min)*time.Minute + time.Duration(s)*time.Second, true
	}
	if m := reClockTxt.FindStringSubmatch(text); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		return time.Duration(h)*time.Hour + time.Duration(min)*time.Minute, true
	}
	if m := reClockHM.FindStringSubmatch(text); m != nil {
		h, _ := strconv.Atoi(m[1])
		min, _ := strconv.Atoi(m[2])
		return time.Duration(h)*time.Hour + time.Duration(min)*time.Minute, true
	}
	return 0, false
}

// reBudget reconoce el gasto acumulado: "$12.34 used of $100".
var reBudget = regexp.MustCompile(`\$\s*([0-9]+(?:\.[0-9]+)?)`)

// ParseBudget devuelve el gasto y el tope del laboratorio, en dólares.
//
// El laboratorio se corta al agotar el presupuesto, así que conviene verlo
// antes de que pase, no después.
func ParseBudget(text string) (used, total float64, ok bool) {
	m := reBudget.FindAllStringSubmatch(text, 2)
	if len(m) == 0 {
		return 0, 0, false
	}
	used, err := strconv.ParseFloat(m[0][1], 64)
	if err != nil {
		return 0, 0, false
	}
	if len(m) > 1 {
		total, _ = strconv.ParseFloat(m[1][1], 64)
	}
	return used, total, true
}
