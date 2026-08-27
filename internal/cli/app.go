// Package cli implementa los comandos de la herramienta.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/goslynn/awsacademycli/internal/canvas"
	"github.com/goslynn/awsacademycli/internal/config"
	"github.com/goslynn/awsacademycli/internal/httpx"
	"github.com/goslynn/awsacademycli/internal/state"
	"github.com/goslynn/awsacademycli/internal/vocareum"
)

// App comparte entre comandos la configuración, la sesión y los clientes.
type App struct {
	cfg    *config.Config
	http   *httpx.Client
	canvas *canvas.Client

	// sessionDirty marca que hay cookies nuevas que conviene guardar.
	sessionDirty bool
}

// newApp prepara la aplicación y restaura la sesión que hubiera en disco.
func newApp(debugHTTP bool) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	client, err := httpx.New()
	if err != nil {
		return nil, err
	}
	if debugHTTP {
		client.Debug = func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "  http: "+format+"\n", args...)
		}
	}

	// Restaurar las cookies es lo que evita volver a loguearse en cada comando.
	if sess, err := state.LoadSession(); err == nil {
		client.ImportCookies(sess.Cookies)
	}

	return &App{
		cfg:    cfg,
		http:   client,
		canvas: canvas.New(client, cfg.CanvasBaseURL),
	}, nil
}

// saveSession persiste las cookies actuales si cambiaron.
func (a *App) saveSession(user *canvas.User) error {
	if !a.sessionDirty {
		return nil
	}
	sess := &state.Session{
		Cookies: a.http.ExportCookies(),
		SavedAt: time.Now(),
	}
	if user != nil {
		sess.UserID, sess.UserName = user.ID, user.Name
	}
	a.sessionDirty = false
	return sess.Save()
}

// EnsureSession devuelve una sesión viva, reautenticando solo si hace falta.
//
// Primero se prueba la sesión guardada, porque Canvas emite una cookie
// persistente al pedir remember_me y suele durar semanas: así la contraseña
// casi nunca entra en juego.
func (a *App) EnsureSession(ctx context.Context) (*canvas.User, error) {
	if user, err := a.canvas.Whoami(ctx); err == nil {
		return user, nil
	} else if !errors.Is(err, canvas.ErrNoSession) {
		return nil, err
	}

	password, err := a.cfg.ResolvePassword()
	if err != nil {
		return nil, err
	}
	user, err := a.canvas.Login(ctx, a.cfg.Email, password)
	if err != nil {
		return nil, err
	}
	a.sessionDirty = true
	return user, a.saveSession(user)
}

// EnsureDiscovery devuelve dónde vive el laboratorio, descubriéndolo si hace falta.
func (a *App) EnsureDiscovery(ctx context.Context) (*state.Discovery, error) {
	if disc, err := state.LoadDiscovery(); err == nil && disc.LaunchURL != "" {
		// Si el usuario fijó un curso a mano y el caché apunta a otro, manda él.
		if a.cfg.CourseID == "" || a.cfg.CourseID == disc.CourseID {
			return disc, nil
		}
	}
	return a.Discover(ctx)
}

// Discover localiza el curso y el ítem del laboratorio desde cero.
func (a *App) Discover(ctx context.Context) (*state.Discovery, error) {
	if _, err := a.EnsureSession(ctx); err != nil {
		return nil, err
	}

	course, err := a.pickCourse(ctx)
	if err != nil {
		return nil, err
	}
	item, err := a.canvas.FindLabItem(ctx, *course)
	if err != nil {
		return nil, err
	}

	disc := &state.Discovery{
		CourseID:     item.CourseID,
		CourseName:   item.CourseName,
		ModuleItemID: item.ItemID,
		ItemTitle:    item.Title,
		LaunchURL:    item.LaunchURL,
		DiscoveredAt: time.Now(),
	}
	return disc, disc.Save()
}

// pickCourse elige el curso: el que el usuario fijó, o el único activo.
func (a *App) pickCourse(ctx context.Context) (*canvas.Course, error) {
	if a.cfg.CourseID != "" {
		return a.canvas.CourseByID(ctx, a.cfg.CourseID)
	}

	courses, err := a.canvas.Courses(ctx)
	if err != nil {
		return nil, err
	}
	switch len(courses) {
	case 0:
		return nil, errors.New("no hay cursos activos en tu cuenta de AWS Academy")
	case 1:
		return &courses[0], nil
	}

	// Con varios cursos no adivinamos: elegir mal significaría levantar el
	// laboratorio equivocado. Pero el mensaje tiene que traer el comando que
	// lo resuelve, no mandar a editar un fichero a ciegas.
	msg := fmt.Sprintf("tenés %d cursos activos y ninguno fijado:\n\n", len(courses))
	for _, c := range courses {
		msg += fmt.Sprintf("  %-8d %s\n", c.ID, c.Label())
	}
	msg += fmt.Sprintf("\nElegí uno con:\n  awsacademy courses --use %d", courses[suggestCourse(courses)].ID)
	return nil, errors.New(msg)
}

// OpenLab atraviesa el lanzamiento LTI y devuelve el laboratorio listo para operar.
func (a *App) OpenLab(ctx context.Context) (*vocareum.Lab, *state.Discovery, error) {
	if _, err := a.EnsureSession(ctx); err != nil {
		return nil, nil, err
	}
	disc, err := a.EnsureDiscovery(ctx)
	if err != nil {
		return nil, nil, err
	}

	sess, err := vocareum.Launch(ctx, a.http, disc.LaunchURL)
	if err != nil {
		// Un curso archivado deja una URL cacheada que ya no lleva a ningún
		// lado. Vale la pena reintentar una vez desde cero antes de rendirse.
		fresh, err2 := a.Discover(ctx)
		if err2 != nil {
			return nil, nil, err
		}
		disc = fresh
		if sess, err = vocareum.Launch(ctx, a.http, disc.LaunchURL); err != nil {
			return nil, nil, err
		}
	}
	a.sessionDirty = true

	// Los endpoints que la propia página revela le ganan a lo que tengamos
	// cacheado, y ambos le ganan a las conjeturas compiladas.
	endpoints := toVocareumEndpoints(disc.Endpoints).
		Merge(vocareum.DetectEndpoints(sess.Page()))

	disc.VocareumBase = sess.Base()
	disc.Endpoints = fromVocareumEndpoints(endpoints)
	if err := disc.Save(); err != nil {
		return nil, nil, err
	}

	return vocareum.NewLab(sess, endpoints), disc, a.saveSession(nil)
}

func toVocareumEndpoints(e state.Endpoints) vocareum.Endpoints {
	return vocareum.Endpoints{Status: e.Status, Start: e.Start, Stop: e.Stop, Credentials: e.Credentials}
}

func fromVocareumEndpoints(e vocareum.Endpoints) state.Endpoints {
	return state.Endpoints{Status: e.Status, Start: e.Start, Stop: e.Stop, Credentials: e.Credentials}
}

// selfCommand devuelve la invocación de este binario para credential_process.
func selfCommand() string {
	exe, err := os.Executable()
	if err != nil {
		return "awsacademy creds"
	}
	if strings.ContainsAny(exe, " \t") {
		return strconv.Quote(exe) + " creds"
	}
	return exe + " creds"
}
