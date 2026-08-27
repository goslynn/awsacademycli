package httpx

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Form is an HTML form ready to be resubmitted.
type Form struct {
	Action string
	Method string
	Values url.Values
}

// request materialises the form as an http.Request.
func (f *Form) request(ctx context.Context) (*http.Request, error) {
	if strings.EqualFold(f.Method, http.MethodGet) {
		target, err := url.Parse(f.Action)
		if err != nil {
			return nil, err
		}
		target.RawQuery = f.Values.Encode()
		return http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Action, strings.NewReader(f.Values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req, nil
}

// Submit sends a form and follows whatever chain opens up from it.
func (c *Client) Submit(ctx context.Context, form *Form) (*Response, error) {
	req, err := form.request(ctx)
	if err != nil {
		return nil, err
	}
	return c.Navigate(ctx, req)
}

// submitCall detects the usual ways of auto-submitting a form:
// document.forms[0].submit(), document.ltiLaunchForm.submit(),
// $('#form').submit(), getElementById('x').submit()...
var submitCall = regexp.MustCompile(`\.submit\s*\(\s*\)`)

// findAutoSubmitForm returns the form the page would submit to itself through
// JavaScript, or nil if the page is a real destination.
//
// This is the only "JavaScript execution" we need: the intermediate pages of an
// LTI launch have no content, only a form with signed hidden fields and a
// script that submits it as soon as it loads.
func findAutoSubmitForm(resp *Response) (*Form, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(resp.Body))
	if err != nil {
		return nil, err
	}

	forms := doc.Find("form")
	if forms.Length() == 0 {
		return nil, nil
	}

	// A bridge page has no content: it exists only to resubmit the form. A real
	// application does have content, and it may also call .submit() from some
	// handler that never fires on load. Submitting that form would take us away
	// from the page we were after, so the absence of content is the decisive
	// signal, more so than the .submit() itself.
	if visibleTextLen(doc) > maxBridgePageText {
		return nil, nil
	}

	// And something has to submit it on its own: without that signal, a form is
	// a form the user is meant to fill in — the login, for instance — and
	// resubmitting it empty would be a mistake.
	if !hasSubmitTrigger(doc) {
		return nil, nil
	}

	// With several forms we pick the first one that has hidden fields: that is
	// the LTI pattern, where the signed payload travels in hidden inputs.
	var chosen *goquery.Selection
	forms.EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if s.Find(`input[type="hidden"]`).Length() > 0 {
			chosen = s
			return false
		}
		return true
	})
	if chosen == nil {
		chosen = forms.First()
	}

	return parseForm(chosen, resp.URL)
}

// maxBridgePageText is how much visible text a page that merely serves as a
// bridge may have. The ones in an LTI launch show a "please wait…" at most.
const maxBridgePageText = 400

// visibleTextLen measures the text the page shows to a person.
func visibleTextLen(doc *goquery.Document) int {
	body := doc.Find("body").Clone()
	body.Find("script, style, noscript, template").Remove()
	return len(strings.Join(strings.Fields(body.Text()), " "))
}

func hasSubmitTrigger(doc *goquery.Document) bool {
	trigger := false
	doc.Find("script").EachWithBreak(func(_ int, s *goquery.Selection) bool {
		if submitCall.MatchString(s.Text()) {
			trigger = true
			return false
		}
		return true
	})
	if trigger {
		return true
	}
	if onload, ok := doc.Find("body").Attr("onload"); ok && submitCall.MatchString(onload) {
		return true
	}
	return false
}

// parseForm extracts the action, the method and the fields of a form.
func parseForm(form *goquery.Selection, base *url.URL) (*Form, error) {
	action, _ := form.Attr("action")
	target := base
	if action != "" {
		parsed, err := url.Parse(action)
		if err != nil {
			return nil, err
		}
		target = base.ResolveReference(parsed)
	}

	method, _ := form.Attr("method")
	if method == "" {
		method = http.MethodGet
	}

	values := url.Values{}
	form.Find("input, textarea, select").Each(func(_ int, s *goquery.Selection) {
		name, ok := s.Attr("name")
		if !ok || name == "" {
			return
		}
		// Unchecked checkboxes and radios are not submitted.
		if t, _ := s.Attr("type"); t == "checkbox" || t == "radio" {
			if _, checked := s.Attr("checked"); !checked {
				return
			}
		}
		if goquery.NodeName(s) == "select" {
			if opt := s.Find("option[selected]").First(); opt.Length() > 0 {
				v, _ := opt.Attr("value")
				values.Set(name, v)
			}
			return
		}
		v, _ := s.Attr("value")
		values.Set(name, v)
	})

	return &Form{Action: target.String(), Method: strings.ToUpper(method), Values: values}, nil
}

// FindForm locates a form by CSS selector and returns it with its fields
// preloaded. It serves to fill in real forms, such as the login.
func (r *Response) FindForm(selector string) (*Form, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(r.Body))
	if err != nil {
		return nil, err
	}
	sel := doc.Find(selector).First()
	if sel.Length() == 0 {
		return nil, nil
	}
	return parseForm(sel, r.URL)
}

// Document parses the body as HTML so it can be queried with selectors.
func (r *Response) Document() (*goquery.Document, error) {
	return goquery.NewDocumentFromReader(bytes.NewReader(r.Body))
}
