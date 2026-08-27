// Package canvas habla con el Canvas de AWS Academy.
//
// Canvas sirve su login con React, pero por debajo sigue siendo Rails clásico:
// un POST con el token CSRF que viene en una cookie. Y expone su API REST v1 a
// la sesión del navegador, así que una vez logueados no hace falta scrapear
// HTML para encontrar el curso ni el laboratorio.
package canvas

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/goslynn/awsacademycli/internal/httpx"
)

// ErrInvalidCredentials indica que Canvas rechazó usuario o contraseña.
var ErrInvalidCredentials = errors.New("credenciales de AWS Academy inválidas")

// ErrNoSession indica que no hay sesión viva.
var ErrNoSession = errors.New("sin sesión en Canvas")

// Client habla con una instancia de Canvas.
type Client struct {
	http    *httpx.Client
	baseURL string
}

// New construye un cliente sobre un httpx.Client ya configurado.
func New(h *httpx.Client, baseURL string) *Client {
	return &Client{http: h, baseURL: strings.TrimRight(baseURL, "/")}
}

// BaseURL devuelve el origen de Canvas.
func (c *Client) BaseURL() string { return c.baseURL }

// User es el usuario logueado.
type User struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
	LoginID   string `json:"login_id"`
}

// Login autentica contra Canvas y deja la sesión en el cookie jar.
//
// El flujo es el de Rails: se pide la página para recibir la cookie con el
// token CSRF y se devuelve ese mismo token en el POST. Pedimos remember_me
// porque la cookie persistente que emite Canvas dura semanas, y así la
// contraseña deja de hacer falta en el uso diario.
func (c *Client) Login(ctx context.Context, email, password string) (*User, error) {
	loginURL := c.baseURL + "/login/canvas"

	if _, err := c.http.Get(ctx, loginURL); err != nil {
		return nil, fmt.Errorf("no se pudo abrir el login: %w", err)
	}

	token, err := c.csrfToken()
	if err != nil {
		return nil, err
	}

	form := url.Values{
		"authenticity_token":             {token},
		"pseudonym_session[unique_id]":   {email},
		"pseudonym_session[password]":    {password},
		"pseudonym_session[remember_me]": {"1"},
	}
	resp, err := c.http.PostForm(ctx, loginURL, form)
	if err != nil {
		return nil, fmt.Errorf("el POST de login falló: %w", err)
	}

	// Canvas no distingue con el código de estado: ante un fallo devuelve la
	// página de login otra vez. Lo único concluyente es preguntar quiénes
	// somos, que además es lo que nos interesa saber.
	user, err := c.Whoami(ctx)
	if err != nil {
		if errors.Is(err, ErrNoSession) {
			if msg := loginErrorMessage(resp); msg != "" {
				return nil, fmt.Errorf("%w: %s", ErrInvalidCredentials, msg)
			}
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	return user, nil
}

// csrfToken lee el token que Canvas dejó en la cookie _csrf_token.
// Viaja percent-encoded, así que hay que decodificarlo antes de reenviarlo.
func (c *Client) csrfToken() (string, error) {
	raw := c.http.Cookie(c.baseURL, "_csrf_token")
	if raw == "" {
		return "", errors.New("Canvas no entregó la cookie _csrf_token; ¿cambió el login?")
	}
	token, err := url.QueryUnescape(raw)
	if err != nil {
		return raw, nil
	}
	return token, nil
}

// loginErrorMessage rescata el motivo que Canvas muestra al fallar el login.
var flashError = regexp.MustCompile(`(?s)<div[^>]*class="[^"]*ic-flash-error[^"]*"[^>]*>(.*?)</div>`)

func loginErrorMessage(resp *httpx.Response) string {
	m := flashError.FindSubmatch(resp.Body)
	if m == nil {
		return ""
	}
	text := regexp.MustCompile(`<[^>]+>`).ReplaceAll(m[1], []byte(" "))
	return strings.Join(strings.Fields(string(text)), " ")
}

// Whoami devuelve el usuario logueado, o ErrNoSession si la sesión murió.
// Es la comprobación más barata que existe: un GET que responde 401 sin ambigüedad.
func (c *Client) Whoami(ctx context.Context) (*User, error) {
	var user User
	if err := c.getJSON(ctx, "/api/v1/users/self", &user); err != nil {
		return nil, err
	}
	return &user, nil
}

// getJSON hace un GET a la API y decodifica la respuesta.
func (c *Client) getJSON(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	// Canvas acepta la sesión del navegador para su API si acompañamos el
	// token CSRF, igual que hace su propio frontend.
	if token, err := c.csrfToken(); err == nil {
		req.Header.Set("X-CSRF-Token", token)
	}

	resp, err := c.http.Navigate(ctx, req)
	if err != nil {
		return err
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrNoSession
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("GET %s devolvió HTTP %d", path, resp.StatusCode)
	}
	if err := json.Unmarshal(resp.Body, dst); err != nil {
		return fmt.Errorf("respuesta inesperada de %s: %w", path, err)
	}
	return nil
}
