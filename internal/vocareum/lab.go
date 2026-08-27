package vocareum

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/goslynn/awsacademycli/internal/state"
)

// LabState es el estado del laboratorio.
type LabState string

const (
	StateStopped  LabState = "detenido"
	StateStarting LabState = "arrancando"
	StateRunning  LabState = "corriendo"
	StateStopping LabState = "deteniéndose"
	StateUnknown  LabState = "desconocido"
)

// Status describe el laboratorio en un momento dado.
type Status struct {
	State LabState
	// Remaining es lo que queda de sesión. Cero si no se pudo leer.
	Remaining time.Duration
	// BudgetUsed y BudgetTotal son el gasto en dólares.
	BudgetUsed  float64
	BudgetTotal float64
}

// Running indica si el laboratorio está listo para usarse.
func (s Status) Running() bool { return s.State == StateRunning }

// Lab controla el laboratorio de una sesión.
type Lab struct {
	session   *Session
	endpoints Endpoints

	// PollInterval separa las consultas mientras se espera el arranque.
	// Arrancar tarda minutos: preguntar más seguido no lo acelera.
	PollInterval time.Duration
}

const defaultPollInterval = 5 * time.Second

// NewLab construye el controlador sobre una sesión ya lanzada.
func NewLab(s *Session, ep Endpoints) *Lab {
	return &Lab{session: s, endpoints: ep, PollInterval: defaultPollInterval}
}

// ErrEndpointsUnknown indica que no reconocimos la API en la página del
// laboratorio, así que no hay nada contra lo que operar.
var ErrEndpointsUnknown = errors.New("no reconocí la API del laboratorio en la página de Vocareum")

// check verifica que tenemos la URL necesaria antes de intentar usarla.
func (l *Lab) check(endpoint, what string) error {
	if endpoint == "" {
		return fmt.Errorf("%w: falta el endpoint de %s (sin resolver: %v). "+
			"Ejecutá 'awsacademy debug lab' para ver qué expone la página",
			ErrEndpointsUnknown, what, l.endpoints.Missing())
	}
	return nil
}

// Session devuelve la sesión sobre la que opera el laboratorio.
func (l *Lab) Session() *Session { return l.session }

// Endpoints devuelve los endpoints en uso, para poder cachearlos.
func (l *Lab) Endpoints() Endpoints { return l.endpoints }

// Status consulta el estado actual del laboratorio.
func (l *Lab) Status(ctx context.Context) (*Status, error) {
	body, err := l.poll(ctx)
	if err != nil {
		return nil, err
	}
	return parseStatus(body), nil
}

// Start pide a Vocareum que levante el laboratorio.
//
// Es idempotente: si ya está corriendo, Vocareum extiende la sesión en vez de
// reiniciarla, que es justo lo que queremos cuando se llama dos veces.
func (l *Lab) Start(ctx context.Context) error {
	return l.command(ctx, l.endpoints.Start, "arrancar")
}

// Stop pide a Vocareum que detenga el laboratorio.
func (l *Lab) Stop(ctx context.Context) error {
	return l.command(ctx, l.endpoints.Stop, "detener")
}

// WaitForRunning espera a que el laboratorio termine de arrancar.
func (l *Lab) WaitForRunning(ctx context.Context, timeout time.Duration, onTick func(Status)) (*Status, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	interval := l.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// El último estado visto se guarda aparte porque el plazo puede vencer en
	// mitad de una consulta: entonces el error que sube es un
	// "context deadline exceeded" que no le dice nada a nadie, cuando lo que
	// hace falta saber es en qué se quedó atascado el laboratorio.
	last := StateUnknown

	for {
		st, err := l.Status(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil, timeoutError(timeout, last)
			}
			return nil, err
		}
		last = st.State

		if onTick != nil {
			onTick(*st)
		}
		if st.Running() {
			return st, nil
		}
		select {
		case <-ctx.Done():
			return nil, timeoutError(timeout, last)
		case <-ticker.C:
		}
	}
}

