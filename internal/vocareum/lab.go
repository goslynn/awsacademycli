package vocareum

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/goslynn/awsacademycli/internal/state"
)

// LabState is the state of the lab.
type LabState string

const (
	StateStopped  LabState = "stopped"
	StateStarting LabState = "starting"
	StateRunning  LabState = "running"
	StateStopping LabState = "stopping"
	StateUnknown  LabState = "unknown"
)

// Status describes the lab at a given moment.
type Status struct {
	State LabState
	// Remaining is what is left of the session. Zero if it could not be read.
	Remaining time.Duration
	// BudgetUsed and BudgetTotal are the spend in dollars.
	BudgetUsed  float64
	BudgetTotal float64
}

// Running reports whether the lab is ready to be used.
func (s Status) Running() bool { return s.State == StateRunning }

// Budget is what the lab has spent against what it is allowed to spend.
//
// It is the limit that does not reset: the session countdown comes back
// tomorrow, the budget does not, and the lab is cut off when it runs out.
type Budget struct {
	Used  float64
	Total float64
	// Monthly says the figures are a monthly allowance rather than the lab's
	// total, which changes what "left" means.
	Monthly bool
}

// Lab controls the lab of a session.
type Lab struct {
	session   *Session
	endpoints Endpoints

	// PollInterval spaces out the queries while waiting for the start.
	// Starting takes minutes: asking more often does not speed it up.
	PollInterval time.Duration
}

const defaultPollInterval = 5 * time.Second

// NewLab builds the controller on top of an already-launched session.
func NewLab(s *Session, ep Endpoints) *Lab {
	return &Lab{session: s, endpoints: ep, PollInterval: defaultPollInterval}
}

// ErrEndpointsUnknown means we did not recognise the API on the lab page, so
// there is nothing to operate against.
var ErrEndpointsUnknown = errors.New("could not recognise the lab API on the Vocareum page")

// check verifies that we have the URL we need before trying to use it.
func (l *Lab) check(endpoint, what string) error {
	if endpoint == "" {
		return fmt.Errorf("%w: the %s endpoint is missing (unresolved: %v). "+
			"Run 'awsacademy debug lab' to see what the page exposes",
			ErrEndpointsUnknown, what, l.endpoints.Missing())
	}
	return nil
}

// Session returns the session the lab operates on.
func (l *Lab) Session() *Session { return l.session }

// Endpoints returns the endpoints in use, so they can be cached.
func (l *Lab) Endpoints() Endpoints { return l.endpoints }

// Status queries the current state of the lab.
func (l *Lab) Status(ctx context.Context) (*Status, error) {
	body, err := l.poll(ctx)
	if err != nil {
		return nil, err
	}
	return parseStatus(body), nil
}

// Start asks Vocareum to bring the lab up.
//
// It is idempotent: if it is already running, Vocareum extends the session
// instead of restarting it, which is exactly what we want when it is called
// twice.
func (l *Lab) Start(ctx context.Context) error {
	return l.command(ctx, l.endpoints.Start, "start")
}

// Stop asks Vocareum to bring the lab down.
func (l *Lab) Stop(ctx context.Context) error {
	return l.command(ctx, l.endpoints.Stop, "stop")
}

// WaitForRunning waits for the lab to finish starting.
func (l *Lab) WaitForRunning(ctx context.Context, timeout time.Duration, onTick func(Status)) (*Status, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	interval := l.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// The last state seen is kept aside because the deadline may expire in the
	// middle of a query: the error that surfaces then is a "context deadline
	// exceeded" that tells nobody anything, when what is needed is to know
	// where the lab got stuck.
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
		"the lab did not become ready within %s (last state: %s)", timeout, last)
}

