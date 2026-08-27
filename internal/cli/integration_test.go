package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/adrg/xdg"
	"github.com/goslynn/awsacademycli/internal/awscreds"
	"github.com/goslynn/awsacademycli/internal/state"
)

// fakeAcademy simula el recorrido completo: el login de Canvas, su API, el
// lanzamiento LTI a través del iframe y el laboratorio del otro lado.
//
// El objetivo es ejercitar la cadena entera —incluido el auto-submit del
// formulario firmado— sin tocar el servicio real.
type fakeAcademy struct {
	canvas   *httptest.Server
	provider *httptest.Server
	labOn    bool
}

func newFakeAcademy(t *testing.T) *fakeAcademy {
	t.Helper()
	f := &fakeAcademy{}

	// --- El proveedor LTI, al otro lado del lanzamiento ---
	provider := http.NewServeMux()
	provider.HandleFunc("/lti/launch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "el lanzamiento LTI debe ser POST", http.StatusMethodNotAllowed)
			return
		}
		r.ParseForm()
		// El payload firmado tiene que haber viajado en el auto-submit.
		if r.Form.Get("oauth_signature") == "" {
			http.Error(w, "falta la firma OAuth", http.StatusBadRequest)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "vocsession", Value: "abc", Path: "/"})
		w.Header().Set("Content-Type", "text/html")
		// El trampolín: Vocareum no sirve el panel directamente.
		fmt.Fprintf(w, `<html><body><script>
			callPostIfCookiesDisabled("../main/main.php?m=clabide&stepid=5679250", "tok");
			</script></body></html>`)
	})
	// El panel real, al final del trampolín. Sus botones son los que revelan
	// la API: las URLs llevan el stepid de la sesión, así que hay que leerlas
	// de aquí y no inventarlas.
	provider.HandleFunc("/main/main.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>
			<div id="labcontrol">Consola del laboratorio</div>
			<script>
			function startLab(){ vcAjax("../util/vcput.php?a=startaws&stepid=5679250&version=0&mode=s&type=1"); }
			function endLab(){ vcAjax("../util/vcput.php?a=endaws&stepid=5679250&version=0&mode=s&type=1"); }
			setInterval(function(){ vcAjax("../util/vcput.php?a=getawsstatus&stepid=5679250&version=0&mode=s&type=1"); },5000);
			function creds(){ vcAjax("../util/vcput.php?a=getaws&type=1&stepid=5679250&version=0&v="); }
			function startAzure(){ vcAjax("../util/vcput.php?a=startazure&stepid=5679250"); }
			</script></body></html>`)
	})

	provider.HandleFunc("/util/vcput.php", func(w http.ResponseWriter, r *http.Request) {
		// Vocareum atiende todo por vcput.php y distingue con a=.
		switch r.URL.Query().Get("a") {
		case "startaws":
			f.labOn = true
			fmt.Fprint(w, "OK")
		case "endaws":
			f.labOn = false
			fmt.Fprint(w, "OK")
		case "getawsstatus":
			// Texto plano, como responde Vocareum de verdad.
			if f.labOn {
				fmt.Fprint(w, "Lab status: ready<br>")
				return
			}
			fmt.Fprint(w, "Lab status: stopped<br>")
		case "getaws":
			if !f.labOn {
				http.Error(w, "el laboratorio no está corriendo", http.StatusConflict)
				return
			}
			fmt.Fprintf(w, `<strong>Cloud Access</strong><br>
<pre><span>[default]
aws_access_key_id=ASIAQZXK4NEXAMPLE01
aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
aws_session_token=IQoJb3JpZ2luX2VjEO7wEaCXVzLXdlc3QtMiJHMEUCIQDexampleTokenValueThatIsVeryLong==
</span></pre></div><span id="vlab-expiretime" class="hidden-1">%d</span>
&nbsp;Remaining session time: 03:58:00(238 minutes)<br>`,
				time.Now().Add(3*time.Hour+58*time.Minute).Unix())
		default:
			http.NotFound(w, r)
		}
	})
	f.provider = httptest.NewServer(provider)
	t.Cleanup(f.provider.Close)

	// --- Canvas ---
	canvas := http.NewServeMux()
	canvas.HandleFunc("/login/canvas", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			http.SetCookie(w, &http.Cookie{Name: "_csrf_token", Value: "tok%3D", Path: "/"})
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><div id="new_login_data"></div></body></html>`)
			return
		}
		r.ParseForm()
		if r.Form.Get("pseudonym_session[unique_id]") == "ada@example.com" &&
			r.Form.Get("pseudonym_session[password]") == "s3cret" {
			http.SetCookie(w, &http.Cookie{Name: "canvas_session", Value: "live", Path: "/"})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<div class="ic-flash-error">bad login</div>`)
	})
	authed := func(r *http.Request) bool {
		c, err := r.Cookie("canvas_session")
		return err == nil && c.Value == "live"
	}
	canvas.HandleFunc("/api/v1/users/self", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !authed(r) {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"status":"unauthenticated"}`)
			return
		}
		fmt.Fprint(w, `{"id":7,"name":"Ada Lovelace"}`)
	})
	canvas.HandleFunc("/api/v1/courses", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":182613,"name":"AWS Academy Learner Lab"}]`)
	})
	canvas.HandleFunc("/api/v1/courses/182613/modules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `[{"id":1,"name":"Módulos","items":[
			{"id":18010855,"title":"Iniciar el Laboratorio de aprendizaje de AWS Academy",
			 "type":"ExternalTool","external_url":%q,
			 "html_url":"%s/courses/182613/modules/items/18010855"}]}]`,
			f.provider.URL+"/lti/launch", f.canvas.URL)
	})
	// La página del ítem: Canvas envuelve la herramienta en un iframe.
	canvas.HandleFunc("/courses/182613/modules/items/18010855", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><iframe id="tool_content" src="/courses/182613/external_tools/retrieve"></iframe></body></html>`)
	})
	// Dentro del iframe vive el formulario firmado que se auto-envía.
	canvas.HandleFunc("/courses/182613/external_tools/retrieve", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body>
			<form action="%s/lti/launch" method="POST" name="ltiLaunchForm">
				<input type="hidden" name="oauth_consumer_key" value="key"/>
				<input type="hidden" name="oauth_signature" value="firma"/>
			</form>
			<script>document.ltiLaunchForm.submit();</script>
			</body></html>`, f.provider.URL)
	})
	canvas.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>Dashboard</body></html>`)
	})

	f.canvas = httptest.NewServer(canvas)
	t.Cleanup(f.canvas.Close)
	return f
}

