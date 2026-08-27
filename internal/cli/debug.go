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
		Short:  "Diagnostic tools",
		Hidden: true,
	}
	cmd.AddCommand(newDebugLabCmd())
	return cmd
}

// rePath looks for endpoint paths in HTML and JavaScript.
var rePath = regexp.MustCompile(`["'\x60](/?[A-Za-z0-9_./-]*\.(?:php|json|cgi)(?:\?[^"'\x60]*)?)["'\x60]`)

func newDebugLabCmd() *cobra.Command {
	var (
		dumpHTML bool
		scripts  bool
		probe    bool
	)
	cmd := &cobra.Command{
		Use:   "lab",
		Short: "Dump the lab page and look for its endpoints",
		Long: `Goes through the LTI launch and examines the lab page.

It serves to discover Vocareum's real endpoints without needing a browser: they
are the ones its own JavaScript calls. With --scripts it also downloads the .js
files the page references, which is where they usually live.`,
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

			fmt.Fprintf(os.Stderr, "course:   %s\n", disc.CourseName)
			fmt.Fprintf(os.Stderr, "vocareum: %s\n", page.URL)
			fmt.Fprintf(os.Stderr, "size:     %d bytes\n\n", len(page.Body))

			if dumpHTML {
				fmt.Println(page.String())
				return nil
			}

			if probe {
				return probeEndpoints(cmd.Context(), app, lab)
			}

			found := map[string]string{}
			for _, m := range rePath.FindAllStringSubmatch(page.String(), -1) {
				found[m[1]] = "page"
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
				fmt.Fprintln(os.Stderr, "No endpoint path found.")
				fmt.Fprintln(os.Stderr, "Try --scripts, or capture the traffic with: go run ./cmd/vockit")
				return nil
			}

			paths := make([]string, 0, len(found))
			for p := range found {
				paths = append(paths, p)
			}
			sort.Strings(paths)

			fmt.Println("PATHS FOUND")
			for _, p := range paths {
				fmt.Printf("  %-55s  (%s)\n", p, found[p])
			}

			fmt.Println("\nCURRENTLY IN USE")
			ep := lab.Endpoints()
			fmt.Printf("  status       %s\n", ep.Status)
			fmt.Printf("  start        %s\n", ep.Start)
			fmt.Printf("  stop         %s\n", ep.Stop)
			fmt.Printf("  credentials  %s\n", ep.Credentials)
			fmt.Fprintf(os.Stderr,
				"\nConfirmed values go in 'vocareum_endpoints' of %s/discovery.json\n",
				stateDirHint())
			return nil
		},
	}
	cmd.Flags().BoolVar(&probe, "probe", false,
		"query the read-only endpoints and show their raw response")
	cmd.Flags().BoolVar(&dumpHTML, "html", false, "dump the raw HTML on stdout")
	cmd.Flags().BoolVar(&scripts, "scripts", false, "also search inside the .js files the page loads")
	return cmd
}

// probeEndpoints queries the endpoints that change nothing and shows what they
// answer, which is what is needed to get the parsing right.
func probeEndpoints(ctx context.Context, app *App, lab *vocareum.Lab) error {
	ep := lab.Endpoints()
	// Read-only: starting or stopping the lab from a diagnostic tool would be
	// an unpleasant surprise.
	for _, probe := range []struct{ name, url string }{
		{"status (a=getawsstatus)", ep.Status},
		{"credentials (a=getaws)", ep.Credentials},
	} {
		fmt.Printf("== %s ==\n%s\n", probe.name, probe.url)
		if probe.url == "" {
			fmt.Print("   (not detected)\n\n")
			continue
		}
		resp, err := app.http.Get(ctx, probe.url)
		if err != nil {
			fmt.Printf("   error: %v\n\n", err)
			continue
		}
		body := strings.TrimSpace(resp.String())
		if len(body) > 6000 {
			body = body[:6000] + "\n   …[truncated]"
		}
		fmt.Printf("   HTTP %d  %s\n   %s\n\n",
			resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	fmt.Println("The responses above are what parseStatus/ParseCredentials interpret.")
	return nil
}

var reScriptSrc = regexp.MustCompile(`<script[^>]+src=["']([^"']+)["']`)

// scriptSources returns the .js files the page loads, absolute and same-host.
func scriptSources(html string, base *url.URL) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range reScriptSrc.FindAllStringSubmatch(html, -1) {
		ref, err := url.Parse(m[1])
		if err != nil {
			continue
		}
		abs := base.ResolveReference(ref)
		// Only the service's own JS; third-party CDNs carry no lab endpoints.
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
