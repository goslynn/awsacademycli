package httpx

import (
	"net/http"
	"net/url"
	"testing"
)

func mkResp(t *testing.T, body string) *Response {
	t.Helper()
	u, err := url.Parse("https://awsacademy.instructure.com/courses/1/external_tools/retrieve")
	if err != nil {
		t.Fatal(err)
	}
	return &Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/html; charset=utf-8"}},
		Body:       []byte(body),
		URL:        u,
	}
}

func TestFindAutoSubmitForm(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantForm   bool
		wantAction string
		wantMethod string
		wantField  string
		wantValue  string
	}{
		{
			// LTI 1.1: a form signed with OAuth1 and a script that fires it.
			name: "LTI 1.1 launch",
			body: `<html><body>
				<form action="https://labs.vocareum.com/lti/launch" method="POST" name="ltiLaunchForm">
					<input type="hidden" name="oauth_consumer_key" value="abc123"/>
					<input type="hidden" name="oauth_signature" value="deadbeef"/>
					<input type="hidden" name="lti_message_type" value="basic-lti-launch-request"/>
				</form>
				<script>document.ltiLaunchForm.submit();</script>
			</body></html>`,
			wantForm:   true,
			wantAction: "https://labs.vocareum.com/lti/launch",
			wantMethod: "POST",
			wantField:  "oauth_signature",
			wantValue:  "deadbeef",
		},
		{
			// LTI 1.3: the id_token travels the same way, in a self-submitting form.
			name: "LTI 1.3 launch with body onload",
			body: `<html><body onload="document.forms[0].submit()">
				<form action="/lti/callback" method="POST">
					<input type="hidden" name="id_token" value="ey.jwt.here"/>
					<input type="hidden" name="state" value="s123"/>
				</form>
			</body></html>`,
			wantForm:   true,
			wantAction: "https://awsacademy.instructure.com/lti/callback",
			wantMethod: "POST",
			wantField:  "id_token",
			wantValue:  "ey.jwt.here",
		},
		{
			// A form with no trigger is a form the user is meant to fill in.
			// Resubmitting it empty would break the login, so it is left alone.
			name: "the login form is not auto-submitted",
			body: `<html><body>
				<form action="/login/canvas" method="POST">
					<input type="hidden" name="authenticity_token" value="tok"/>
					<input type="text" name="pseudonym_session[unique_id]" value=""/>
					<input type="submit" value="Log in"/>
				</form>
			</body></html>`,
			wantForm: false,
		},
		{
			name:     "page with no forms",
			body:     `<html><body><h1>Learner Lab</h1></body></html>`,
			wantForm: false,
		},
		{
			// The trigger exists and points at a form; with several of them,
			// the one carrying hidden fields wins.
			name: "picks the form with hidden fields",
			body: `<html><body>
				<form action="/search" method="GET"><input type="text" name="q"/></form>
				<form action="/lti" method="POST"><input type="hidden" name="payload" value="p"/></form>
				<script>document.forms[1].submit()</script>
			</body></html>`,
			wantForm:   true,
			wantAction: "https://awsacademy.instructure.com/lti",
			wantMethod: "POST",
			wantField:  "payload",
			wantValue:  "p",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			form, err := findAutoSubmitForm(mkResp(t, tt.body))
			if err != nil {
				t.Fatalf("findAutoSubmitForm: %v", err)
			}
			if !tt.wantForm {
				if form != nil {
					t.Fatalf("expected nil, got action=%q", form.Action)
				}
				return
			}
			if form == nil {
				t.Fatal("expected a form, got nil")
			}
			if form.Action != tt.wantAction {
				t.Errorf("Action = %q, expected %q", form.Action, tt.wantAction)
			}
			if form.Method != tt.wantMethod {
				t.Errorf("Method = %q, expected %q", form.Method, tt.wantMethod)
			}
			if got := form.Values.Get(tt.wantField); got != tt.wantValue {
				t.Errorf("Values[%q] = %q, expected %q", tt.wantField, got, tt.wantValue)
			}
		})
	}
}

func TestParseFormSkipsUncheckedBoxes(t *testing.T) {
	resp := mkResp(t, `<html><body>
		<form action="/x" method="POST">
			<input type="checkbox" name="checked_box" value="1" checked/>
			<input type="checkbox" name="unchecked_box" value="1"/>
			<input type="hidden" name="h" value="v"/>
		</form>
	</body></html>`)

	form, err := resp.FindForm("form")
	if err != nil {
		t.Fatal(err)
	}
	if form.Values.Get("checked_box") != "1" {
		t.Error("the checked checkbox should be submitted")
	}
	if form.Values.Has("unchecked_box") {
		t.Error("the unchecked checkbox should not be submitted")
	}
}
