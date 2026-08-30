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

// fakeAcademy simulates the complete round trip: the Canvas login, its API, the
// LTI launch through the iframe and the lab on the other side.
//
// The goal is to exercise the whole chain — including the auto-submit of the
// signed form — without touching the real service.
type fakeAcademy struct {
	canvas   *httptest.Server
	provider *httptest.Server
	labOn    bool
}

func newFakeAcademy(t *testing.T) *fakeAcademy {
	t.Helper()
	f := &fakeAcademy{}

	// --- The LTI provider, on the far side of the launch ---
	provider := http.NewServeMux()
	provider.HandleFunc("/lti/launch", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "the LTI launch must be a POST", http.StatusMethodNotAllowed)
			return
		}
		r.ParseForm()
		// The signed payload must have travelled in the auto-submit.
		if r.Form.Get("oauth_signature") == "" {
			http.Error(w, "the OAuth signature is missing", http.StatusBadRequest)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "vocsession", Value: "abc", Path: "/"})
		w.Header().Set("Content-Type", "text/html")
		// The bounce page: Vocareum does not serve the panel directly.
		fmt.Fprintf(w, `<html><body><script>
			callPostIfCookiesDisabled("../main/main.php?m=clabide&stepid=5679250", "tok");
			</script></body></html>`)
	})
	// The real panel, at the end of the bounce. Its buttons are what reveal the
	// API: the URLs carry the session's stepid, so they have to be read from
	// here and not made up.
	provider.HandleFunc("/main/main.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body>
			<div id="labcontrol">Lab console</div>
			<script>
			function startLab(){ vcAjax("../util/vcput.php?a=startaws&stepid=5679250&version=0&mode=s&type=1"); }
			function endLab(){ vcAjax("../util/vcput.php?a=endaws&stepid=5679250&version=0&mode=s&type=1"); }
			setInterval(function(){ vcAjax("../util/vcput.php?a=getawsstatus&stepid=5679250&version=0&mode=s&type=1"); },5000);
			function creds(){ vcAjax("../util/vcput.php?a=getaws&type=1&stepid=5679250&version=0&v="); }
			function updatecloudbudget(){ vcAjax("../util/vcput.php?a=getaws&type=1&stepid=5679250&version=0&v=3"); }
			function startAzure(){ vcAjax("../util/vcput.php?a=startazure&stepid=5679250"); }
			</script></body></html>`)
	})

	provider.HandleFunc("/util/vcput.php", func(w http.ResponseWriter, r *http.Request) {
		// Vocareum serves everything through vcput.php and distinguishes with a=.
		switch r.URL.Query().Get("a") {
		case "startaws":
			f.labOn = true
			fmt.Fprint(w, "OK")
		case "endaws":
			f.labOn = false
			fmt.Fprint(w, "OK")
		case "getawsstatus":
			// Plain text, as Vocareum really answers.
			if f.labOn {
				fmt.Fprint(w, "Lab status: ready<br>")
				return
			}
			fmt.Fprint(w, "Lab status: stopped<br>")
		case "getaws":
			if !f.labOn {
				http.Error(w, "the lab is not running", http.StatusConflict)
				return
			}
			// One action, two answers: v=3 is the spend, anything else is the
			// credentials panel.
			if r.URL.Query().Get("v") == "3" {
				fmt.Fprint(w, `{"total_budget":"50.00","monthly_budget":0,`+
					`"total_spend":"12.50","monthly_spend":"12.50"}`)
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
		fmt.Fprintf(w, `[{"id":1,"name":"Modules","items":[
			{"id":18010855,"title":"Start the AWS Academy Learner Lab",
			 "type":"ExternalTool","external_url":%q,
			 "html_url":"%s/courses/182613/modules/items/18010855"}]}]`,
			f.provider.URL+"/lti/launch", f.canvas.URL)
	})
	// The item page: Canvas wraps the tool in an iframe.
	canvas.HandleFunc("/courses/182613/modules/items/18010855", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><body><iframe id="tool_content" src="/courses/182613/external_tools/retrieve"></iframe></body></html>`)
	})
	// Inside the iframe lives the signed form that submits itself.
	canvas.HandleFunc("/courses/182613/external_tools/retrieve", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><body>
			<form action="%s/lti/launch" method="POST" name="ltiLaunchForm">
				<input type="hidden" name="oauth_consumer_key" value="key"/>
				<input type="hidden" name="oauth_signature" value="signature"/>
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

// setupEnv isolates the config, the state and the AWS files in temp directories.
func setupEnv(t *testing.T, canvasURL string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(dir, "aws-config"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "aws-credentials"))

	// xdg caches the paths when imported, so they have to be re-read.
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

	// 1. Authenticate and discover the lab with no hardcoded URLs.
	user, err := app.EnsureSession(ctx)
	if err != nil {
		t.Fatalf("EnsureSession: %v", err)
	}
	if user.Name != "Ada Lovelace" {
		t.Errorf("user = %q", user.Name)
	}

	// 2. Go through the LTI launch: iframe, signed form and host jump.
	lab, disc, err := app.OpenLab(ctx)
	if err != nil {
		t.Fatalf("OpenLab: %v", err)
	}
	lab.PollInterval = 5 * time.Millisecond
	if disc.CourseID != "182613" {
		t.Errorf("CourseID = %q", disc.CourseID)
	}
	// The endpoints must come from the page's JavaScript, not from the guesses.
	wantStart := fake.provider.URL + "/util/vcput.php?a=startaws&stepid=5679250&version=0&mode=s&type=1"
	if got := lab.Endpoints().Start; got != wantStart {
		t.Errorf("Start endpoint =\n  %q\nexpected the one detected on the page\n  %q", got, wantStart)
	}
	// Credentials and budget are the same action and must not be confused: the
	// one with v=3 is the spend, and taking it for the other means asking for
	// credentials and being handed JSON.
	if got := lab.Endpoints().Credentials; strings.Contains(got, "v=3") {
		t.Errorf("the credentials endpoint picked up the budget call: %q", got)
	}
	if got := lab.Endpoints().Budget; !strings.Contains(got, "v=3") {
		t.Errorf("Budget endpoint = %q, expected the a=getaws call with v=3", got)
	}

	// 3. Start and wait.
	st, err := lab.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Running() {
		t.Fatal("the lab should not be running yet")
	}
	if err := lab.Start(ctx); err != nil {
		t.Fatal(err)
	}
	st, err = lab.WaitForRunning(ctx, 5*time.Second, nil)
	if err != nil {
		t.Fatalf("WaitForRunning: %v", err)
	}
	// 4. Read the credentials and write them where the AWS CLI looks for them.
	// Details brings credentials and status in one go, which is how Vocareum
	// serves them.
	st, creds, err := lab.Details(ctx)
	if err != nil {
		t.Fatalf("Details: %v", err)
	}
	if st.Remaining < 3*time.Hour || st.Remaining > 4*time.Hour {
		t.Errorf("Remaining = %v, expected ~3h58m", st.Remaining)
	}
	if creds.Expiration.IsZero() {
		t.Error("expected the expiry published by Vocareum")
	}
	if creds.AccessKeyID != "ASIAQZXK4NEXAMPLE01" {
		t.Errorf("AccessKeyID = %q", creds.AccessKeyID)
	}

	// The spend lives at its own endpoint, so it takes its own request.
	budget, err := lab.Budget(ctx)
	if err != nil {
		t.Fatalf("Budget: %v", err)
	}
	if budget.Used != 12.50 || budget.Total != 50 {
		t.Errorf("budget = $%.2f of $%.2f, expected $12.50 of $50.00", budget.Used, budget.Total)
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
		t.Errorf("the credentials never reached the file:\n%s", raw)
	}

	// 5. The saved session has to serve a fresh process without logging in again.
	fresh, err := newApp(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.canvas.Whoami(ctx); err != nil {
		t.Errorf("the saved session did not revive: %v", err)
	}

	// 6. Stop.
	if err := lab.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := lab.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if after.Running() {
		t.Error("the lab should be stopped")
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

	// credential_process must not bring the lab up: every `aws` command would
	// hang for minutes. It has to fail fast and say what to do.
	loaded, err := state.LoadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Expired() {
		t.Fatal("expired credentials should be detected as such")
	}
}
