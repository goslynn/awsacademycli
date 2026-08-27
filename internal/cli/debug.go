package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/goslynn/awsacademycli/internal/vocareum"
	"github.com/spf13/cobra"
)

func newDebugCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "debug",
		Short:  "Herramientas de diagnóstico",
		Hidden: true,
	}
	cmd.AddCommand(newDebugLabCmd())
	return cmd
}

// rePath busca rutas de endpoints en HTML y JavaScript.
var rePath = regexp.MustCompile(`["'\x60](/?[A-Za-z0-9_./-]*\.(?:php|json|cgi)(?:\?[^"'\x60]*)?)["'\x60]`)

func newDebugLabCmd() *cobra.Command {
	var (
		dumpHTML bool
		scripts  bool
		probe    bool
	)
	cmd := &cobra.Command{
		Use:   "lab",
		Short: "Vuelca la página del laboratorio y busca sus endpoints",
		Long: `Atraviesa el lanzamiento LTI y examina la página del laboratorio.

Sirve para descubrir los endpoints reales de Vocareum sin necesidad de un
navegador: son los que llama su propio JavaScript. Con --scripts descarga
también los ficheros .js que la página referencia, que es donde suelen estar.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, err := newApp(flagDebugHTTP)
			if err != nil {
				return err
			}
			ctx := cmd.Context()

			lab, disc, err := app.OpenLab(ctx)
			if err != nil {
				return err
			}
			page := lab.Session().Page()

			fmt.Fprintf(os.Stderr, "curso:    %s\n", disc.CourseName)
			fmt.Fprintf(os.Stderr, "vocareum: %s\n", page.URL)
			fmt.Fprintf(os.Stderr, "tamaño:   %d bytes\n\n", len(page.Body))

			if dumpHTML {
				fmt.Println(page.String())
				return nil
			}

			if probe {
				return probeEndpoints(cmd.Context(), app, lab)
			}

			found := map[string]string{}
			for _, m := range rePath.FindAllStringSubmatch(page.String(), -1) {
				found[m[1]] = "página"
			}

			if scripts {
				for _, src := range scriptSources(page.String(), page.URL) {
					resp, err := app.http.Get(ctx, src)
					if err != nil || resp.StatusCode != 200 {
						continue
					}
					for _, m := range rePath.FindAllStringSubmatch(resp.String(), -1) {
						if _, seen := found[m[1]]; !seen {
							found[m[1]] = shortName(src)
						}
					}
				}
			}

			if len(found) == 0 {
				fmt.Fprintln(os.Stderr, "No encontré ninguna ruta de endpoint.")
				fmt.Fprintln(os.Stderr, "Probá con --scripts, o capturá el tráfico con: go run ./cmd/vockit")
				return nil
			}

			paths := make([]string, 0, len(found))
			for p := range found {
				paths = append(paths, p)
			}
			sort.Strings(paths)

			fmt.Println("RUTAS ENCONTRADAS")
			for _, p := range paths {
				fmt.Printf("  %-55s  (%s)\n", p, found[p])
			}

			fmt.Println("\nEN USO AHORA")
			ep := lab.Endpoints()
			fmt.Printf("  status       %s\n", ep.Status)
			fmt.Printf("  start        %s\n", ep.Start)
			fmt.Printf("  stop         %s\n", ep.Stop)
			fmt.Printf("  credentials  %s\n", ep.Credentials)
			fmt.Fprintf(os.Stderr,
				"\nLos valores confirmados van en 'vocareum_endpoints' de %s/discovery.json\n",
				stateDirHint())
			return nil
		},
	}
	cmd.Flags().BoolVar(&probe, "probe", false,
		"consultar los endpoints de solo lectura y mostrar su respuesta cruda")
	cmd.Flags().BoolVar(&dumpHTML, "html", false, "volcar el HTML crudo por stdout")
	cmd.Flags().BoolVar(&scripts, "scripts", false, "buscar también dentro de los .js que carga la página")
	return cmd
}

// probeEndpoints consulta los endpoints que no cambian nada y muestra lo que
// contestan, que es lo que hace falta para escribir bien el parseo.
func probeEndpoints(ctx context.Context, app *App, lab *vocareum.Lab) error {
	ep := lab.Endpoints()
	// Solo lectura: arrancar o detener el laboratorio desde una herramienta de
	// diagnóstico sería una sorpresa desagradable.
	for _, probe := range []struct{ name, url string }{
		{"status (a=getawsstatus)", ep.Status},
		{"credenciales (a=getaws)", ep.Credentials},
	} {
		fmt.Printf("== %s ==\n%s\n", probe.name, probe.url)
		if probe.url == "" {
			fmt.Print("   (no detectado)\n\n")
			continue
		}
		resp, err := app.http.Get(ctx, probe.url)
		if err != nil {
			fmt.Printf("   error: %v\n\n", err)
			continue
		}
		body := strings.TrimSpace(resp.String())
		if len(body) > 6000 {
			body = body[:6000] + "\n   …[truncado]"
		}
		fmt.Printf("   HTTP %d  %s\n   %s\n\n",
			resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	fmt.Println("Las respuestas de arriba son lo que interpreta parseStatus/ParseCredentials.")
	return nil
}

var reScriptSrc = regexp.MustCompile(`<script[^>]+src=["']([^"']+)["']`)

// scriptSources devuelve los .js que carga la página, en absoluto y del mismo host.
func scriptSources(html string, base *url.URL) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range reScriptSrc.FindAllStringSubmatch(html, -1) {
		ref, err := url.Parse(m[1])
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		// Solo el JS propio del servicio; los CDN de terceros no llevan
		// endpoints del laboratorio.
		if abs.Host != base.Host || seen[abs.String()] {
			continue
		}
		seen[abs.String()] = true
		out = append(out, abs.String())
	}
	return out
}

func shortName(rawurl string) string {
	if i := strings.LastIndex(rawurl, "/"); i >= 0 {
		return rawurl[i+1:]
	}
	return rawurl
}

func stateDirHint() string { return vocareum.StateDirHint() }
