package vocareum

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/goslynn/awsacademycli/internal/httpx"
)

// Session es una sesión abierta contra el laboratorio.
type Session struct {
	http *httpx.Client
	// base es el origen de Vocareum al que nos llevó el lanzamiento.
	base string
	// page es la página del laboratorio tal como quedó tras el lanzamiento.
	page *httpx.Response
}

// Base devuelve el origen de Vocareum.
func (s *Session) Base() string { return s.base }

// Page devuelve la última página del laboratorio que vimos.
func (s *Session) Page() *httpx.Response { return s.page }

// Launch atraviesa el lanzamiento LTI desde Canvas hasta el laboratorio.
//
// Canvas no pone el formulario de lanzamiento en la página del ítem: la
// envuelve en un iframe que apunta a external_tools/retrieve, y es dentro de
// ese iframe donde está el form firmado. Por eso hay que seguir el iframe a
// mano; a partir de ahí httpx.Navigate resuelve la cadena, sea LTI 1.1 o 1.3.
func Launch(ctx context.Context, client *httpx.Client, launchURL string) (*Session, error) {
	origin, err := url.Parse(launchURL)
	if err != nil {
		return nil, err
	}
	lmsHost := origin.Host

	resp, err := client.Get(ctx, launchURL)
	if err != nil {
		return nil, fmt.Errorf("no se pudo abrir el ítem del laboratorio: %w", err)
	}

	// Un lanzamiento LTI exitoso siempre acaba fuera del LMS, en el host del
	// proveedor. Usamos eso como señal en vez de buscar el nombre "vocareum":
	// el dominio del proveedor puede cambiar, que salgamos del LMS no.
	if resp.URL.Host == lmsHost {
		if resp, err = leaveLMS(ctx, client, resp); err != nil {
			return nil, err
		}
	}

	if resp.URL.Host == lmsHost {
		return nil, fmt.Errorf(
			"el lanzamiento LTI no salió de %s; terminó en %s", lmsHost, resp.URL)
	}

	// La primera página del proveedor suele ser un trampolín, no el panel.
	if resp, err = followJSRedirects(ctx, client, resp); err != nil {
		return nil, err
	}

	return &Session{
		http: client,
		base: (&url.URL{Scheme: resp.URL.Scheme, Host: resp.URL.Host}).String(),
		page: resp,
	}, nil
}

// leaveLMS da el salto desde la página de Canvas hacia el proveedor.
//
// Canvas ofrece dos formas según la versión y la versión de LTI, y hay que
// intentarlas en orden: un formulario oculto que su JavaScript envía (LTI 1.3),
// o un iframe que carga la página de lanzamiento (LTI 1.1).
func leaveLMS(ctx context.Context, client *httpx.Client, page *httpx.Response) (*httpx.Response, error) {
	// El formulario de lanzamiento de Canvas se marca a sí mismo. Es una señal
	// mucho más fiable que buscar una llamada a submit(), porque Canvas lo
	// envía desde su bundle y no desde un script en la página.
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
			return nil, fmt.Errorf("el lanzamiento LTI falló al enviar %s: %w", selector, err)
		}
		return resp, nil
	}

	frameURL, err := findToolFrame(page)
	if err != nil {
		return nil, err
	}
	if frameURL == "" {
		return nil, fmt.Errorf(
			"la página del laboratorio no traía ni el formulario de lanzamiento ni un iframe "+
				"utilizable (terminamos en %s); ¿sigue la sesión de Canvas viva?", page.URL)
	}
	resp, err := client.Get(ctx, frameURL)
	if err != nil {
		return nil, fmt.Errorf("el lanzamiento LTI falló: %w", err)
	}
	return resp, nil
}

// findToolFrame localiza el iframe en el que Canvas embebe la herramienta.
func findToolFrame(resp *httpx.Response) (string, error) {
	doc, err := resp.Document()
	if err != nil {
		return "", err
	}
	// Canvas ha usado varios nombres para este iframe según la versión.
	selectors := []string{
		"iframe#tool_content",
		"iframe.tool_launch",
		"iframe[data-lti-launch]",
		"iframe[src*='external_tools']",
		"iframe[src*='vocareum']",
	}
	for _, sel := range selectors {
		src, ok := doc.Find(sel).First().Attr("src")
		// Canvas deja el iframe en about:blank y le pone la URL real por
		// JavaScript, así que ese valor no lleva a ninguna parte.
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

// Reload vuelve a pedir la página del laboratorio.
func (s *Session) Reload(ctx context.Context) error {
	resp, err := s.http.Get(ctx, s.page.URL.String())
	if err != nil {
		return err
	}
	s.page = resp
	return nil
}

// Patrones de redirección por JavaScript.
//
// Tras el lanzamiento LTI, Vocareum no sirve el panel directamente: devuelve un
// trampolín cuyo único contenido es un script que navega al panel real. No hace
// falta un motor de JavaScript para seguirlo, solo leer la URL.
var jsRedirects = []*regexp.Regexp{
	// El trampolín de Vocareum. El segundo argumento es un token para navegar
	// sin cookies; como nosotros sí las tenemos, se ignora.
	regexp.MustCompile(`callPostIfCookiesDisabled\(\s*["']([^"']+)["']`),
	// Redirecciones corrientes, por si el trampolín cambia de forma.
	regexp.MustCompile(`location\.(?:href|replace)\s*(?:=|\()\s*["']([^"']+)["']`),
	regexp.MustCompile(`(?i)<meta[^>]+http-equiv=["']refresh["'][^>]+content=["'][^"']*url=([^"'\s;]+)`),
}

// findJSRedirect devuelve la URL a la que la página se manda sola, o "".
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

// followJSRedirects sigue la cadena de trampolines hasta el panel real.
func followJSRedirects(ctx context.Context, client *httpx.Client, page *httpx.Response) (*httpx.Response, error) {
	// Un par de saltos cubre el trampolín; más que eso es un bucle.
	for hop := 0; hop < 3; hop++ {
		next := findJSRedirect(page)
		if next == "" || next == page.URL.String() {
			return page, nil
		}
		resp, err := client.Get(ctx, next)
		if err != nil {
			return nil, fmt.Errorf("no pude seguir al panel del laboratorio: %w", err)
		}
		page = resp
	}
	return page, nil
}
