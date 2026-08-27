package httpx

import (
	"net/http"
	"net/url"
	"sort"
)

// The standard jar only hands cookies over when asked about a specific URL, so
// in order to save it we have to remember where we have been.
func (c *Client) rememberOrigin(u *url.URL) {
	if u == nil {
		return
	}
	origin := (&url.URL{Scheme: u.Scheme, Host: u.Host}).String()
	c.originsMu.Lock()
	c.origins[origin] = struct{}{}
	c.originsMu.Unlock()
}

// ExportCookies returns the live cookies grouped by origin, ready to persist.
//
// Note: the standard jar does not expose attributes (Expires, Domain, Path),
// only name and value. On restore we set them again on the origin where we saw
// them, which is enough to revive a session.
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

// ImportCookies restores a previously exported jar.
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

// Cookie returns the value of a specific cookie on an origin, or "" if absent.
// Canvas delivers its CSRF token this way.
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
