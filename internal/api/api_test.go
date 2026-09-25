package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/jordibrouwer/nextupdate/internal/auth"
	"github.com/jordibrouwer/nextupdate/internal/changelog"
	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
)

var errFake = errors.New("docker unreachable")

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

type fakeEngine struct {
	mu         sync.Mutex
	checks     int
	updates    []string
	rollbacks  []string
	containers []discovery.Container
	avail      []store.Available
	err        error
	block      chan struct{} // when set, Update waits for it to be closed
}

func (f *fakeEngine) Check(ctx context.Context) ([]store.Available, error) {
	f.mu.Lock()
	f.checks++
	f.mu.Unlock()
	return f.avail, f.err
}
func (f *fakeEngine) Update(ctx context.Context, name string) (store.History, error) {
	if f.block != nil {
		<-f.block
	}
	f.mu.Lock()
	f.updates = append(f.updates, name)
	f.mu.Unlock()
	return store.History{Container: name, Outcome: updater.OutcomeOK}, f.err
}
func (f *fakeEngine) Rollback(ctx context.Context, name string) (store.History, error) {
	f.mu.Lock()
	f.rollbacks = append(f.rollbacks, name)
	f.mu.Unlock()
	return store.History{Container: name, Outcome: updater.OutcomeOK}, f.err
}
func (f *fakeEngine) Containers(ctx context.Context) ([]discovery.Container, error) {
	return f.containers, nil
}

type fakeChangelog struct{ releases []changelog.Release }

func (c fakeChangelog) Releases(ctx context.Context, repo string) ([]changelog.Release, error) {
	return c.releases, nil
}

type harness struct {
	t      *testing.T
	srv    *Server
	st     *store.Store
	eng    *fakeEngine
	cookie *http.Cookie
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	clock := time.Now
	eng := &fakeEngine{}
	srv := New(Deps{
		Store:     st,
		Auth:      &auth.Service{Store: st, Cost: bcrypt.MinCost, Limiter: auth.NewLimiter(5, 15*time.Minute, clock)},
		Engine:    eng,
		Changelog: fakeChangelog{},
		BaseURL:   "https://nu.example",
		Version:   "test",
		BaseCtx:   context.Background(),
	})
	return &harness{t: t, srv: srv, st: st, eng: eng}
}

// do sends a request; a body value is sent as JSON. Non-GET requests carry
// the CSRF header and, when signed in, the session cookie.
func (h *harness) do(method, path string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.RemoteAddr = "1.2.3.4:5555"
	if method != http.MethodGet {
		req.Header.Set("X-NextUpdate", "1")
	}
	if h.cookie != nil {
		req.AddCookie(h.cookie)
	}
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	return rec
}

func (h *harness) signIn() {
	h.t.Helper()
	rec := h.do("POST", "/api/setup", map[string]string{"name": "jordi", "password": "long enough password"})
	if rec.Code != http.StatusCreated {
		h.t.Fatalf("setup: %d %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == "nu_session" {
			h.cookie = c
		}
	}
	if h.cookie == nil {
		h.t.Fatal("setup did not set a session cookie")
	}
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestStatusBeforeAndAfterSetup(t *testing.T) {
	h := newHarness(t)
	st := decode[map[string]any](t, h.do("GET", "/api/status", nil))
	if st["setupNeeded"] != true || st["signedIn"] != false || st["version"] != "test" {
		t.Fatalf("status %v", st)
	}
	h.signIn()
	st = decode[map[string]any](t, h.do("GET", "/api/status", nil))
	if st["setupNeeded"] != false || st["signedIn"] != true {
		t.Fatalf("status after setup %v", st)
	}
}

func TestSetupTwiceAndWeakPassword(t *testing.T) {
	h := newHarness(t)
	if rec := h.do("POST", "/api/setup", map[string]string{"name": "a", "password": "short"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("weak password: %d %s", rec.Code, rec.Body)
	}
	h.signIn()
	if rec := h.do("POST", "/api/setup", map[string]string{"name": "b", "password": "another long password"}); rec.Code != http.StatusConflict {
		t.Fatalf("second setup: %d", rec.Code)
	}
}

func TestLoginLogoutAndMe(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	me := decode[map[string]string](t, h.do("GET", "/api/me", nil))
	if me["name"] != "jordi" {
		t.Fatalf("me %v", me)
	}

	h.cookie = nil
	if rec := h.do("GET", "/api/me", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("me without cookie: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/login", map[string]string{"name": "jordi", "password": "nope nope nope"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login: %d", rec.Code)
	}
	rec := h.do("POST", "/api/login", map[string]string{"name": "jordi", "password": "long enough password"})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "nu_session" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie %+v", cookie)
	}
	h.cookie = cookie
	if rec := h.do("POST", "/api/logout", nil); rec.Code != http.StatusOK {
		t.Fatalf("logout: %d", rec.Code)
	}
	if rec := h.do("GET", "/api/me", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("session must be gone after logout: %d", rec.Code)
	}
}

func TestLoginLockout(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	h.cookie = nil
	for i := 0; i < 5; i++ {
		h.do("POST", "/api/login", map[string]string{"name": "jordi", "password": "wrong wrong wrong"})
	}
	if rec := h.do("POST", "/api/login", map[string]string{"name": "jordi", "password": "long enough password"}); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked login: %d", rec.Code)
	}
}

func TestCSRFHeaderRequired(t *testing.T) {
	h := newHarness(t)
	req := httptest.NewRequest("POST", "/api/setup", bytes.NewReader([]byte(`{"name":"a","password":"long enough password"}`)))
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without X-NextUpdate: %d", rec.Code)
	}
}

func TestProtectedRoutesNeedLogin(t *testing.T) {
	h := newHarness(t)
	for _, p := range [][2]string{{"GET", "/api/me"}} {
		if rec := h.do(p[0], p[1], nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without login: %d", p[0], p[1], rec.Code)
		}
	}
}

func TestJobsRunOncePerKey(t *testing.T) {
	var j Jobs
	release := make(chan struct{})
	started := make(chan struct{})
	if !j.Start(context.Background(), "app", "update", func(ctx context.Context) error {
		close(started)
		<-release
		return context.Canceled
	}) {
		t.Fatal("first start refused")
	}
	<-started
	if j.Start(context.Background(), "app", "update", func(ctx context.Context) error { return nil }) {
		t.Fatal("a second job for the same key must be refused")
	}
	if r := j.Running(); len(r) != 1 || r[0].Key != "app" || r[0].Kind != "update" {
		t.Fatalf("running %+v", r)
	}
	close(release)
	j.Wait()
	if len(j.Running()) != 0 {
		t.Fatal("job still running")
	}
	if f := j.Recent(); len(f) != 1 || f[0].Key != "app" || f[0].Error == "" {
		t.Fatalf("a failed job must be remembered: %+v", f)
	}
}
