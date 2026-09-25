// Package api serves nextupdate's JSON API.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/auth"
	"github.com/jordibrouwer/nextupdate/internal/changelog"
	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/push"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

const cookieName = "nu_session"

type Engine interface {
	Check(ctx context.Context) ([]store.Available, error)
	Update(ctx context.Context, name string) (store.History, error)
	Rollback(ctx context.Context, name string) (store.History, error)
	Containers(ctx context.Context) ([]discovery.Container, error)
}

type Deps struct {
	Store     *store.Store
	Auth      *auth.Service
	Engine    Engine
	Changelog changelog.Source
	Mappings  *changelog.Mappings
	Push      *push.Sender
	BaseURL   string
	Client    *http.Client
	Log       *log.Logger
	Version   string
	BaseCtx   context.Context // parent of background jobs; cancelled on shutdown
}

type Server struct {
	d    Deps
	mux  *http.ServeMux
	jobs Jobs
}

func New(d Deps) *Server {
	if d.BaseCtx == nil {
		d.BaseCtx = context.Background()
	}
	s := &Server{d: d, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("POST /api/setup", s.csrf(s.handleSetup))
	s.mux.HandleFunc("POST /api/login", s.csrf(s.handleLogin))
	s.mux.HandleFunc("POST /api/logout", s.csrf(s.handleLogout))
	s.mux.HandleFunc("GET /api/me", s.protected(s.handleMe))
	s.registerUpdates()
	s.registerNotifiers()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	s.mux.ServeHTTP(w, r)
}

// Wait blocks until every background job has finished.
func (s *Server) Wait() { s.jobs.Wait() }

func (s *Server) logf(format string, a ...any) {
	l := s.d.Log
	if l == nil {
		l = log.Default()
	}
	l.Printf(format, a...)
}

// csrf rejects state-changing requests that lack the custom header. A
// cross-site form or fetch cannot set it without a CORS preflight.
func (s *Server) csrf(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get("X-NextUpdate") == "" {
			writeError(w, http.StatusForbidden, "The X-NextUpdate header is missing.")
			return
		}
		h(w, r)
	}
}

type ctxKey int

const userKey ctxKey = 0

// protected requires a signed-in user (and the CSRF header on writes).
func (s *Server) protected(h http.HandlerFunc) http.HandlerFunc {
	return s.csrf(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "Sign in first.")
			return
		}
		uid, err := s.d.Auth.Authenticate(c.Value)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "Sign in first.")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), userKey, uid)))
	})
}

func userID(r *http.Request) int64 {
	id, _ := r.Context().Value(userKey).(int64)
	return id
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		return errors.New("The request body is not valid JSON.")
	}
	return nil
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func setSession(w http.ResponseWriter, r *http.Request, token string, ttl time.Duration) {
	secure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	c := &http.Cookie{Name: cookieName, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: secure}
	if token != "" {
		c.MaxAge = int(ttl.Seconds())
	} else {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	need, err := s.d.Auth.NeedsSetup()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the account state.")
		return
	}
	signedIn := false
	if c, err := r.Cookie(cookieName); err == nil {
		_, err := s.d.Auth.Authenticate(c.Value)
		signedIn = err == nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": s.d.Version, "setupNeeded": need, "signedIn": signedIn})
}

type credentials struct {
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := readJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch err := s.d.Auth.Setup(c.Name, c.Password); {
	case errors.Is(err, auth.ErrSetupDone):
		writeError(w, http.StatusConflict, "An admin account already exists. Sign in instead.")
		return
	case errors.Is(err, auth.ErrWeakPassword):
		writeError(w, http.StatusBadRequest, "Use a password of at least 10 characters.")
		return
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	token, err := s.d.Auth.Login(c.Name, c.Password, remoteIP(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "The account was created, but signing in failed.")
		return
	}
	setSession(w, r, token, 30*24*time.Hour)
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := readJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	token, err := s.d.Auth.Login(c.Name, c.Password, remoteIP(r))
	switch {
	case errors.Is(err, auth.ErrLocked):
		writeError(w, http.StatusTooManyRequests, "Too many failed attempts. Try again in a few minutes.")
		return
	case errors.Is(err, auth.ErrBadLogin):
		writeError(w, http.StatusUnauthorized, "That name and password don't match.")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "Signing in failed.")
		return
	}
	setSession(w, r, token, 30*24*time.Hour)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		_ = s.d.Auth.Logout(c.Value)
	}
	setSession(w, r, "", 0)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	u, err := s.d.Store.GetUserByID(userID(r))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "Sign in first.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": u.Name})
}
