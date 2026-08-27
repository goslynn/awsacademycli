// Package canvas talks to the AWS Academy Canvas.
//
// Canvas serves its login with React, but underneath it is still classic Rails:
// a POST with the CSRF token that arrives in a cookie. And it exposes its REST
// API v1 to the browser session, so once logged in there is no need to scrape
// HTML to find the course or the lab.
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

// ErrInvalidCredentials means Canvas rejected the username or the password.
var ErrInvalidCredentials = errors.New("invalid AWS Academy credentials")

// ErrNoSession means there is no live session.
var ErrNoSession = errors.New("no Canvas session")

// Client talks to a Canvas instance.
type Client struct {
	http    *httpx.Client
	baseURL string
}

// New builds a client on top of an already-configured httpx.Client.
func New(h *httpx.Client, baseURL string) *Client {
	return &Client{http: h, baseURL: strings.TrimRight(baseURL, "/")}
}

// BaseURL returns the Canvas origin.
func (c *Client) BaseURL() string { return c.baseURL }

// User is the logged-in user.
type User struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	ShortName string `json:"short_name"`
	LoginID   string `json:"login_id"`
}

// Login authenticates against Canvas and leaves the session in the cookie jar.
//
// The flow is the Rails one: request the page to receive the cookie with the
// CSRF token and send that same token back in the POST. We ask for remember_me
// because the persistent cookie Canvas issues lasts weeks, so the password stops
// being needed in daily use.
func (c *Client) Login(ctx context.Context, email, password string) (*User, error) {
	loginURL := c.baseURL + "/login/canvas"

	if _, err := c.http.Get(ctx, loginURL); err != nil {
		return nil, fmt.Errorf("could not open the login page: %w", err)
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
		return nil, fmt.Errorf("the login POST failed: %w", err)
	}

	// Canvas does not distinguish through the status code: on failure it
	// returns the login page again. The only conclusive move is to ask who we
	// are, which is also what we want to know.
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

// csrfToken reads the token Canvas left in the _csrf_token cookie.
// It travels percent-encoded, so it has to be decoded before being sent back.
func (c *Client) csrfToken() (string, error) {
	raw := c.http.Cookie(c.baseURL, "_csrf_token")
	if raw == "" {
		return "", errors.New("Canvas did not hand over the _csrf_token cookie; did the login change?")
	}
	token, err := url.QueryUnescape(raw)
	if err != nil {
		return raw, nil
	}
	return token, nil
}

// loginErrorMessage rescues the reason Canvas shows when the login fails.
var flashError = regexp.MustCompile(`(?s)<div[^>]*class="[^"]*ic-flash-error[^"]*"[^>]*>(.*?)</div>`)

func loginErrorMessage(resp *httpx.Response) string {
	m := flashError.FindSubmatch(resp.Body)
	if m == nil {
		return ""
	}
	text := regexp.MustCompile(`<[^>]+>`).ReplaceAll(m[1], []byte(" "))
	return strings.Join(strings.Fields(string(text)), " ")
}

// Whoami returns the logged-in user, or ErrNoSession if the session died.
// It is the cheapest check there is: a GET that answers 401 unambiguously.
func (c *Client) Whoami(ctx context.Context) (*User, error) {
	var user User
	if err := c.getJSON(ctx, "/api/v1/users/self", &user); err != nil {
		return nil, err
	}
	return &user, nil
}

// getJSON performs a GET against the API and decodes the response.
func (c *Client) getJSON(ctx context.Context, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	// Canvas accepts the browser session for its API as long as we send the
	// CSRF token along, just like its own frontend does.
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
		return fmt.Errorf("GET %s returned HTTP %d", path, resp.StatusCode)
	}
	if err := json.Unmarshal(resp.Body, dst); err != nil {
		return fmt.Errorf("unexpected response from %s: %w", path, err)
	}
	return nil
}
