// Command vockit captura el tráfico real de AWS Academy para poder escribir el
// cliente HTTP contra evidencia en vez de contra suposiciones.
//
// No forma parte del binario que se instala: es la herramienta que se usa una
// vez al principio, y de nuevo si Canvas o Vocareum cambian. Por eso vive en su
// propio comando, y chromedp nunca entra en cmd/awsacademy.
//
// Uso:
//
//	go run ./cmd/vockit -out captura.json
//
// Abre un navegador, hacés el flujo a mano (login, abrir el laboratorio,
// Start Lab, AWS Details) y al cerrar con Ctrl+C queda el volcado de todos los
// XHR con su URL, método, cabeceras, cuerpo y respuesta.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// exchange es un request con su respuesta, tal como los vio el navegador.
type exchange struct {
	Time           time.Time         `json:"time"`
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	ResourceType   string            `json:"resource_type,omitempty"`
	RequestHeaders map[string]string `json:"request_headers,omitempty"`
	PostData       string            `json:"post_data,omitempty"`
	Status         int64             `json:"status,omitempty"`
	// postDataPending marca los cuerpos que CDP omitió por tamaño.
	postDataPending bool              `json:"-"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	ResponseBody    string            `json:"response_body,omitempty"`
}

func main() {
	var (
		startURL = flag.String("url", "https://awsacademy.instructure.com/login/canvas",
			"página inicial")
		out         = flag.String("out", "captura.json", "fichero de salida")
		filterHosts = flag.String("hosts", "vocareum.com,instructure.com",
			"lista separada por comas: solo se capturan estos hosts (vacío = todos)")
		browser = flag.String("browser", "", "ruta del navegador (autodetectada si se omite)")
		profile = flag.String("profile", "",
			"directorio de perfil del navegador; persistir uno evita re-loguearse en cada captura")
		maxBody = flag.Int("max-body", 256*1024, "bytes máximos de cuerpo a guardar por respuesta")
	)
	flag.Parse()

	if err := run(*startURL, *out, *filterHosts, *browser, *profile, *maxBody); err != nil {
		fmt.Fprintf(os.Stderr, "vockit: %v\n", err)
		os.Exit(1)
	}
}

func run(startURL, out, filterHosts, browserPath, profileDir string, maxBody int) error {
	if browserPath == "" {
		var err error
		if browserPath, err = findBrowser(); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "navegador: %s\n", browserPath)

	if profileDir == "" {
		var err error
		if profileDir, err = os.MkdirTemp("", "vockit-profile-*"); err != nil {
			return err
		}
		defer os.RemoveAll(profileDir)
	}

	hosts := splitHosts(filterHosts)
	rec := &recorder{maxBody: maxBody, hosts: hosts}

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.ExecPath(browserPath),
		chromedp.UserDataDir(profileDir),
		// Necesitamos ver la ventana: el flujo lo hace la persona, no el código.
		chromedp.Flag("headless", false),
		// Brave arranca con funciones propias que estorban en automatización.
		chromedp.Flag("disable-brave-update", true),
		chromedp.Flag("disable-brave-rewards-extension", true),
		chromedp.Flag("no-first-run", true),
		chromedp.Flag("no-default-browser-check", true),
	)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
	defer cancelAlloc()

	taskCtx, cancelTask := chromedp.NewContext(allocCtx)
	defer cancelTask()

	chromedp.ListenTarget(taskCtx, rec.handler(taskCtx))

	if err := chromedp.Run(taskCtx,
		network.Enable(),
		chromedp.Navigate(startURL),
	); err != nil {
		return fmt.Errorf("no se pudo arrancar el navegador: %w", err)
	}

	fmt.Fprintf(os.Stderr, `
Navegador abierto. Hacé el flujo completo a mano:

  1. Login en Canvas
  2. Entrar al curso y abrir el ítem del Learner Lab
  3. Start Lab, esperar a que ponga el semáforo en verde
  4. Abrir "AWS Details" y revelar el bloque de AWS CLI
  5. End Lab

Cuando termines, volvé acá y pulsá Ctrl+C para guardar la captura.

`)

	// Terminamos por Ctrl+C o porque se cerró el navegador; en ambos casos
	// hay que volcar lo capturado, que es el motivo de todo esto.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sigs:
		fmt.Fprintln(os.Stderr, "\ninterrumpido, guardando...")
	case <-taskCtx.Done():
		fmt.Fprintln(os.Stderr, "\nnavegador cerrado, guardando...")
	}

	return rec.dump(out)
}

// recorder acumula los intercambios que pasan el filtro de hosts.
type recorder struct {
	mu        sync.Mutex
	exchanges map[network.RequestID]*exchange
	order     []network.RequestID
	maxBody   int
	hosts     []string
}

func (r *recorder) handler(ctx context.Context) func(any) {
	return func(ev any) {
		switch e := ev.(type) {
		case *network.EventRequestWillBeSent:
			r.onRequest(e)
		case *network.EventResponseReceived:
			r.onResponse(ctx, e)
		}
	}
}

func (r *recorder) onRequest(e *network.EventRequestWillBeSent) {
	if !r.wanted(e.Request.URL) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.exchanges == nil {
		r.exchanges = make(map[network.RequestID]*exchange)
	}
	r.exchanges[e.RequestID] = &exchange{
		Time:           time.Now(),
		Method:         e.Request.Method,
		URL:            e.Request.URL,
		ResourceType:   e.Type.String(),
		RequestHeaders: headersOf(e.Request.Headers),
		PostData:       postDataOf(e.Request),
	}
	if ex := r.exchanges[e.RequestID]; ex.PostData == "" && e.Request.HasPostData {
		// CDP omite el cuerpo cuando es grande; se pide aparte más tarde.
		ex.postDataPending = true
	}
	r.order = append(r.order, e.RequestID)
	fmt.Fprintf(os.Stderr, "  %-6s %s\n", e.Request.Method, truncate(e.Request.URL, 110))
}

func (r *recorder) onResponse(ctx context.Context, e *network.EventResponseReceived) {
	r.mu.Lock()
	ex, ok := r.exchanges[e.RequestID]
	r.mu.Unlock()
	if !ok {
		return
	}

	ex.Status = e.Response.Status
	ex.ResponseHeaders = headersOf(e.Response.Headers)
	pending := ex.postDataPending

	// El cuerpo se pide aparte y solo está disponible un rato, así que se
	// recoge en cuanto llega el evento y en su propio contexto.
	go func() {
		bodyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var body []byte
		err := chromedp.Run(bodyCtx, chromedp.ActionFunc(func(c context.Context) error {
			var err error
			body, err = network.GetResponseBody(e.RequestID).Do(c)
			return err
		}))
		if pending {
			if pd, err := requestPostData(bodyCtx, e.RequestID); err == nil {
				r.mu.Lock()
				ex.PostData = pd
				r.mu.Unlock()
			}
		}
		if err != nil {
			return
		}
		if len(body) > r.maxBody {
			body = append(body[:r.maxBody], []byte("\n...[truncado]")...)
		}
		r.mu.Lock()
		ex.ResponseBody = string(body)
		r.mu.Unlock()
	}()
}

// wanted decide si la URL entra en la captura.
func (r *recorder) wanted(rawurl string) bool {
	if len(r.hosts) == 0 {
		return true
	}
	for _, h := range r.hosts {
		if strings.Contains(rawurl, h) {
			return true
		}
	}
	return false
}

func (r *recorder) dump(out string) error {
	// Damos un respiro a las descargas de cuerpo en vuelo antes de volcar.
	time.Sleep(time.Second)

	r.mu.Lock()
	defer r.mu.Unlock()

	list := make([]*exchange, 0, len(r.order))
	for _, id := range r.order {
		if ex, ok := r.exchanges[id]; ok {
			list = append(list, ex)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].Time.Before(list[j].Time) })

	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return err
	}
	// 0600: la captura lleva cookies de sesión y credenciales AWS en claro.
	if err := os.WriteFile(out, raw, 0o600); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "\n%d intercambios guardados en %s\n", len(list), out)
	fmt.Fprintln(os.Stderr, "CUIDADO: contiene cookies y credenciales en claro. No lo publiques.")
	return nil
}

// postDataOf reensambla el cuerpo del request. CDP lo entrega troceado y en
// base64, y lo omite del todo cuando es demasiado grande.
func postDataOf(req *network.Request) string {
	if len(req.PostDataEntries) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, entry := range req.PostDataEntries {
		decoded, err := base64.StdEncoding.DecodeString(entry.Bytes)
		if err != nil {
			// Si no era base64, lo guardamos tal cual antes que perderlo.
			sb.WriteString(entry.Bytes)
			continue
		}
		sb.Write(decoded)
	}
	return sb.String()
}

// requestPostData recupera un cuerpo que CDP no incluyó en el evento.
func requestPostData(ctx context.Context, id network.RequestID) (string, error) {
	var data []byte
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		var err error
		data, err = network.GetRequestPostData(id).Do(c)
		return err
	}))
	return string(data), err
}

func headersOf(h network.Headers) map[string]string {
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = fmt.Sprint(v)
	}
	return out
}

// findBrowser localiza un navegador basado en Chromium.
func findBrowser() (string, error) {
	candidates := []string{
		"google-chrome-stable", "google-chrome", "chromium", "chromium-browser",
		"brave", "brave-browser", "brave-origin",
	}
	for _, name := range candidates {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("no encontré un navegador basado en Chromium (probé: %s); "+
		"indicá uno con -browser", strings.Join(candidates, ", "))
}

func splitHosts(s string) []string {
	var out []string
	for _, h := range strings.Split(s, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
