package canvas

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goslynn/awsacademycli/internal/httpx"
)

// fakeCanvas mimics just enough of Canvas: the CSRF cookie, the login POST and
// the API we query.
type fakeCanvas struct {
	email, password string
	loggedIn        bool
	gotToken        string
	gotRememberMe   string
}

func (f *fakeCanvas) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/login/canvas", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			// Canvas delivers the token percent-encoded in a cookie.
			http.SetCookie(w, &http.Cookie{Name: "_csrf_token", Value: "abc%2Bdef%3D", Path: "/"})
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<html><body><div id="new_login_data"></div></body></html>`))
			return
		}
		r.ParseForm()
		f.gotToken = r.Form.Get("authenticity_token")
		f.gotRememberMe = r.Form.Get("pseudonym_session[remember_me]")

		if r.Form.Get("pseudonym_session[unique_id]") == f.email &&
			r.Form.Get("pseudonym_session[password]") == f.password {
			f.loggedIn = true
			http.Redirect(w, r, "/?login_success=1", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body>
			<div class="ic-flash-error"><span>Please verify your username or password and try again.</span></div>
		</body></html>`))
	})

	mux.HandleFunc("/api/v1/users/self", func(w http.ResponseWriter, r *http.Request) {
		if !f.loggedIn {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"status":"unauthenticated"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":4242,"name":"Ada Lovelace","login_id":"ada@example.com"}`))
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body>Dashboard</body></html>`))
	})
	return mux
}

func newTestClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	hc, err := httpx.New()
	if err != nil {
		t.Fatal(err)
	}
	return New(hc, srv.URL), srv
}

func TestLoginSuccess(t *testing.T) {
	fake := &fakeCanvas{email: "ada@example.com", password: "s3cret"}
	c, _ := newTestClient(t, fake.handler())

	user, err := c.Login(context.Background(), "ada@example.com", "s3cret")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if user.ID != 4242 || user.Name != "Ada Lovelace" {
		t.Errorf("unexpected user: %+v", user)
	}
	// The token must arrive decoded: Canvas stores it percent-encoded but
	// expects it in the clear in the form.
	if fake.gotToken != "abc+def=" {
		t.Errorf("authenticity_token = %q, expected %q", fake.gotToken, "abc+def=")
	}
	// remember_me is what makes the session survive days without the password.
	if fake.gotRememberMe != "1" {
		t.Errorf("remember_me = %q, expected \"1\"", fake.gotRememberMe)
	}
}

func TestLoginBadPassword(t *testing.T) {
	fake := &fakeCanvas{email: "ada@example.com", password: "s3cret"}
	c, _ := newTestClient(t, fake.handler())

	_, err := c.Login(context.Background(), "ada@example.com", "wrong")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("expected ErrInvalidCredentials, got %v", err)
	}
	// The reason Canvas shows must reach the user.
	if got := err.Error(); !strings.Contains(got, "verify your username or password") {
		t.Errorf("the error does not include the Canvas message: %q", got)
	}
}

func TestWhoamiWithoutSession(t *testing.T) {
	fake := &fakeCanvas{email: "ada@example.com", password: "s3cret"}
	c, _ := newTestClient(t, fake.handler())

	if _, err := c.Whoami(context.Background()); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expected ErrNoSession, got %v", err)
	}
}

func TestFindLabItem(t *testing.T) {
	// The real modules of an AWS Academy course: seven external tools, six of
	// them material *about* the lab, and a single one that launches it. The
	// title does not tell them apart — nearly all of them say "learner lab" —
	// but the LTI provider does.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/courses/182613/modules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
		  {"id":1,"name":"Welcome","items":[
		    {"id":100,"title":"Pre-course survey","type":"Quiz"},
		    {"id":101,"title":"AWS Academy Learner Lab student guide",
		     "type":"ExternalTool","external_url":"https://emergingtalent.contentcontroller.com/api/launch/lti/"}
		  ]},
		  {"id":2,"name":"Compliance","items":[
		    {"id":102,"title":"How to make effective use of the Academy Learner Lab",
		     "type":"ExternalTool","external_url":"https://emergingtalent.contentcontroller.com/api/launch/lti/"}
		  ]},
		  {"id":3,"name":"Learner Lab","items":[
		    {"id":18010855,"title":"Start the AWS Academy Learner Lab  ",
		     "type":"ExternalTool","external_url":"https://labs.vocareum.com/lti/launch.php?assignment=2902317",
		     "html_url":"https://canvas.test/courses/182613/modules/items/18010855"}
		  ]},
		  {"id":4,"name":"Resources","items":[
		    {"id":103,"title":"Demonstration: how to access the Learner Lab",
		     "type":"ExternalTool","external_url":"https://emergingtalent.contentcontroller.com/api/launch/lti/"},
		    {"id":104,"title":"Demonstration: how to launch services through the AWS Console",
		     "type":"ExternalTool","external_url":"https://emergingtalent.contentcontroller.com/api/launch/lti/"},
		    {"id":105,"title":"FAQ: free AWS Skill Builder subscription",
		     "type":"ExternalTool","external_url":"https://emergingtalent.contentcontroller.com/api/launch/lti/"}
		  ]}
		]`))
	})
	c, _ := newTestClient(t, mux)

	item, err := c.FindLabItem(context.Background(), Course{ID: 182613, Name: "AWS Academy Learner Lab"})
	if err != nil {
		t.Fatalf("FindLabItem: %v", err)
	}
	if item.ItemID != "18010855" {
		t.Errorf("ItemID = %q, expected 18010855 (the one pointing at Vocareum)", item.ItemID)
	}
	// The Canvas title carries spare whitespace; it should not reach the user.
	if item.Title != "Start the AWS Academy Learner Lab" {
		t.Errorf("Title = %q (not trimmed)", item.Title)
	}
	if item.LaunchURL != "https://canvas.test/courses/182613/modules/items/18010855" {
		t.Errorf("unexpected LaunchURL: %q", item.LaunchURL)
	}
}