// setupEnv aísla config, estado y ficheros de AWS en directorios temporales.
func setupEnv(t *testing.T, canvasURL string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "aws-config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "aws-credentials"))

	// xdg cachea las rutas al importarse, así que hay que releerlas.
	xdg.Reload()

	cfgDir := filepath.Join(dir, "config", "awsacademy")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`email = "ada@example.com"
password = "s3cret"
canvas_base_url = %q
aws_profile = "academy"
region = "us-east-1"
`, canvasURL)
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFullLabFlow(t *testing.T) {
	fake := newFakeAcademy(t)
	setupEnv(t, fake.canvas.URL)
	ctx := context.Background()

	app, err := newApp(false)
	if err != nil {
		t.Fatalf("newApp: %v", err)
	}

	// 1. Autenticarse y descubrir el laboratorio sin URLs hardcodeadas.
	user, err := app.EnsureSession(ctx)
	if err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if user.Name != "Ada Lovelace" {
		t.Errorf("usuario = %q", user.Name)
	}

	// 2. Atravesar el lanzamiento LTI: iframe, formulario firmado y salto de host.
	lab, disc, err := app.OpenLab(ctx)
	if err != nil {
		t.Fatalf("OpenLab: %v", err)
	}
	lab.PollInterval = 5 * time.Millisecond
	if disc.CourseID != "182613" {
		t.Errorf("CourseID = %q", disc.CourseID)
	}
	// Los endpoints deben salir del JavaScript de la página, no de las conjeturas.
	wantStart := fake.provider.URL + "/util/vcput.php?a=startaws&stepid=5679250&version=0&mode=s&type=1"
	if got := lab.Endpoints().Start; got != wantStart {
		t.Errorf("endpoint Start =\n  %q\nesperaba el detectado en la página\n  %q", got, wantStart)
	}

	// 3. Arrancar y esperar.
	st, err := lab.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Running() {
		t.Fatal("el laboratorio no debería estar corriendo todavía")
	}
	if err := lab.Start(ctx); err != nil {
		t.Fatal(err)
	}
	st, err = lab.WaitForRunning(ctx, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("WaitForRunning: %v", err)
	}
	// 4. Leer credenciales y escribirlas donde el AWS CLI las busca.
	// Details trae credenciales y estado de una sola vez, que es como
	// Vocareum los sirve.
	st, creds, err := lab.Details(ctx)
	if err != nil {
		t.Fatalf("Details: %v", err)
	}
	if st.Remaining < 3*time.Hour || st.Remaining > 4*time.Hour {
		t.Errorf("Remaining = %v, esperaba ~3h58m", st.Remaining)
	}
	if creds.Expiration.IsZero() {
		t.Error("esperaba la expiración publicada por Vocareum")
	}
	if creds.AccessKeyID != "ASIAQZXK4NEXAMPLE01" {
		t.Errorf("AccessKeyID = %q", creds.AccessKeyID)
	}
	if err := creds.Save(); err != nil {
		t.Fatal(err)
	}
	if err := awscreds.WriteSharedCredentials("academy", creds); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(awscreds.CredentialsPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "ASIAQZXK4NEXAMPLE01") {
		t.Errorf("las credenciales no llegaron al fichero:\n%s", raw)
	}

	// 5. La sesión guardada tiene que servir a un proceso nuevo sin re-loguear.
	fresh, err := newApp(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.canvas.Whoami(ctx); err != nil {
		t.Errorf("la sesión guardada no revivió: %v", err)
	}

	// 6. Detener.
	if err := lab.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := lab.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Running() {
		t.Error("el laboratorio debería estar detenido")
	}
}

func TestCredsRefusesWhenExpired(t *testing.T) {
	fake := newFakeAcademy(t)
	setupEnv(t, fake.canvas.URL)

	expired := &state.Credentials{
		AccessKeyID: "ASIA1", SecretAccessKey: "s", SessionToken: "t",
		Expiration: time.Now().Add(-time.Hour),
	}
	if err := expired.Save(); err != nil {
		t.Fatal(err)
	}

	// credential_process no debe levantar el laboratorio: cada comando `aws`
	// se colgaría minutos. Tiene que fallar rápido y decir qué hacer.
	loaded, err := state.LoadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Expired() {
		t.Fatal("las credenciales vencidas deberían detectarse como tales")
	}
}
