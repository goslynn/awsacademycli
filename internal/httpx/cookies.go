package httpx

import (
	"net/http"
	"net/url"
	"sort"
)

// El jar estándar solo entrega cookies cuando se le pregunta por una URL
// concreta, así que para poder guardarlo hay que recordar dónde estuvimos.
func (c *Client) rememberOrigin(u *url.URL) {
	if u == nil {
		return
	}
	origin := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	c.originsMu.Lock()
	c.origins[origin] = struct{}{}
	c.originsMu.Unlock()
}

// ExportCookies devuelve las cookies vivas agrupadas por origen, listas para
// persistir.
//
// Nota: el jar estándar no expone atributos (Expires, Domain, Path), solo
// nombre y valor. Al restaurar volvemos a fijarlas en el origen donde las
// vimos, que es suficiente para revivir una sesión.
func (c *Client) ExportCookies() map[string][]*http.Cookie {
	c.originsMu.Lock()
	origins := make([]string, 0, len(c.origins))
	for o := range c.origins {
		origins = append(origins, o)
	}
	c.originsMu.Unlock()
	sort.Strings(origins)

	out := make(map[string][]*http.Cookie, len(origins))
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil {
			continue
		}
		if cookies := c.jar.Cookies(u); len(cookies) > 0 {
			out[origin] = cookies
		}
	}
	return out
}

// ImportCookies restaura un jar exportado previamente.
func (c *Client) ImportCookies(byOrigin map[string][]*http.Cookie) {
	for origin, cookies := range byOrigin {
		u, err := url.Parse(origin)
		if err != nil {
			continue
		}
		c.jar.SetCookies(u, cookies)
		c.rememberOrigin(u)
	}
}

// Cookie devuelve el valor de una cookie concreta en un origen, o "" si no está.
// Canvas entrega su token CSRF por esta vía.
func (c *Client) Cookie(rawurl, name string) string {
	u, err := url.Parse(rawurl)
	if err != nil {
		return ""
	}
	for _, ck := range c.jar.Cookies(u) {
		if ck.Name == name {
			return ck.Value
		}
	}
	return ""
}
