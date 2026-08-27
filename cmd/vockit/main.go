// Command vockit captures the real AWS Academy traffic so the HTTP client can
// be written against evidence rather than against assumptions.
//
// It is not part of the binary that gets installed: it is the tool you use once
// at the beginning, and again if Canvas or Vocareum change. That is why it
// lives in its own command, and why chromedp never enters cmd/awsacademy.
//
// Usage:
//
//	go run ./cmd/vockit -out capture.json
//
// It opens a browser, you do the flow by hand (log in, open the lab, Start Lab,
// AWS Details) and on closing with Ctrl+C you are left with a dump of every XHR
// with its URL, method, headers, body and response.
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

// exchange is a request with its response, as the browser saw them.
type exchange struct {
	Time           time.Time         `json:"time"`
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	ResourceType   string            `json:"resource_type,omitempty"`
	RequestHeaders map[string]string `json:"request_headers,omitempty"`
	PostData       string            `json:"post_data,omitempty"`
	Status         int64             `json:"status,omitempty"`
	// postDataPending marks the bodies CDP omitted because of their size.
	postDataPending bool              `json:"-"`
	ResponseHeaders map[string]string `json:"response_headers,omitempty"`
	ResponseBody    string            `json:"response_body,omitempty"`
}

func main() {
	var (
		startURL = flag.String("url", "https://awsacademy.instructure.com/login/canvas",
			"starting page")
		out         = flag.String("out", "capture.json", "output file")
		filterHosts = flag.String("hosts", "vocareum.com,instructure.com",
			"comma-separated list: only these hosts are captured (empty = all)")
		browser = flag.String("browser", "", "browser path (autodetected if omitted)")
		profile = flag.String("profile", "",
			"browser profile directory; keeping one around avoids logging in again on every capture")
		maxBody = flag.Int("max-body", 256*1024, "maximum body bytes to store per response")
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
	fmt.Fprintf(os.Stderr, "browser: %s\n", browserPath)

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
		// We need to see the window: the flow is done by the person, not by
		// the code.
		chromedp.Flag("headless", false),
		// Brave starts up with its own features that get in the way of
		// automation.
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
		return fmt.Errorf("could not start the browser: %w", err)
	}

	fmt.Fprintf(os.Stderr, `
Browser open. Do the complete flow by hand:

  1. Log in to Canvas
  2. Enter the course and open the Learner Lab item
  3. Start Lab, wait for the light to turn green
  4. Open "AWS Details" and reveal the AWS CLI block
  5. End Lab

When you are done, come back here and press Ctrl+C to save the capture.

`)

	// We finish either on Ctrl+C or because the browser was closed; in both
	// cases what was captured has to be dumped, which is the point of all this.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	select {
	case <-sigs:
		fmt.Fprintln(os.Stderr, "\ninterrupted, saving...")
	case <-taskCtx.Done():
		fmt.Fprintln(os.Stderr, "\nbrowser closed, saving...")
	}

	return rec.dump(out)
}

// recorder accumulates the exchanges that pass the host filter.
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
		// CDP omits the body when it is large; it is requested separately later.
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

	// The body is requested separately and is only available for a while, so it
	// is collected as soon as the event arrives and in its own context.
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
			body = append(body[:r.maxBody], []byte("\n...[truncated]")...)
		}
		r.mu.Lock()
		ex.ResponseBody = string(body)
		r.mu.Unlock()
	}()
}

// wanted decides whether the URL makes it into the capture.
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
	// Give the in-flight body downloads a moment before dumping.
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
	// 0600: the capture carries session cookies and AWS credentials in the clear.
	if err := os.WriteFile(out, raw, 0o600); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "\n%d exchanges saved in %s\n", len(list), out)
	fmt.Fprintln(os.Stderr, "CAREFUL: it contains cookies and credentials in the clear. Do not publish it.")
	return nil
}

// postDataOf reassembles the request body. CDP delivers it in chunks and in
// base64, and omits it entirely when it is too large.
func postDataOf(req *network.Request) string {
	if len(req.PostDataEntries) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, entry := range req.PostDataEntries {
		decoded, err := base64.StdEncoding.DecodeString(entry.Bytes)
		if err != nil {
			// If it was not base64, we store it as-is rather than lose it.
			sb.WriteString(entry.Bytes)
			continue
		}
		sb.Write(decoded)
	}
	return sb.String()
}

// requestPostData retrieves a body that CDP did not include in the event.
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

// findBrowser locates a Chromium-based browser.
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
	return "", fmt.Errorf("could not find a Chromium-based browser (tried: %s); "+
		"point at one with -browser", strings.Join(candidates, ", "))
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
