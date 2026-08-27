package vocareum

import (
	"strings"
	"testing"
	"time"
)

func TestParseCredentials(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{
			// The format as the lab's "AWS CLI" panel shows it.
			name: "INI block with profile",
			text: `[default]
aws_access_key_id=ASIAQZXK4NEXAMPLE01
aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
aws_session_token=IQoJb3JpZ2luX2VjEO7//////////wEaCXVzLXdlc3QtMiJHMEUCIQDexampleTokenValueThatIsVeryLongIndeed==`,
		},
		{
			name: "with spaces around the equals sign",
			text: `aws_access_key_id = ASIAQZXK4NEXAMPLE01
aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
aws_session_token = IQoJb3JpZ2luX2VjEO7//////////wEaCXVzLXdlc3QtMiJHMEUCIQDexampleTokenValueThatIsVeryLongIndeed==`,
		},
		{
			name: "wrapped in HTML",
			text: `<pre id="clikey">[default]
aws_access_key_id=ASIAQZXK4NEXAMPLE01
aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
aws_session_token=IQoJb3JpZ2luX2VjEO7//////////wEaCXVzLXdlc3QtMiJHMEUCIQDexampleTokenValueThatIsVeryLongIndeed==</pre>`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			creds, err := ParseCredentials(tt.text)
			if err != nil {
				t.Fatalf("ParseCredentials: %v", err)
			}
			if creds.AccessKeyID != "ASIAQZXK4NEXAMPLE01" {
				t.Errorf("AccessKeyID = %q", creds.AccessKeyID)
			}
			if creds.SecretAccessKey != "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" {
				t.Errorf("SecretAccessKey = %q", creds.SecretAccessKey)
			}
			if len(creds.SessionToken) < 50 {
				t.Errorf("SessionToken too short: %q", creds.SessionToken)
			}
		})
	}
}

func TestParseCredentialsMissingToken(t *testing.T) {
	// Without a session token these are not lab credentials: better to fail
	// saying what was missing than to write a half-finished profile.
	_, err := ParseCredentials(`aws_access_key_id=ASIAQZXK4NEXAMPLE01
aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`)
	if err == nil {
		t.Fatal("expected an error when aws_session_token is missing")
	}
	if got := err.Error(); !strings.Contains(got, "aws_session_token") {
		t.Errorf("the error should name the missing field: %q", got)
	}
}

func TestParseRemaining(t *testing.T) {
	tests := []struct {
		text string
		want time.Duration
		ok   bool
	}{
		{"3:59:30", 3*time.Hour + 59*time.Minute + 30*time.Second, true},
		{"Session ends in 0:04:15", 4*time.Minute + 15*time.Second, true},
		{"2h 30m remaining", 2*time.Hour + 30*time.Minute, true},
		{"1:05", time.Hour + 5*time.Minute, true},
		{"no visible countdown", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			got, ok := ParseRemaining(tt.text)
			if ok != tt.ok {
				t.Fatalf("ok = %v, expected %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("= %v, expected %v", got, tt.want)
			}
		})
	}
}

func TestParseBudget(t *testing.T) {
	used, total, ok := ParseBudget("$12.34 used of $100")
	if !ok {
		t.Fatal("expected to find the budget")
	}
	if used != 12.34 {
		t.Errorf("used = %v, expected 12.34", used)
	}
	if total != 100 {
		t.Errorf("total = %v, expected 100", total)
	}
}
