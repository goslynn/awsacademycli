package vocareum

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/goslynn/awsacademycli/internal/httpx"
)

func TestParseStatus(t *testing.T) {
	// Vocareum contesta en texto plano, no en JSON.
	tests := []struct {
		name  string
		body  string
		want  LabState
		clock time.Duration
	}{
		{"listo", "Lab status: ready<br>", StateRunning, 0},
		{"arrancando", "Lab status: starting<br>", StateStarting, 0},
		{"detenido", "Lab status: stopped<br>", StateStopped, 0},
		{"no iniciado", "Lab status: not started<br>", StateStopped, 0},
		{"deteniéndose", "Lab status: terminating<br>", StateStopping, 0},
		{"ilegible", "<html><body>algo salió mal</body></html>", StateUnknown, 0},
		{
			// Solo cuenta el contador etiquetado: en la página del laboratorio
			// conviven varios relojes y los demás miden otra cosa.
			name:  "contador etiquetado, no el acumulado",
			body:  "Remaining session time: 03:53:27(234 minutes)<br>Accumulated lab time: 04:42:00 (282 minutes)",
			want:  StateRunning,
			clock: 3*time.Hour + 53*time.Minute + 27*time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseStatus(tt.body)
			if got.State != tt.want {
				t.Errorf("State = %q, esperaba %q", got.State, tt.want)
			}
			if got.Remaining != tt.clock {
				t.Errorf("Remaining = %v, esperaba %v", got.Remaining, tt.clock)
			}
		})
	}
}

func TestParseStatusIgnoresSubstrings(t *testing.T) {
	// Una página de cientos de kilobytes contiene "red" dentro de "required" y
	// "ready" dentro de "already". Buscar palabras sueltas por el cuerpo daba
	// falsos positivos; solo vale la etiqueta.
	body := `<input required class="hidden"> ya está already configurado, green button`
	if got := parseStatus(body); got.State != StateUnknown {
		t.Errorf("State = %q, esperaba desconocido", got.State)
	}
}

func TestParseExpiry(t *testing.T) {
	body := `<span id="vlab-expiretime" class="hidden-1">1787803239</span>&nbsp;Remaining session time: 03:53:27`
	exp, ok := ParseExpiry(body)
	if !ok {
		t.Fatal("esperaba encontrar la marca de expiración")
	}
	if exp.Unix() != 1787803239 {
		t.Errorf("Expiry = %v (%d)", exp, exp.Unix())
	}
}

// fakeLab simula un laboratorio que tarda unas cuantas consultas en arrancar.
type fakeLab struct {
	polls   int
	readyAt int
	started bool
	stopped bool
}