func TestFindLabItemHandlesLocalisedTitles(t *testing.T) {
	// Canvas serves item titles in the account's own language, so the scoring
	// has to cope with titles that are not in English. This is the Spanish
	// payload as the real service returns it, and it is why the title patterns
	// in discover.go keep their Spanish alternatives.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/courses/182613/modules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
		  {"id":1,"name":"Bienvenida","items":[
		    {"id":101,"title":"Guía del estudiante del Laboratorio de aprendizaje de AWS Academy",
		     "type":"ExternalTool","external_url":"https://emergingtalent.contentcontroller.com/api/launch/lti/"}
		  ]},
		  {"id":2,"name":"Laboratorio de aprendizaje","items":[
		    {"id":18010855,"title":"Iniciar el Laboratorio de aprendizaje de AWS Academy  ",
		     "type":"ExternalTool","external_url":"https://labs.vocareum.com/lti/launch.php?assignment=2902317",
		     "html_url":"https://canvas.test/courses/182613/modules/items/18010855"}
		  ]},
		  {"id":3,"name":"Recursos","items":[
		    {"id":103,"title":"Demostración: cómo acceder al Laboratorio de aprendizaje",
		     "type":"ExternalTool","external_url":"https://emergingtalent.contentcontroller.com/api/launch/lti/"},
		    {"id":105,"title":"Preguntas frecuentes: suscripción gratuita a AWS Skill Builder",
		     "type":"ExternalTool","external_url":"https://emergingtalent.contentcontroller.com/api/launch/lti/"}
		  ]}
		]`))
	})
	c, _ := newTestClient(t, mux)

	item, err := c.FindLabItem(context.Background(), Course{ID: 182613, Name: "AWS Academy Learner Lab"})
	if err != nil {
		t.Fatalf("FindLabItem: %v", err)
	}
	if item.ItemID != "18010855" {
		t.Errorf("ItemID = %q, expected 18010855 (the one pointing at Vocareum)", item.ItemID)
	}
}

func TestFindLabItemPrefersProviderOverTitle(t *testing.T) {
	// Even if another item has a more promising title, the provider rules: the
	// lab lives on Vocareum, not on the content server.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/courses/1/modules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":1,"name":"M","items":[
		  {"id":50,"title":"Start the Learner Lab","type":"ExternalTool",
		   "external_url":"https://emergingtalent.contentcontroller.com/api/launch/lti/"},
		  {"id":51,"title":"Hands-on environment","type":"ExternalTool",
		   "external_url":"https://labs.vocareum.com/lti/launch.php"}
		]}]`))
	})
	c, _ := newTestClient(t, mux)

	item, err := c.FindLabItem(context.Background(), Course{ID: 1, Name: "Course"})
	if err != nil {
		t.Fatalf("FindLabItem: %v", err)
	}
	if item.ItemID != "51" {
		t.Errorf("ItemID = %q, expected 51 (the Vocareum one)", item.ItemID)
	}
}

func TestFindLabItemRejectsOnlyGuides(t *testing.T) {
	// If no candidate carries a positive signal, we have to fail saying what we
	// saw, not launch a guide at random.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/courses/1/modules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":1,"name":"M","items":[
		  {"id":50,"title":"Student guide","type":"ExternalTool","external_url":"https://x.example.com/"},
		  {"id":51,"title":"Demonstration: how to get started","type":"ExternalTool","external_url":"https://x.example.com/"}
		]}]`))
	})
	c, _ := newTestClient(t, mux)

	_, err := c.FindLabItem(context.Background(), Course{ID: 1, Name: "Course"})
	if err == nil {
		t.Fatal("expected an error: none of the items launches the lab")
	}
	if !strings.Contains(err.Error(), "Student guide") {
		t.Errorf("the error should list the candidates: %q", err)
	}
}
