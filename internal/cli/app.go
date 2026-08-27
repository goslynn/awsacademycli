// Package cli implements the tool's commands.
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

// App shares the configuration, the session and the clients across commands.
type App struct {
	cfg    *config.Config
	http   *httpx.Client
	canvas *canvas.Client

	// sessionDirty marks that there are new cookies worth saving.
	sessionDirty bool
}

// newApp prepares the application and restores whatever session is on disk.
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

	// Restoring the cookies is what avoids logging in again on every command.
	if sess, err := state.LoadSession(); err == nil {
		client.ImportCookies(sess.Cookies)
	}

	return &App{
		cfg:    cfg,
		http:   client,
		canvas: canvas.New(client, cfg.CanvasBaseURL),
	}, nil
}

// saveSession persists the current cookies if they changed.
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

// EnsureSession returns a live session, reauthenticating only when needed.
//
// The saved session is tried first, because Canvas issues a persistent cookie
// when remember_me is requested and it usually lasts weeks: that way the
// password almost never comes into play.
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

// EnsureDiscovery returns where the lab lives, discovering it if needed.
func (a *App) EnsureDiscovery(ctx context.Context) (*state.Discovery, error) {
	if disc, err := state.LoadDiscovery(); err == nil && disc.LaunchURL != "" {
		// If the user pinned a course by hand and the cache points elsewhere,
		// the user wins.
		if a.cfg.CourseID == "" || a.cfg.CourseID == disc.CourseID {
			return disc, nil
		}
	}
	return a.Discover(ctx)
}

// Discover locates the course and the lab item from scratch.
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

// pickCourse chooses the course: the one the user pinned, or the only active one.
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
		return nil, errors.New("there are no active courses in your AWS Academy account")
	case 1:
		return &courses[0], nil
	}

	// With several courses we do not guess: choosing wrong would mean bringing
	// up the wrong lab. But the message has to carry the command that settles
	// it, not send the user off to edit a file blindly.
	msg := fmt.Sprintf("you have %d active courses and none pinned:\n\n", len(courses))
	for _, c := range courses {
		msg += fmt.Sprintf("  %-8d %s\n", c.ID, c.Label())
	}
	msg += fmt.Sprintf("\nPick one with:\n  awsacademy courses --use %d", courses[suggestCourse(courses)].ID)
	return nil, errors.New(msg)
}

// OpenLab goes through the LTI launch and returns the lab ready to operate.
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
		// An archived course leaves a cached URL that no longer leads
		// anywhere. It is worth retrying once from scratch before giving up.
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

	// The endpoints the page itself reveals beat whatever we have cached, and
	// both beat the compiled-in guesses.
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

// selfCommand returns this binary's invocation for credential_process.
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
