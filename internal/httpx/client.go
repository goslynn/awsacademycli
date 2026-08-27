// Package httpx is an HTTP client that behaves enough like a browser to get
// through Canvas and the LTI launch into Vocareum.
//
// Neither service needs JavaScript executed for what we do: both LTI 1.1 (a
// form signed with OAuth1) and LTI 1.3 (the OIDC dance) reduce, from the client
// side, to "follow the redirects and resubmit the form the page carries
// self-submitted". Navigate implements exactly that, which is why there is no
// need to know in advance which LTI version the course uses.
package httpx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

// UserAgent identifies the tool honestly. We do not imitate a real browser: if
// the service wants to tell this traffic apart, it can.
const UserAgent = "awsacademycli/0.1 (+https://github.com/goslynn/awsacademycli)"

// maxHops bounds the chain of redirects and auto-submits. The LTI launch uses
// half a dozen; more than this is a loop.
const maxHops = 15

// Client is an http.Client with a cookie jar, retries and form auto-submit.
type Client struct {
	http *http.Client
	jar  *cookiejar.Jar

	// mu serialises the requests. We do not parallelise against third-party
	// services: a personal tool gains nothing from concurrency and can well
	// make itself more noticeable than it should.
	mu sync.Mutex

	// origins records the visited origins so the jar can be persisted, since
	// cookiejar only hands cookies over when asked about a URL.
	originsMu sync.Mutex
	origins   map[string]struct{}

	// Debug, when not nil, receives one line per request. --debug-http uses it.
	Debug func(format string, args ...any)
}

// New builds a client with an empty jar.
func New() (*Client, error) {
	jar, err := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	if err != nil {
		return nil, err
	}
	c := &Client{
		jar:     jar,
		origins: make(map[string]struct{}),
	}
	c.http = &http.Client{
		Jar:     jar,
		Timeout: 60 * time.Second,
		// We cut off the automatic redirects so we can record every hop and
		// count them against maxHops together with the auto-submits.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return c, nil
}

// Response is a response with the body already read and the URL we ended at.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	// URL is the final address after following redirects and auto-submits.
	URL *url.URL
}

// String returns the body as text.
func (r *Response) String() string { return string(r.Body) }

// IsHTML reports whether the response carries HTML.
func (r *Response) IsHTML() bool {
	return strings.Contains(r.Header.Get("Content-Type"), "text/html")
}

// IsJSON reports whether the response carries JSON.
func (r *Response) IsJSON() bool {
	return strings.Contains(r.Header.Get("Content-Type"), "json")
}

// Get performs a GET, following the complete chain.
func (c *Client) Get(ctx context.Context, rawurl string) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return nil, err
	}
	return c.Navigate(ctx, req)
}

// PostForm submits a form, following the complete chain.
func (c *Client) PostForm(ctx context.Context, rawurl string, values url.Values) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawurl, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.Navigate(ctx, req)
}

// Navigate runs the request and follows the chain of redirects and
// self-submitting forms until it reaches a page that really is the destination.
func (c *Client) Navigate(ctx context.Context, req *http.Request) (*Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for hop := 0; hop < maxHops; hop++ {
		resp, err := c.do(ctx, req)
		if err != nil {
			return nil, err
		}

		// Redirect: we follow it by hand so it counts as a hop.
		if loc := redirectTarget(resp); loc != nil {
			next, err := followRedirect(ctx, req, resp, loc)
			if err != nil {
				return nil, err
			}
			req = next
			continue
		}

		// Is this a page whose only purpose is to resubmit a form?
		if resp.IsHTML() {
			form, err := findAutoSubmitForm(resp)
			if err != nil {
				return nil, err
			}
			if form != nil {
				c.debugf("auto-submit %s %s", form.Method, form.Action)
				next, err := form.request(ctx)
				if err != nil {
					return nil, err
				}
				req = next
				continue
			}
		}

		return resp, nil
	}
	return nil, fmt.Errorf("too many hops (%d) following the navigation chain", maxHops)
}

// do runs a single request with retries on transient failures.
func (c *Client) do(ctx context.Context, req *http.Request) (*Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", UserAgent)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8")
	}
	c.rememberOrigin(req.URL)

	var lastErr error
	// Exponential backoff on 429 and 5xx: if the service is asking us to slow
	// down, insisting straight away only makes things worse.
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt-1)) * time.Second
			c.debugf("retry %d after %s (%v)", attempt, delay, lastErr)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}

		body, err := rewind(req)
		if err != nil {
			return nil, err
		}
		req.Body = body

		start := time.Now()
		raw, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := io.ReadAll(raw.Body)
		raw.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue
		}
		c.debugf("%s %s -> %d (%d bytes, %s)",
			req.Method, req.URL, raw.StatusCode, len(data), time.Since(start).Round(time.Millisecond))

		if raw.StatusCode == http.StatusTooManyRequests || raw.StatusCode >= 500 {
			lastErr = fmt.Errorf("HTTP %d from %s", raw.StatusCode, req.URL.Host)
			continue
		}
		return &Response{
			StatusCode: raw.StatusCode,
			Header:     raw.Header,
			Body:       data,
			URL:        raw.Request.URL,
		}, nil
	}
	return nil, fmt.Errorf("request to %s failed after 4 attempts: %w", req.URL, lastErr)
}

// rewind returns a fresh body so the same request can be retried.
func rewind(req *http.Request) (io.ReadCloser, error) {
	if req.GetBody == nil {
		return req.Body, nil
	}
	return req.GetBody()
}

func (c *Client) debugf(format string, args ...any) {
	if c.Debug != nil {
		c.Debug(format, args...)
	}
}

// redirectTarget returns the destination URL if the response is a redirect.
func redirectTarget(resp *Response) *url.URL {
	switch resp.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
	default:
		return nil
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return nil
	}
	target, err := url.Parse(loc)
	if err != nil {
		return nil
	}
	return resp.URL.ResolveReference(target)
}

// followRedirect builds the next request respecting each code's semantics: 303
// and (by universal convention) 301/302 over POST become GET.
func followRedirect(ctx context.Context, prev *http.Request, resp *Response, target *url.URL) (*http.Request, error) {
	method := prev.Method
	var body io.Reader

	switch resp.StatusCode {
	case http.StatusSeeOther:
		method = http.MethodGet
	case http.StatusMovedPermanently, http.StatusFound:
		if method == http.MethodPost {
			method = http.MethodGet
		}
	default:
		// 307 and 308 preserve method and body.
		if prev.GetBody != nil {
			b, err := prev.GetBody()
			if err != nil {
				return nil, err
			}
			body = b
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, err
	}
	if method == prev.Method && prev.Header.Get("Content-Type") != "" {
		req.Header.Set("Content-Type", prev.Header.Get("Content-Type"))
	}
	req.Header.Set("Referer", resp.URL.String())
	return req, nil
}
