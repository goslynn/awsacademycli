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

// Form es un formulario HTML listo para reenviar.
type Form struct {
	Action string
	Method string
	Values url.Values
}

// request materializa el formulario como un http.Request.
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

// Submit envía un formulario y sigue la cadena que se abra a partir de él.
func (c *Client) Submit(ctx context.Context, form *Form) (*Response, error) {
	req, err := form.request(ctx)
	if err != nil {
		return nil, err
	}
	return c.Navigate(ctx, req)
}

// submitCall detecta las formas habituales de auto-enviar un formulario:
// document.forms[0].submit(), document.ltiLaunchForm.submit(),
// $('#form').submit(), getElementById('x').submit()...
var submitCall = regexp.MustCompile(`\.submit\s*\(\s*\)`)

// findAutoSubmitForm devuelve el formulario que la página se enviaría a sí
// misma mediante JavaScript, o nil si la página es un destino real.
//
// Esta es la única "ejecución de JavaScript" que necesitamos: las páginas
// intermedias de un lanzamiento LTI no tienen contenido, solo un formulario con
// campos ocultos firmados y un script que lo envía en cuanto carga.
func findAutoSubmitForm(resp *Response) (*Form, error) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(resp.Body))
	if err != nil {
		return nil, err
	}

	forms := doc.Find("form")
	if forms.Length() == 0 {
		return nil, nil
	}

	// Una página puente no tiene contenido: existe solo para reenviar el
	// formulario. Una aplicación de verdad sí lo tiene, y también puede llamar
	// a .submit() desde algún manejador que nunca se dispara al cargar.
	// Enviar ese formulario nos sacaría de la página que buscábamos, así que la
	// ausencia de contenido es la señal decisiva, más que el propio .submit().
	if visibleTextLen(doc) > maxBridgePageText {
		return nil, nil
	}

	// Y algo tiene que enviarlo solo: sin esa señal, un formulario es un
	// formulario que el usuario debería completar —el login, por ejemplo— y
	// reenviarlo vacío sería un error.
	if !hasSubmitTrigger(doc) {
		return nil, nil
	}

	// Con varios formularios elegimos el primero que tenga campos ocultos: es
	// el patrón de LTI, donde el payload firmado viaja en inputs hidden.
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

// maxBridgePageText es cuánto texto visible puede tener una página que solo
// sirve de puente. Las de un lanzamiento LTI muestran a lo sumo un "espere…".
const maxBridgePageText = 400

// visibleTextLen mide el texto que la página le muestra a una persona.
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

// parseForm extrae action, método y campos de un formulario.
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
		// Los checkboxes y radios sin marcar no se envían.
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

// FindForm localiza un formulario por selector CSS y lo devuelve con sus campos
// precargados. Sirve para completar formularios reales, como el login.
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

// Document parsea el cuerpo como HTML para consultarlo con selectores.
func (r *Response) Document() (*goquery.Document, error) {
	return goquery.NewDocumentFromReader(bytes.NewReader(r.Body))
}
