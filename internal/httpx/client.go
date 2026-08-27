// Package httpx es un cliente HTTP que se comporta lo suficientemente parecido
// a un navegador como para atravesar Canvas y el lanzamiento LTI hacia Vocareum.
//
// Ninguno de los dos servicios necesita ejecutar JavaScript para lo que hacemos:
// tanto LTI 1.1 (un form firmado con OAuth1) como LTI 1.3 (el baile OIDC) se
// reducen, desde el lado del cliente, a "seguí los redirects y reenviá el form
// que la página trae auto-enviado". Navigate implementa exactamente eso, por lo
// que no hace falta saber de antemano qué versión de LTI usa el curso.
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

// UserAgent identifica a la herramienta con honestidad. No imitamos un
// navegador real: si el servicio quiere distinguir este tráfico, puede.
const UserAgent = "awsacademycli/0.1 (+https://github.com/goslynn/awsacademycli)"

// maxHops acota la cadena de redirects y auto-submits. El lanzamiento LTI usa
// media docena; más que esto es un bucle.
const maxHops = 15

// Client es un http.Client con cookie jar, reintentos y auto-submit de forms.
type Client struct {
	http *http.Client
	jar  *cookiejar.Jar

	// mu serializa los requests. No paralelizamos contra servicios de terceros:
	// una herramienta personal no gana nada con concurrencia y sí puede
	// hacerse notar de más.
	mu sync.Mutex

	// origins registra los orígenes visitados para poder persistir el jar,
	// ya que cookiejar solo entrega cookies cuando se le pregunta por una URL.
	originsMu sync.Mutex
	origins   map[string]struct{}

	// Debug, si no es nil, recibe una línea por request. Lo usa --debug-http.
	Debug func(format string, args ...any)
}

// New construye un cliente con jar vacío.
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
		// Cortamos los redirects automáticos para poder registrar cada salto
		// y contarlos contra maxHops junto con los auto-submits.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return c, nil
}

// Response es una respuesta con el cuerpo ya leído y la URL donde terminamos.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	// URL es la dirección final tras seguir redirects y auto-submits.
	URL *url.URL
}

// String devuelve el cuerpo como texto.
func (r *Response) String() string { return string(r.Body) }

// IsHTML indica si la respuesta trae HTML.
func (r *Response) IsHTML() bool {
	return strings.Contains(r.Header.Get("Content-Type"), "text/html")
}

// IsJSON indica si la respuesta trae JSON.
func (r *Response) IsJSON() bool {
	return strings.Contains(r.Header.Get("Content-Type"), "json")
}

// Get hace un GET siguiendo la cadena completa.
func (c *Client) Get(ctx context.Context, rawurl string) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawurl, nil)
	if err != nil {
		return nil, err
	}
	return c.Navigate(ctx, req)
}

// PostForm envía un formulario siguiendo la cadena completa.
func (c *Client) PostForm(ctx context.Context, rawurl string, values url.Values) (*Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawurl, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.Navigate(ctx, req)
}

// Navigate ejecuta el request y sigue la cadena de redirects y formularios
// auto-enviados hasta llegar a una página que de verdad es el destino.
func (c *Client) Navigate(ctx context.Context, req *http.Request) (*Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for hop := 0; hop < maxHops; hop++ {
		resp, err := c.do(ctx, req)
		if err != nil {
			return nil, err
		}

		// Redirect: seguimos manualmente para contarlo como salto.
		if loc := redirectTarget(resp); loc != nil {
			next, err := followRedirect(ctx, req, resp, loc)
			if err != nil {
				return nil, err
			}
			req = next
			continue
		}

		// ¿Es una página cuyo único propósito es reenviar un formulario?
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
	return nil, fmt.Errorf("demasiados saltos (%d) siguiendo la cadena de navegación", maxHops)
}

// do ejecuta un único request con reintentos ante fallos transitorios.
func (c *Client) do(ctx context.Context, req *http.Request) (*Response, error) {
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", UserAgent)
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8")
	}
	c.rememberOrigin(req.URL)

	var lastErr error
	// Backoff exponencial ante 429 y 5xx: si el servicio nos está pidiendo
	// que bajemos el ritmo, insistir de inmediato solo empeora las cosas.
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<uint(attempt-1)) * time.Second
			c.debugf("reintento %d tras %s (%v)", attempt, delay, lastErr)
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
			lastErr = fmt.Errorf("HTTP %d de %s", raw.StatusCode, req.URL.Host)
			continue
		}
		return &Response{
			StatusCode: raw.StatusCode,
			Header:     raw.Header,
			Body:       data,
			URL:        raw.Request.URL,
		}, nil
	}
	return nil, fmt.Errorf("request a %s falló tras 4 intentos: %w", req.URL, lastErr)
}

// rewind devuelve un cuerpo fresco para reintentar el mismo request.
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

// redirectTarget devuelve la URL de destino si la respuesta es un redirect.
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

// followRedirect construye el request siguiente respetando la semántica de
// cada código: 303 y (por convención universal) 301/302 sobre POST pasan a GET.
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
		// 307 y 308 preservan método y cuerpo.
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