func (f *fakeLab) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/util/vcput.php_start", func(w http.ResponseWriter, r *http.Request) {
		f.started = true
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("/util/vcput.php_stop", func(w http.ResponseWriter, r *http.Request) {
		f.stopped = true
		w.Write([]byte("OK"))
	})
	mux.HandleFunc("/util/vcput.php_status", func(w http.ResponseWriter, r *http.Request) {
		f.polls++
		if !f.started {
			w.Write([]byte("Lab status: stopped<br>"))
			return
		}
		if f.polls < f.readyAt {
			w.Write([]byte("Lab status: starting<br>"))
			return
		}
		w.Write([]byte("Lab status: ready<br>"))
	})
	mux.HandleFunc("/util/vcput.php_creds", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<strong>Cloud Access</strong><br>
<pre><span>[default]
aws_access_key_id=ASIAQZXK4NEXAMPLE01
aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
aws_session_token=IQoJb3JpZ2luX2VjEO7wEaCXVzLXdlc3QtMiJHMEUCIQDexampleTokenValueThatIsVeryLong==
</span></pre></div><strong>Cloud Labs</strong><br>
<span id="vlab-expiretime" class="hidden-1">` + expiresAt() + `</span>&nbsp;Remaining session time: 03:59:00(239 minutes)<br>
Accumulated lab time: 04:42:00 (282 minutes)<br>`))
	})
	return mux
}

// expiresAt es la marca Unix que devolvería un laboratorio recién arrancado.
func expiresAt() string {
	return strconv.FormatInt(time.Now().Add(3*time.Hour+59*time.Minute).Unix(), 10)
}

func newTestLab(t *testing.T, f *fakeLab) *Lab {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)

	hc, err := httpx.New()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := hc.Get(context.Background(), srv.URL+"/util/vcput.php_status")
	lab := NewLab(&Session{http: hc, base: srv.URL, page: u}, Endpoints{
		Start:       srv.URL + "/util/vcput.php_start",
		Stop:        srv.URL + "/util/vcput.php_stop",
		Status:      srv.URL + "/util/vcput.php_status",
		Credentials: srv.URL + "/util/vcput.php_creds",
	})
	lab.PollInterval = 5 * time.Millisecond // no hace falta esperar de verdad en un test
	return lab
}

func TestStartAndWait(t *testing.T) {
	fake := &fakeLab{readyAt: 6}
	lab := newTestLab(t, fake)
	ctx := context.Background()

	st, err := lab.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateStopped {
		t.Fatalf("estado inicial = %q, esperaba detenido", st.State)
	}

	if err := lab.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !fake.started {
		t.Fatal("Start no llegó al endpoint")
	}

	var ticks int
	final, err := lab.WaitForRunning(ctx, 60*time.Second, func(Status) { ticks++ })
	if err != nil {
		t.Fatalf("WaitForRunning: %v", err)
	}
	if !final.Running() {
		t.Errorf("estado final = %q", final.State)
	}
	if ticks < 2 {
		t.Errorf("esperaba varios avisos de progreso, hubo %d", ticks)
	}
}

func TestWaitForRunningTimeout(t *testing.T) {
	// Un laboratorio que nunca arranca tiene que rendirse, no colgarse.
	fake := &fakeLab{readyAt: 1 << 30}
	lab := newTestLab(t, fake)

	if err := lab.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := lab.WaitForRunning(context.Background(), 200*time.Millisecond, nil)
	if err == nil {
		t.Fatal("esperaba un timeout")
	}
	// El mensaje debe decir cuál fue el último estado visto, para poder diagnosticar.
	if got := err.Error(); !strings.Contains(got, "arrancando") {
		t.Errorf("el error debería incluir el último estado: %q", got)
	}
}

func TestCredentials(t *testing.T) {
	fake := &fakeLab{readyAt: 1}
	lab := newTestLab(t, fake)
	ctx := context.Background()

	if err := lab.Start(ctx); err != nil {
		t.Fatal(err)
	}
	creds, err := lab.Credentials(ctx)
	if err != nil {
		t.Fatalf("Credentials: %v", err)
	}
	if creds.AccessKeyID != "ASIAQZXK4NEXAMPLE01" {
		t.Errorf("AccessKeyID = %q", creds.AccessKeyID)
	}
	// Sin expiración, el AWS CLI cachearía las credenciales para siempre.
	if creds.Expiration.IsZero() {
		t.Error("esperaba la expiración que publica Vocareum")
	}
	if d := time.Until(creds.Expiration); d < 3*time.Hour || d > 4*time.Hour {
		t.Errorf("expiración a %v vista, esperaba ~3h59m", d)
	}
}

func TestDetectEndpoints(t *testing.T) {
	// Un extracto de la página real del laboratorio. Vocareum sirve varios
	// proveedores desde la misma página, así que hay que quedarse con AWS.
	u, _ := url.Parse("https://labs.vocareum.com/main/main.php?m=clabide&stepid=5679250")
	page := &httpx.Response{URL: u, Body: []byte(`
		<script>
		function startLab(){ vcAjax("../util/vcput.php?a=startaws&stepid=5679250&version=0&mode=s&type=1"); }
		function endLab(){ vcAjax("../util/vcput.php?a=endaws&stepid=5679250&version=0&mode=s&type=1"); }
		function poll(){ vcAjax("../util/vcput.php?a=getawsstatus&stepid=5679250&version=0&mode=s&type=1"); }
		function creds(){ vcAjax("../util/vcput.php?a=getaws&type=1&stepid=5679250&version=0&v="); }
		function startAzure(){ vcAjax("../util/vcput.php?a=startazure&stepid=5679250"); }
		function startGcp(){ vcAjax("../util/vcput.php?a=startgcp&stepid=5679250"); }
		</script>`)}

	ep := DetectEndpoints(page)

	// Deben quedar absolutas y conservar los parámetros de sesión: sin el
	// stepid, Vocareum no sabe de qué laboratorio le hablamos.
	want := map[string]string{
		"start":  "https://labs.vocareum.com/util/vcput.php?a=startaws&stepid=5679250&version=0&mode=s&type=1",
		"stop":   "https://labs.vocareum.com/util/vcput.php?a=endaws&stepid=5679250&version=0&mode=s&type=1",
		"status": "https://labs.vocareum.com/util/vcput.php?a=getawsstatus&stepid=5679250&version=0&mode=s&type=1",
		"creds":  "https://labs.vocareum.com/util/vcput.php?a=getaws&type=1&stepid=5679250&version=0&v=",
	}
	got := map[string]string{"start": ep.Start, "stop": ep.Stop, "status": ep.Status, "creds": ep.Credentials}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s =\n  %q\nesperaba\n  %q", k, got[k], w)
		}
	}
	if !ep.Complete() {
		t.Errorf("faltan endpoints: %v", ep.Missing())
	}
}

func TestDetectEndpointsIgnoresOtherClouds(t *testing.T) {
	// Si el laboratorio solo ofreciera Azure, no debemos confundirlo con AWS.
	u, _ := url.Parse("https://labs.vocareum.com/main/main.php")
	page := &httpx.Response{URL: u, Body: []byte(`
		<script>vcAjax("../util/vcput.php?a=startazure&stepid=1");</script>`)}

	ep := DetectEndpoints(page)
	if ep.Start != "" {
		t.Errorf("Start = %q, no debería reconocer un endpoint de Azure", ep.Start)
	}
	if ep.Complete() {
		t.Error("Complete() debería ser falso")
	}
	if len(ep.Missing()) != 4 {
		t.Errorf("Missing() = %v, esperaba los cuatro", ep.Missing())
	}
}

func TestLabWithoutEndpointsFailsClearly(t *testing.T) {
	// Sin endpoints hay que decirlo, no pegarle a una URL inventada y
	// reportar un 404 incomprensible.
	hc, err := httpx.New()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://labs.vocareum.com/main/main.php")
	lab := NewLab(&Session{http: hc, base: "https://labs.vocareum.com", page: &httpx.Response{URL: u}}, Endpoints{})

	_, err = lab.Status(context.Background())
	if !errors.Is(err, ErrEndpointsUnknown) {
		t.Fatalf("esperaba ErrEndpointsUnknown, obtuve %v", err)
	}
	if !strings.Contains(err.Error(), "debug lab") {
		t.Errorf("el error debería sugerir cómo diagnosticarlo: %q", err)
	}
}

func TestEndpointsMergePrefersDiscovered(t *testing.T) {
	// Lo recién detectado en la página le gana a lo que hubiera cacheado.
	cached := Endpoints{Start: "https://old/a?a=startaws", Stop: "https://old/a?a=endaws"}
	merged := cached.Merge(Endpoints{Start: "https://new/a?a=startaws"})

	if merged.Start != "https://new/a?a=startaws" {
		t.Errorf("Start = %q, esperaba el detectado", merged.Start)
	}
	if merged.Stop != "https://old/a?a=endaws" {
		t.Errorf("Stop = %q, esperaba conservar el cacheado", merged.Stop)
	}
}
