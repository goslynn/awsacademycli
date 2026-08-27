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
			// LTI 1.1: form firmado con OAuth1 y un script que lo dispara.
			name: "lanzamiento LTI 1.1",
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
			// LTI 1.3: el id_token viaja igual, en un form auto-enviado.
			name: "lanzamiento LTI 1.3 con body onload",
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
			// Un form sin disparador es un form que el usuario debe completar.
			// Reenviarlo vacío rompería el login, así que no se toca.
			name: "form de login no se auto-envía",
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
			name:     "página sin formularios",
			body:     `<html><body><h1>Learner Lab</h1></body></html>`,
			wantForm: false,
		},
		{
			// El disparador existe pero apunta a un form; con varios,
			// gana el que lleva campos ocultos.
			name: "elige el form con campos ocultos",
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
					t.Fatalf("esperaba nil, obtuve action=%q", form.Action)
				}
				return
			}
			if form == nil {
				t.Fatal("esperaba un formulario, obtuve nil")
			}
			if form.Action != tt.wantAction {
				t.Errorf("Action = %q, esperaba %q", form.Action, tt.wantAction)
			}
			if form.Method != tt.wantMethod {
				t.Errorf("Method = %q, esperaba %q", form.Method, tt.wantMethod)
			}
			if got := form.Values.Get(tt.wantField); got != tt.wantValue {
				t.Errorf("Values[%q] = %q, esperaba %q", tt.wantField, got, tt.wantValue)
			}
		})
	}
}

func TestParseFormSkipsUncheckedBoxes(t *testing.T) {
	resp := mkResp(t, `<html><body>
		<form action="/x" method="POST">
			<input type="checkbox" name="marcado" value="1" checked/>
			<input type="checkbox" name="sinmarcar" value="1"/>
			<input type="hidden" name="h" value="v"/>
		</form>
	</body></html>`)

	form, err := resp.FindForm("form")
	if err != nil {
		t.Fatal(err)
	}
	if form.Values.Get("marcado") != "1" {
		t.Error("el checkbox marcado debería enviarse")
	}
	if form.Values.Has("sinmarcar") {
		t.Error("el checkbox sin marcar no debería enviarse")
	}
}