// Credentials reads the AWS credentials from the details panel.
func (l *Lab) Credentials(ctx context.Context) (*state.Credentials, error) {
	if err := l.check(l.endpoints.Credentials, "credentials"); err != nil {
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
	// The same response carries when the session expires, so no extra query is
	// needed. The exact instant is preferable to the countdown.
	if exp, ok := ParseExpiry(body); ok {
		creds.Expiration = exp
	} else if remaining, ok := ParseRemaining(body); ok {
		creds.Expiration = time.Now().Add(remaining)
	}
	return creds, nil
}

// Details queries the credentials and the session state in one go.
//
// Vocareum serves them together in the a=getaws response, so asking for them
// separately would be one call too many.
func (l *Lab) Details(ctx context.Context) (*Status, *state.Credentials, error) {
	if err := l.check(l.endpoints.Credentials, "credentials"); err != nil {
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
	// If credentials are being served, the lab is up and running.
	if st.State == StateUnknown {
		st.State = StateRunning
	}
	return st, creds, nil
}

// Budget queries what the lab has spent.
//
// It is a separate request because Vocareum serves it separately: the same
// a=getaws action answers with JSON instead of the credentials panel when
// asked with v=3.
func (l *Lab) Budget(ctx context.Context) (*Budget, error) {
	endpoint := l.BudgetURL()
	if err := l.check(endpoint, "budget"); err != nil {
		return nil, err
	}
	resp, err := l.session.http.Get(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("could not query the lab budget: %w", err)
	}
	return ParseBudgetJSON(resp.String())
}

// BudgetURL resolves where to ask for the spend.
//
// It is usually derived rather than detected: the page builds that URL by
// concatenation ("…&v=" + v), so the value that selects the budget never
// appears as a literal for DetectEndpoints to find. Deriving it from the
// credentials endpoint, which is the same URL with a different v=, is both
// cheaper and steadier than failing over one parameter.
func (l *Lab) BudgetURL() string {
	if l.endpoints.Budget != "" {
		return l.endpoints.resolve(l.session.base, l.endpoints.Budget)
	}
	if l.endpoints.Credentials == "" {
		return ""
	}
	u, err := url.Parse(l.endpoints.resolve(l.session.base, l.endpoints.Credentials))
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("v", budgetVersion)
	u.RawQuery = q.Encode()
	return u.String()
}

func (l *Lab) poll(ctx context.Context) (string, error) {
	if err := l.check(l.endpoints.Status, "status"); err != nil {
		return "", err
	}
	resp, err := l.session.http.Get(ctx, l.endpoints.resolve(l.session.base, l.endpoints.Status))
	if err != nil {
		return "", fmt.Errorf("could not query the lab status: %w", err)
	}
	return resp.String(), nil
}

func (l *Lab) command(ctx context.Context, endpoint, verb string) error {
	if err := l.check(endpoint, verb); err != nil {
		return err
	}
	resp, err := l.session.http.Get(ctx, l.endpoints.resolve(l.session.base, endpoint))
	if err != nil {
		return fmt.Errorf("could not %s the lab: %w", verb, err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("Vocareum returned HTTP %d while trying to %s the lab", resp.StatusCode, verb)
	}
	return nil
}

// parseStatus interprets the status response.
//
// Vocareum answers in plain text ("Lab status: ready"), so that label is read
// instead of hunting for loose words across the body: in a page of hundreds of
// kilobytes, searching for "red" or "ready" at random produces matches inside
// other words.
func parseStatus(body string) *Status {
	st := &Status{State: StateUnknown}

	if state, ok := ParseLabStatus(body); ok {
		st.State = state
	}
	if remaining, ok := ParseRemaining(body); ok {
		st.Remaining = remaining
		// A running countdown proves there is a session, even if the status
		// label did not come in this response.
		if st.State == StateUnknown && remaining > 0 {
			st.State = StateRunning
		}
	}
	// The spend is deliberately not looked for here: it does not travel in
	// these responses at all, and hunting for dollar signs in them only ever
	// found amounts that meant something else. It has its own endpoint.
	return st
}
