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
	// Vocareum answers in plain text, not in JSON.
	tests := []struct {
		name  string
		body  string
		want  LabState
		clock time.Duration
	}{
		{"ready", "Lab status: ready<br>", StateRunning, 0},
		{"starting", "Lab status: starting<br>", StateStarting, 0},
		{"stopped", "Lab status: stopped<br>", StateStopped, 0},
		{"not started", "Lab status: not started<br>", StateStopped, 0},
		{"stopping", "Lab status: terminating<br>", StateStopping, 0},
		{"unreadable", "<html><body>something went wrong</body></html>", StateUnknown, 0},
		{
			// Only the labelled countdown counts: several clocks coexist on the
			// lab page and the others measure something else.
			name:  "labelled countdown, not the accumulated one",
			body:  "Remaining session time: 03:53:27(234 minutes)<br>Accumulated lab time: 04:42:00 (282 minutes)",
			want:  StateRunning,
			clock: 3*time.Hour + 53*time.Minute + 27*time.Second,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseStatus(tt.body)
			if got.State != tt.want {
				t.Errorf("State = %q, expected %q", got.State, tt.want)
			}
			if got.Remaining != tt.clock {
				t.Errorf("Remaining = %v, expected %v", got.Remaining, tt.clock)
			}
		})
	}
}

func TestParseStatusIgnoresSubstrings(t *testing.T) {
	// A page of hundreds of kilobytes contains "red" inside "required" and
	// "ready" inside "already". Searching for loose words across the body gave
	// false positives; only the label counts.
	body := `<input required class="hidden"> it is already configured, green button`
	if got := parseStatus(body); got.State != StateUnknown {
		t.Errorf("State = %q, expected unknown", got.State)
	}
}

func TestParseExpiry(t *testing.T) {
	body := `<span id="vlab-expiretime" class="hidden-1">1787803239</span>&nbsp;Remaining session time: 03:53:27`
	exp, ok := ParseExpiry(body)
	if !ok {
		t.Fatal("expected to find the expiry timestamp")
	}
	if exp.Unix() != 1787803239 {
		t.Errorf("Expiry = %v (%d)", exp, exp.Unix())
	}
}

// fakeLab simulates a lab that takes a few polls to start.
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

// expiresAt is the Unix timestamp a freshly started lab would return.
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
	lab.PollInterval = 5 * time.Millisecond // no need to wait for real in a test
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
		t.Fatalf("initial state = %q, expected stopped", st.State)
	}

	if err := lab.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if !fake.started {
		t.Fatal("Start never reached the endpoint")
	}

	var ticks int
	final, err := lab.WaitForRunning(ctx, 60*time.Second, func(Status) { ticks++ })
	if err != nil {
		t.Fatalf("WaitForRunning: %v", err)
	}
	if !final.Running() {
		t.Errorf("final state = %q", final.State)
	}
	if ticks < 2 {
		t.Errorf("expected several progress notices, there were %d", ticks)
	}
}

func TestWaitForRunningTimeout(t *testing.T) {
	// A lab that never starts has to give up, not hang.
	fake := &fakeLab{readyAt: 1 << 30}
	lab := newTestLab(t, fake)

	if err := lab.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err := lab.WaitForRunning(context.Background(), 200*time.Millisecond, nil)
	if err == nil {
		t.Fatal("expected a timeout")
	}
	// The message must say what the last state seen was, so it can be diagnosed.
	if got := err.Error(); !strings.Contains(got, "starting") {
		t.Errorf("the error should include the last state: %q", got)
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
	// Without an expiry, the AWS CLI would cache the credentials forever.
	if creds.Expiration.IsZero() {
		t.Error("expected the expiry Vocareum publishes")
	}
	if d := time.Until(creds.Expiration); d < 3*time.Hour || d > 4*time.Hour {
		t.Errorf("expiry seen at %v, expected ~3h59m", d)
	}
}

func TestDetectEndpoints(t *testing.T) {
	// An extract of the real lab page. Vocareum serves several providers from
	// the same page, so we have to keep the AWS ones.
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

	// They must come out absolute and keep the session parameters: without the
	// stepid, Vocareum does not know which lab we are talking about.
	want := map[string]string{
		"start":  "https://labs.vocareum.com/util/vcput.php?a=startaws&stepid=5679250&version=0&mode=s&type=1",
		"stop":   "https://labs.vocareum.com/util/vcput.php?a=endaws&stepid=5679250&version=0&mode=s&type=1",
		"status": "https://labs.vocareum.com/util/vcput.php?a=getawsstatus&stepid=5679250&version=0&mode=s&type=1",
		"creds":  "https://labs.vocareum.com/util/vcput.php?a=getaws&type=1&stepid=5679250&version=0&v=",
	}
	got := map[string]string{"start": ep.Start, "stop": ep.Stop, "status": ep.Status, "creds": ep.Credentials}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s =\n  %q\nexpected\n  %q", k, got[k], w)
		}
	}
	if !ep.Complete() {
		t.Errorf("missing endpoints: %v", ep.Missing())
	}
}

func TestDetectEndpointsIgnoresOtherClouds(t *testing.T) {
	// If the lab only offered Azure, we must not mistake it for AWS.
	u, _ := url.Parse("https://labs.vocareum.com/main/main.php")
	page := &httpx.Response{URL: u, Body: []byte(`
		<script>vcAjax("../util/vcput.php?a=startazure&stepid=1");</script>`)}

	ep := DetectEndpoints(page)
	if ep.Start != "" {
		t.Errorf("Start = %q, it should not recognise an Azure endpoint", ep.Start)
	}
	if ep.Complete() {
		t.Error("Complete() should be false")
	}
	if len(ep.Missing()) != 4 {
		t.Errorf("Missing() = %v, expected all four", ep.Missing())
	}
}

func TestLabWithoutEndpointsFailsClearly(t *testing.T) {
	// With no endpoints we have to say so, not hit a made-up URL and report an
	// incomprehensible 404.
	hc, err := httpx.New()
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse("https://labs.vocareum.com/main/main.php")
	lab := NewLab(&Session{http: hc, base: "https://labs.vocareum.com", page: &httpx.Response{URL: u}}, Endpoints{})

	_, err = lab.Status(context.Background())
	if !errors.Is(err, ErrEndpointsUnknown) {
		t.Fatalf("expected ErrEndpointsUnknown, got %v", err)
	}
	if !strings.Contains(err.Error(), "debug lab") {
		t.Errorf("the error should suggest how to diagnose it: %q", err)
	}
}

func TestEndpointsMergePrefersDiscovered(t *testing.T) {
	// What was just detected on the page beats whatever was cached.
	cached := Endpoints{Start: "https://old/a?a=startaws", Stop: "https://old/a?a=endaws"}
	merged := cached.Merge(Endpoints{Start: "https://new/a?a=startaws"})

	if merged.Start != "https://new/a?a=startaws" {
		t.Errorf("Start = %q, expected the detected one", merged.Start)
	}
	if merged.Stop != "https://old/a?a=endaws" {
		t.Errorf("Stop = %q, expected the cached one to be kept", merged.Stop)
	}
}
