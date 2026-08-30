package awscreds

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/goslynn/awsacademycli/internal/state"
)

func TestConsoleDestination(t *testing.T) {
	tests := []struct {
		name    string
		region  string
		service string
		want    string
	}{
		{
			name:   "the console home when no service is asked for",
			region: "eu-west-1",
			want:   "https://eu-west-1.console.aws.amazon.com/console/home?region=eu-west-1",
		},
		{
			name:    "a regional service lives on the regional host",
			region:  "eu-west-1",
			service: "ec2",
			want:    "https://eu-west-1.console.aws.amazon.com/ec2/home?region=eu-west-1",
		},
		{
			name:    "a global service does not",
			region:  "eu-west-1",
			service: "iam",
			want:    "https://us-east-1.console.aws.amazon.com/iam/home?region=eu-west-1",
		},
		{
			name:    "the service name is not case sensitive",
			region:  "us-east-1",
			service: "EC2",
			want:    "https://us-east-1.console.aws.amazon.com/ec2/home?region=us-east-1",
		},
		{
			name:    "a full URL is passed through untouched",
			region:  "us-east-1",
			service: "https://us-east-1.console.aws.amazon.com/cloudwatch/home#logsV2:log-groups",
			want:    "https://us-east-1.console.aws.amazon.com/cloudwatch/home#logsV2:log-groups",
		},
		{
			name: "without a region it still produces a usable console",
			want: "https://us-east-1.console.aws.amazon.com/console/home?region=us-east-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConsoleDestination(tt.region, tt.service); got != tt.want {
				t.Errorf("ConsoleDestination(%q, %q) =\n  %s\nexpected\n  %s",
					tt.region, tt.service, got, tt.want)
			}
		})
	}
}

// fakeFederation stands in for signin.aws.amazon.com.
func fakeFederation(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	previous := federationEndpoint
	federationEndpoint = srv.URL
	t.Cleanup(func() { federationEndpoint = previous })
}

func TestConsoleURL(t *testing.T) {
	fakeFederation(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("Action"); got != "getSigninToken" {
			t.Errorf("Action = %q, expected getSigninToken", got)
		}
		// The credentials travel as a JSON blob, with AWS's own key names.
		var session map[string]string
		if err := json.Unmarshal([]byte(r.URL.Query().Get("Session")), &session); err != nil {
			t.Fatalf("the Session parameter is not JSON: %v", err)
		}
		for key, want := range map[string]string{
			"sessionId":    "ASIAEXAMPLE",
			"sessionKey":   "secret",
			"sessionToken": "token",
		} {
			if session[key] != want {
				t.Errorf("Session[%q] = %q, expected %q", key, session[key], want)
			}
		}
		// A duration must not be requested: AWS rejects it for role credentials.
		if _, ok := r.URL.Query()["SessionDuration"]; ok {
			t.Error("SessionDuration was sent; AWS rejects it for role credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"SigninToken":"signin-token-value"}`))
	})

	creds := &state.Credentials{
		AccessKeyID:     "ASIAEXAMPLE",
		SecretAccessKey: "secret",
		SessionToken:    "token",
	}
	destination := "https://us-east-1.console.aws.amazon.com/ec2/home?region=us-east-1"

	raw, err := ConsoleURL(context.Background(), creds, destination)
	if err != nil {
		t.Fatalf("ConsoleURL: %v", err)
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("the URL produced does not parse: %v", err)
	}
	q := parsed.Query()
	if q.Get("Action") != "login" {
		t.Errorf("Action = %q, expected login", q.Get("Action"))
	}
	if q.Get("SigninToken") != "signin-token-value" {
		t.Errorf("SigninToken = %q, expected the one the endpoint returned", q.Get("SigninToken"))
	}
	if q.Get("Destination") != destination {
		t.Errorf("Destination = %q, expected %q", q.Get("Destination"), destination)
	}
	if q.Get("Issuer") == "" {
		t.Error("Issuer is missing; AWS needs somewhere to send an expired session back to")
	}
}

func TestConsoleURLRefusesStaticCredentials(t *testing.T) {
	// Without a session token these are not lab credentials, and federation
	// only works with temporary ones.
	_, err := ConsoleURL(context.Background(),
		&state.Credentials{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "secret"}, "https://example.com")
	if err == nil {
		t.Fatal("expected static credentials to be refused")
	}
}

func TestConsoleURLReportsWhatAWSSaid(t *testing.T) {
	// Expired credentials are the usual case, and AWS explains it in the body;
	// swallowing that would leave the user with a bare HTTP code.
	fakeFederation(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Error retrieving SigninToken", http.StatusBadRequest)
	})

	_, err := ConsoleURL(context.Background(), &state.Credentials{
		AccessKeyID:     "ASIAEXAMPLE",
		SecretAccessKey: "secret",
		SessionToken:    "expired",
	}, "https://example.com")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Error retrieving SigninToken") {
		t.Errorf("the error does not carry what AWS said: %v", err)
	}
}
