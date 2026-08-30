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

func TestParseBudgetJSON(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		used, total float64
		monthly     bool
	}{
		{
			// The real shape, as the lab answers it: amounts quoted, an absent
			// allowance as a bare zero, in the same object.
			name:  "the total budget, with mixed types",
			body:  `{"total_budget":"50.00","monthly_budget":0,"total_spend":"0.014342","monthly_spend":"0.014342"}`,
			used:  0.014342,
			total: 50,
		},
		{
			// A monthly allowance wins over the total, which is the rule the
			// page's own budgetString2 follows.
			name:    "a monthly allowance takes precedence",
			body:    `{"total_budget":"100.00","total_spend":"75.00","monthly_budget":"20.00","monthly_spend":"3.50"}`,
			used:    3.5,
			total:   20,
			monthly: true,
		},
		{
			name:  "amounts may arrive unquoted",
			body:  `{"total_budget":50,"monthly_budget":0,"total_spend":12.5,"monthly_spend":0}`,
			used:  12.5,
			total: 50,
		},
		{
			name:  "a null allowance is not an allowance",
			body:  `{"total_budget":"50.00","monthly_budget":null,"total_spend":"1.00","monthly_spend":null}`,
			used:  1,
			total: 50,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget, err := ParseBudgetJSON(tt.body)
			if err != nil {
				t.Fatalf("ParseBudgetJSON: %v", err)
			}
			if budget.Used != tt.used {
				t.Errorf("used = %v, expected %v", budget.Used, tt.used)
			}
			if budget.Total != tt.total {
				t.Errorf("total = %v, expected %v", budget.Total, tt.total)
			}
			if budget.Monthly != tt.monthly {
				t.Errorf("monthly = %v, expected %v", budget.Monthly, tt.monthly)
			}
		})
	}
}

func TestParseBudgetJSONRejects(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		// Vocareum says so in plain text instead of failing the request; the
		// page checks for exactly this before parsing.
		{"the failure marker", "fail_getaws_cost"},
		{"the credentials panel, asked for with the wrong v=", "<pre>[default]\naws_access_key_id=ASIA...</pre>"},
		{"an empty response", ""},
		{"no cap at all", `{"total_budget":0,"monthly_budget":0,"total_spend":"1.00","monthly_spend":0}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if budget, err := ParseBudgetJSON(tt.body); err == nil {
				t.Errorf("expected an error, got %+v", budget)
			}
		})
	}
}