func timeoutError(timeout time.Duration, last LabState) error {
	return fmt.Errorf(
		"el laboratorio no llegó a estar listo en %s (último estado: %s)", timeout, last)
}

// Credentials lee las credenciales AWS del panel de detalles.
func (l *Lab) Credentials(ctx context.Context) (*state.Credentials, error) {
	if err := l.check(l.endpoints.Credentials, "credenciales"); err != nil {
		return nil, err
	}
	resp, err := l.session.http.Get(ctx, l.endpoints.resolve(l.session.base, l.endpoints.Credentials))
	if err != nil {
		return nil, err
	}
	body := resp.String()
	creds, err := ParseCredentials(body)
	if err != nil {
		return nil, err
	}
	// La misma respuesta trae cuándo expira la sesión, así que no hace falta
	// una consulta extra. El instante exacto es preferible al contador.
	if exp, ok := ParseExpiry(body); ok {
		creds.Expiration = exp
	} else if remaining, ok := ParseRemaining(body); ok {
		creds.Expiration = time.Now().Add(remaining)
	}
	return creds, nil
}

// Details consulta de una sola vez las credenciales y el estado de la sesión.
//
// Vocareum los sirve juntos en la respuesta de a=getaws, así que pedirlos por
// separado sería una llamada de más.
func (l *Lab) Details(ctx context.Context) (*Status, *state.Credentials, error) {
	if err := l.check(l.endpoints.Credentials, "credenciales"); err != nil {
		return nil, nil, err
	}
	resp, err := l.session.http.Get(ctx, l.endpoints.resolve(l.session.base, l.endpoints.Credentials))
	if err != nil {
		return nil, nil, err
	}
	body := resp.String()

	creds, err := ParseCredentials(body)
	if err != nil {
		return nil, nil, err
	}
	if exp, ok := ParseExpiry(body); ok {
		creds.Expiration = exp
	}

	st := parseStatus(body)
	if st.Remaining == 0 && !creds.Expiration.IsZero() {
		st.Remaining = time.Until(creds.Expiration)
	}
	// Si hay credenciales servidas, el laboratorio está en marcha.
	if st.State == StateUnknown {
		st.State = StateRunning
	}
	return st, creds, nil
}

func (l *Lab) poll(ctx context.Context) (string, error) {
	if err := l.check(l.endpoints.Status, "estado"); err != nil {
		return "", err
	}
	resp, err := l.session.http.Get(ctx, l.endpoints.resolve(l.session.base, l.endpoints.Status))
	if err != nil {
		return "", fmt.Errorf("no pude consultar el estado del laboratorio: %w", err)
	}
	return resp.String(), nil
}

func (l *Lab) command(ctx context.Context, endpoint, verb string) error {
	if err := l.check(endpoint, verb); err != nil {
		return err
	}
	resp, err := l.session.http.Get(ctx, l.endpoints.resolve(l.session.base, endpoint))
	if err != nil {
		return fmt.Errorf("no pude %s el laboratorio: %w", verb, err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Vocareum devolvió HTTP %d al %s el laboratorio", resp.StatusCode, verb)
	}
	return nil
}

// parseStatus interpreta la respuesta de estado.
//
// Vocareum contesta en texto plano ("Lab status: ready"), así que se lee esa
// etiqueta en vez de rastrear palabras sueltas por todo el cuerpo: en una
// página de cientos de kilobytes, buscar "red" o "ready" al azar produce
// coincidencias dentro de otras palabras.
func parseStatus(body string) *Status {
	st := &Status{State: StateUnknown}

	if state, ok := ParseLabStatus(body); ok {
		st.State = state
	}
	if remaining, ok := ParseRemaining(body); ok {
		st.Remaining = remaining
		// Un contador corriendo prueba que hay sesión, aunque la etiqueta de
		// estado no viniera en esta respuesta.
		if st.State == StateUnknown && remaining > 0 {
			st.State = StateRunning
		}
	}
	if used, total, ok := ParseBudget(body); ok {
		st.BudgetUsed, st.BudgetTotal = used, total
	}
	return st
}
