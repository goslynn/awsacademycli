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

// fakeCanvas imita lo justo de Canvas: la cookie CSRF, el POST de login y la
// API que consultamos.
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
			// Canvas entrega el token percent-encoded en una cookie.
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
		t.Errorf("usuario inesperado: %+v", user)
	}
	// El token debe llegar decodificado: Canvas lo guarda percent-encoded
	// pero lo espera en claro en el formulario.
	if fake.gotToken != "abc+def=" {
		t.Errorf("authenticity_token = %q, esperaba %q", fake.gotToken, "abc+def=")
	}
	// remember_me es lo que hace que la sesión sobreviva días sin la contraseña.
	if fake.gotRememberMe != "1" {
		t.Errorf("remember_me = %q, esperaba \"1\"", fake.gotRememberMe)
	}
}

func TestLoginBadPassword(t *testing.T) {
	fake := &fakeCanvas{email: "ada@example.com", password: "s3cret"}
	c, _ := newTestClient(t, fake.handler())

	_, err := c.Login(context.Background(), "ada@example.com", "equivocada")
	if !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("esperaba ErrInvalidCredentials, obtuve %v", err)
	}
	// El motivo que muestra Canvas debe llegar al usuario.
	if got := err.Error(); !strings.Contains(got, "verify your username or password") {
		t.Errorf("el error no incluye el mensaje de Canvas: %q", got)
	}
}

func TestWhoamiWithoutSession(t *testing.T) {
	fake := &fakeCanvas{email: "ada@example.com", password: "s3cret"}
	c, _ := newTestClient(t, fake.handler())

	if _, err := c.Whoami(context.Background()); !errors.Is(err, ErrNoSession) {
		t.Fatalf("esperaba ErrNoSession, obtuve %v", err)
	}
}

func TestFindLabItem(t *testing.T) {
	// Los módulos reales de un curso de AWS Academy: siete herramientas
	// externas, seis de ellas material *sobre* el laboratorio, y una sola que
	// lo lanza. El título no las distingue —casi todas dicen "Laboratorio de
	// aprendizaje"— pero el proveedor LTI sí.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/courses/182613/modules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
		  {"id":1,"name":"Bienvenida","items":[
		    {"id":100,"title":"Encuesta previa al curso","type":"Quiz"},
		    {"id":101,"title":"Guía del estudiante del Laboratorio de aprendizaje de AWS Academy",
		     "type":"ExternalTool","external_url":"https://emergingtalent.contentcontroller.com/api/launch/lti/"}
		  ]},
		  {"id":2,"name":"Cumplimiento","items":[
		    {"id":102,"title":"Cómo usar de manera eficaz el Laboratorio de aprendizaje de Academy",
		     "type":"ExternalTool","external_url":"https://emergingtalent.contentcontroller.com/api/launch/lti/"}
		  ]},
		  {"id":3,"name":"Laboratorio de aprendizaje","items":[
		    {"id":18010855,"title":"Iniciar el Laboratorio de aprendizaje de AWS Academy  ",
		     "type":"ExternalTool","external_url":"https://labs.vocareum.com/lti/launch.php?assignment=2902317",
		     "html_url":"https://canvas.test/courses/182613/modules/items/18010855"}
		  ]},
		  {"id":4,"name":"Recursos","items":[
		    {"id":103,"title":"Demostración: cómo acceder al Laboratorio de aprendizaje",
		     "type":"ExternalTool","external_url":"https://emergingtalent.contentcontroller.com/api/launch/lti/"},
		    {"id":104,"title":"Demostración: cómo iniciar servicios a través de la Consola de AWS",
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
		t.Errorf("ItemID = %q, esperaba 18010855 (el que apunta a Vocareum)", item.ItemID)
	}
	// El título de Canvas trae espacios de sobra; no deberían llegar al usuario.
	if item.Title != "Iniciar el Laboratorio de aprendizaje de AWS Academy" {
		t.Errorf("Title = %q (sin recortar)", item.Title)
	}
	if item.LaunchURL != "https://canvas.test/courses/182613/modules/items/18010855" {
		t.Errorf("LaunchURL inesperada: %q", item.LaunchURL)
	}
}

func TestFindLabItemPrefersProviderOverTitle(t *testing.T) {
	// Aunque otro ítem tenga un título más prometedor, manda el proveedor:
	// el laboratorio vive en Vocareum, no en el servidor de contenidos.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/courses/1/modules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":1,"name":"M","items":[
		  {"id":50,"title":"Iniciar el Learner Lab","type":"ExternalTool",
		   "external_url":"https://emergingtalent.contentcontroller.com/api/launch/lti/"},
		  {"id":51,"title":"Entorno práctico","type":"ExternalTool",
		   "external_url":"https://labs.vocareum.com/lti/launch.php"}
		]}]`))
	})
	c, _ := newTestClient(t, mux)

	item, err := c.FindLabItem(context.Background(), Course{ID: 1, Name: "Curso"})
	if err != nil {
		t.Fatalf("FindLabItem: %v", err)
	}
	if item.ItemID != "51" {
		t.Errorf("ItemID = %q, esperaba 51 (el de Vocareum)", item.ItemID)
	}
}

func TestFindLabItemRejectsOnlyGuides(t *testing.T) {
	// Si no hay ningún candidato con señal positiva, hay que fallar diciendo
	// qué se vio, no lanzar una guía al azar.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/courses/1/modules", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"id":1,"name":"M","items":[
		  {"id":50,"title":"Guía del estudiante","type":"ExternalTool","external_url":"https://x.example.com/"},
		  {"id":51,"title":"Demostración: cómo empezar","type":"ExternalTool","external_url":"https://x.example.com/"}
		]}]`))
	})
	c, _ := newTestClient(t, mux)

	_, err := c.FindLabItem(context.Background(), Course{ID: 1, Name: "Curso"})
	if err == nil {
		t.Fatal("esperaba un error: ninguno de los ítems lanza el laboratorio")
	}
	if !strings.Contains(err.Error(), "Guía del estudiante") {
		t.Errorf("el error debería listar los candidatos: %q", err)
	}
}
