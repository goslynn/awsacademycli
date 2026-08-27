package vocareum

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/goslynn/awsacademycli/internal/httpx"
)

// Session is an open session against the lab.
type Session struct {
	http *httpx.Client
	// base is the Vocareum origin the launch took us to.
	base string
	// page is the lab page as it stood after the launch.
	page *httpx.Response
}

// Base returns the Vocareum origin.
func (s *Session) Base() string { return s.base }

// Page returns the last lab page we saw.
func (s *Session) Page() *httpx.Response { return s.page }

// Launch goes through the LTI launch from Canvas all the way to the lab.
//
// Canvas does not put the launch form on the item page: it wraps it in an
// iframe pointing at external_tools/retrieve, and it is inside that iframe that
// the signed form lives. That is why the iframe has to be followed by hand;
// from there httpx.Navigate resolves the chain, be it LTI 1.1 or 1.3.
func Launch(ctx context.Context, client *httpx.Client, launchURL string) (*Session, error) {
	origin, err := url.Parse(launchURL)
	if err != nil {
		return nil, err
	}
	lmsHost := origin.Host

	resp, err := client.Get(ctx, launchURL)
	if err != nil {
		return nil, fmt.Errorf("could not open the lab item: %w", err)
	}

	// A successful LTI launch always ends up outside the LMS, on the provider's
	// host. We use that as the signal instead of looking for the name
	// "vocareum": the provider's domain may change, our leaving the LMS will not.
	if resp.URL.Host == lmsHost {
		if resp, err = leaveLMS(ctx, client, resp); err != nil {
			return nil, err
		}
	}

	if resp.URL.Host == lmsHost {
		return nil, fmt.Errorf(
			"the LTI launch did not leave %s; it ended at %s", lmsHost, resp.URL)
	}

	// The provider's first page is usually a bounce page, not the panel.
	if resp, err = followJSRedirects(ctx, client, resp); err != nil {
		return nil, err
	}

	return &Session{
		http: client,
		base: (&url.URL{Scheme: resp.URL.Scheme, Host: resp.URL.Host}).String(),
		page: resp,
	}, nil
}

// leaveLMS makes the jump from the Canvas page over to the provider.
//
// Canvas offers two ways depending on its version and on the LTI version, and
// they have to be tried in order: a hidden form its JavaScript submits (LTI
// 1.3), or an iframe that loads the launch page (LTI 1.1).
func leaveLMS(ctx context.Context, client *httpx.Client, page *httpx.Response) (*httpx.Response, error) {
	// The Canvas launch form marks itself. That is a far more reliable signal
	// than looking for a submit() call, because Canvas submits it from its
	// bundle and not from a script on the page.
	for _, selector := range []string{
		`form[data-message-type="tool_launch"]`,
		`form[id^="tool_form"]`,
	} {
		form, err := page.FindForm(selector)
		if err != nil {
			return nil, err
		}
		if form == nil {
			continue
		}
		resp, err := client.Submit(ctx, form)
		if err != nil {
			return nil, fmt.Errorf("the LTI launch failed while submitting %s: %w", selector, err)
		}
		return resp, nil
	}

	frameURL, err := findToolFrame(page)
	if err != nil {
		return nil, err
	}
	if frameURL == "" {
		return nil, fmt.Errorf(
			"the lab page carried neither the launch form nor a usable iframe "+
				"(we ended at %s); is the Canvas session still alive?", page.URL)
	}
	resp, err := client.Get(ctx, frameURL)
	if err != nil {
		return nil, fmt.Errorf("the LTI launch failed: %w", err)
	}
	return resp, nil
}

// findToolFrame locates the iframe in which Canvas embeds the tool.
func findToolFrame(resp *httpx.Response) (string, error) {
	doc, err := resp.Document()
	if err != nil {
		return "", err
	}
	// Canvas has used several names for this iframe across versions.
	selectors := []string{
		"iframe#tool_content",
		"iframe.tool_launch",
		"iframe[data-lti-launch]",
		"iframe[src*='external_tools']",
		"iframe[src*='vocareum']",
	}
	for _, sel := range selectors {
		src, ok := doc.Find(sel).First().Attr("src")
		// Canvas leaves the iframe at about:blank and sets the real URL with
		// JavaScript, so that value leads nowhere.
		if ok && src != "" && src != "about:blank" {
			ref, err := url.Parse(src)
			if err != nil {
				continue
			}
			return resp.URL.ResolveReference(ref).String(), nil
		}
	}
	return "", nil
}

// Reload requests the lab page again.
func (s *Session) Reload(ctx context.Context) error {
	resp, err := s.http.Get(ctx, s.page.URL.String())
	if err != nil {
		return err
	}
	s.page = resp
	return nil
}

// JavaScript redirection patterns.
//
// After the LTI launch, Vocareum does not serve the panel directly: it returns
// a bounce page whose only content is a script that navigates to the real
// panel. Following it needs no JavaScript engine, only reading the URL.
var jsRedirects = []*regexp.Regexp{
	// Vocareum's bounce page. The second argument is a token for navigating
	// without cookies; since we do have them, it is ignored.
	regexp.MustCompile(`callPostIfCookiesDisabled\(\s*["']([^"']+)["']`),
	// Ordinary redirections, in case the bounce page changes shape.
	regexp.MustCompile(`location\.(?:href|replace)\s*(?:=|\()\s*["']([^"']+)["']`),
	regexp.MustCompile(`(?i)<meta[^>]+http-equiv=["']refresh["'][^>]+content=["'][^"']*url=([^"'\s;]+)`),
}

// findJSRedirect returns the URL the page sends itself to, or "".
func findJSRedirect(page *httpx.Response) string {
	body := page.String()
	for _, re := range jsRedirects {
		if m := re.FindStringSubmatch(body); m != nil {
			ref, err := url.Parse(strings.TrimSpace(m[1]))
			if err != nil {
				continue
			}
			return page.URL.ResolveReference(ref).String()
		}
	}
	return ""
}

// followJSRedirects follows the chain of bounce pages to the real panel.
func followJSRedirects(ctx context.Context, client *httpx.Client, page *httpx.Response) (*httpx.Response, error) {
	// A couple of hops covers the bounce page; more than that is a loop.
	for hop := 0; hop < 3; hop++ {
		next := findJSRedirect(page)
		if next == "" || next == page.URL.String() {
			return page, nil
		}
		resp, err := client.Get(ctx, next)
		if err != nil {
			return nil, fmt.Errorf("could not follow through to the lab panel: %w", err)
		}
		page = resp
	}
	return page, nil
}
