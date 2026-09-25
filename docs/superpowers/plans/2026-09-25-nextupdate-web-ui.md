# nextupdate Web UI Implementation Plan (Plan 3b)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A web UI for nextupdate, served by the same binary: sign in, see all containers with the ones that have updates on top, read the release notes and the breaking reasons next to the list, update or roll back with one click, set the policy per container, manage notifiers, and turn on push notifications. Installable as a PWA.

**Architecture:** Plain HTML, CSS and ES modules, no build step, embedded in the Go binary with `go:embed` and served by a small `web` package next to the API. Hash routing (`#/updates/<name>`, `#/history`, `#/settings`). The layout is decided: list on the left, detail panel on the right; on a phone one pane at a time. A demo server (`cmd/uidemo`, a fake Docker engine over the real API and store) makes the UI testable without Docker; Playwright drives it, Node's test runner covers the pure helpers.

**Tech Stack:** HTML/CSS/JS (ES modules), Go `embed`, Node 26 test runner, Playwright 1.61.

**Spec:** `docs/superpowers/specs/2026-09-25-nextupdate-design.md`

**Builds on:** Plans 1, 2 and 3a on branch `dev` (last commit `2a9742c`). The API contract used here is defined in Plan 3a, Tasks 7-9.

## Global Constraints

- Content-Security-Policy on every web response: `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; manifest-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'`. So: no inline `<script>`, no inline `<style>`, no `style="..."` attributes in markup. Setting `element.style.x` from JS is allowed but avoid it; use classes.
- Never build DOM from strings. No `innerHTML`, `outerHTML`, `insertAdjacentHTML` or `document.write` anywhere. Release notes come from GitHub and are untrusted: they go through `markdown.js`, which builds DOM nodes and only makes links for `http://` and `https://`.
- Every `fetch` to a non-GET endpoint sends `X-NextUpdate: 1` (done once, in `api.js`).
- `localStorage` is only for the theme choice, and every access is wrapped in try/catch.
- Sentence case for all UI text, no terminal punctuation on labels and headings, active voice, no "successfully", no "please". Error and toast messages are plain English sentences.
- No emoji. Icons are inline SVG built with `createElementNS`.
- One accent-filled (primary) button per view; the rest are secondary. Avoid disabled buttons; explain and respond instead. The exception is a button that is working (busy).
- The keyboard legend uses `<kbd>` chips in a flex row; keys stay untranslated (`j`, `k`, `u`, `c`).
- Colors and spacing come from CSS custom properties defined once in `app.css`, with a dark set under `@media (prefers-color-scheme: dark)` and `[data-theme="dark"]`.
- Playwright: always `PW_WORKERS=2`; never the default of four. Write the run's output to a file and read the exit code, never pipe through `tail`. Run only the specs that cover the change. Do not run a full-suite regression as a final check.
- Tests use port 8099 and up (one per worker). Never 8080.
- Every task ends with a commit: short plain subject line, no `Co-Authored-By` trailer.
- Keep Bash calls under 30 s; run slow work in the background and wait with a Monitor.

## File Structure

```
package.json, package-lock.json       dev dependencies and scripts
playwright.config.js
.gitignore                            node_modules, test-results, playwright-report, e2e/.bin
e2e/fixtures.js                       per-worker demo server, reset per test, sign-in helper
e2e/global-setup.js                   builds the demo binary once
e2e/auth.spec.js  updates.spec.js  history.spec.js  settings.spec.js  mobile.spec.js  pwa.spec.js
web-test/format.test.mjs              node:test for format.js
web-test/markdown.test.mjs            node:test for markdown.js (parser)
cmd/uidemo/main.go                    demo server: fake engine, seeded store, sink, reset
tools/genicons/main.go                writes the PNG icons
internal/web/web.go                   embed + handler + Mount
internal/web/web_test.go
internal/web/static/index.html
internal/web/static/manifest.webmanifest
internal/web/static/sw.js
internal/web/static/icons/            icon.svg, icon-192.png, icon-512.png, icon-maskable-512.png, apple-touch-icon.png
internal/web/static/css/app.css
internal/web/static/js/theme.js       classic script, sets the theme before first paint
internal/web/static/js/app.js         boot, shell, router, header
internal/web/static/js/api.js  dom.js  icons.js  ui.js  format.js  markdown.js  jobs.js  push.js
internal/web/static/js/views/login.js  updates.js  history.js  settings.js
internal/api/api.go, updates.go, notifiers.go   (modify: lastCheck in /api/jobs, POST /api/push/test)
cmd/nextupdate/main.go                (modify: mount the web handler)
Dockerfile, .dockerignore, README.md  (modify)
```

---

### Task 1: Serve the UI, `lastCheck` and a push test endpoint

**Files:**
- Create: `internal/web/web.go`, `internal/web/static/index.html` (placeholder), `internal/web/static/js/app.js` (placeholder)
- Modify: `internal/api/updates.go`, `internal/api/notifiers.go`, `cmd/nextupdate/main.go`, `Dockerfile`, `.dockerignore`
- Test: `internal/web/web_test.go`, `internal/api/updates_test.go`, `internal/api/notifiers_test.go`

**Interfaces:**
- Produces:
  - `web.Handler() http.Handler`: serves the embedded `static/` files; `GET` and `HEAD` only; `/` serves `index.html`; strong `ETag` (first 16 hex characters of the SHA-256 of the content) and `Cache-Control: no-cache`; `304` on a matching `If-None-Match`; the CSP header above plus `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, `X-Frame-Options: DENY`; unknown paths `404`; directory paths `404`
  - `web.Mount(api http.Handler) http.Handler`: `/api/` goes to `api`, everything else to `Handler()`
  - `GET /api/jobs` gains `"lastCheck": "<RFC3339 or empty>"`
  - `POST /api/push/test` (signed in, CSRF header): sends `{"title":"nextupdate test","body":"Push notifications work on this device.","url":"<BaseURL>"}` to every subscription; `503` when push is not configured; `200 {"sent":N,"removed":M}` (a gone subscription is deleted and counted as removed)

- [ ] **Step 1: Write the failing tests**

`internal/web/web_test.go`:

```go
package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func get(t *testing.T, h http.Handler, method, path string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestIndexHasSecurityHeadersAndETag(t *testing.T) {
	rec := get(t, Handler(), "GET", "/", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<title>") {
		t.Fatalf("index: %d %q", rec.Code, rec.Body.String())
	}
	csp := rec.Header().Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "style-src 'self'", "object-src 'none'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP lacks %q: %s", want, csp)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP must not allow unsafe code: %s", csp)
	}
	h := rec.Header()
	if h.Get("Cache-Control") != "no-cache" || h.Get("X-Content-Type-Options") != "nosniff" || h.Get("X-Frame-Options") != "DENY" || h.Get("Referrer-Policy") != "same-origin" {
		t.Errorf("headers %v", h)
	}
	if !strings.HasPrefix(h.Get("Etag"), `"`) {
		t.Errorf("etag %q", h.Get("Etag"))
	}
	if ct := h.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content type %q", ct)
	}
}

func TestETagGivesNotModified(t *testing.T) {
	etag := get(t, Handler(), "GET", "/", nil).Header().Get("Etag")
	rec := get(t, Handler(), "GET", "/", map[string]string{"If-None-Match": etag})
	if rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("conditional get: %d body %q", rec.Code, rec.Body.String())
	}
}

func TestContentTypes(t *testing.T) {
	for path, want := range map[string]string{
		"/js/app.js": "text/javascript",
	} {
		rec := get(t, Handler(), "GET", path, nil)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), want) {
			t.Errorf("%s: %d %q", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}

func TestUnknownAndUnsafePaths(t *testing.T) {
	h := Handler()
	for _, p := range []string{"/nope.txt", "/js", "/js/", "/../go.mod", "/%2e%2e/go.mod", "/js/../../go.mod"} {
		if rec := get(t, h, "GET", p, nil); rec.Code != http.StatusNotFound && rec.Code != http.StatusMovedPermanently {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	if rec := get(t, h, "POST", "/", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rec.Code)
	}
}

func TestMountRoutesAPIAndStatic(t *testing.T) {
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("api:" + r.URL.Path)) })
	h := Mount(api)
	if rec := get(t, h, "GET", "/api/status", nil); rec.Body.String() != "api:/api/status" {
		t.Fatalf("api route: %q", rec.Body.String())
	}
	if rec := get(t, h, "GET", "/", nil); !strings.Contains(rec.Body.String(), "<title>") {
		t.Fatalf("static route: %q", rec.Body.String())
	}
}
```

Append to `internal/api/updates_test.go`:

```go
func TestJobsReportsLastCheck(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	h.st.SetSetting("last_check", "2026-09-25T18:00:00Z")
	got := decode[map[string]any](t, h.do("GET", "/api/jobs", nil))
	if got["lastCheck"] != "2026-09-25T18:00:00Z" {
		t.Fatalf("jobs %v", got)
	}
}
```

Append to `internal/api/notifiers_test.go` (the file already imports `push` and `store`; add `"crypto/ecdh"`, `"crypto/rand"`, `"encoding/base64"`):

```go
func testPushSub(t *testing.T, endpoint string) store.PushSub {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	rand.Read(authSecret)
	return store.PushSub{Endpoint: endpoint,
		P256dh: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(authSecret)}
}

func TestPushTest(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	if rec := h.do("POST", "/api/push/test", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("push not configured: %d", rec.Code)
	}
	keys, _ := push.EnsureKeys(h.st)
	h.srv.d.Push = &push.Sender{Keys: keys, Subject: "mailto:a@b.c"}
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) }))
	defer live.Close()
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(410) }))
	defer gone.Close()
	h.st.AddPushSub(testPushSub(t, live.URL+"/a"))
	h.st.AddPushSub(testPushSub(t, gone.URL+"/b"))

	got := decode[map[string]int](t, h.do("POST", "/api/push/test", nil))
	if got["sent"] != 1 || got["removed"] != 1 {
		t.Fatalf("result %v", got)
	}
	if left, _ := h.st.ListPushSubs(); len(left) != 1 {
		t.Fatalf("the gone subscription must be deleted: %+v", left)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/web/ ./internal/api/ 2>&1 | head -20`
Expected: FAIL (`internal/web` has no Go files, `lastCheck` missing, 404 for `/api/push/test`).

- [ ] **Step 3: Implement the web package**

Create the placeholders (they get replaced in later tasks):

`internal/web/static/index.html`:

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>nextupdate</title>
</head>
<body></body>
</html>
```

`internal/web/static/js/app.js`:

```js
export {};
```

`internal/web/web.go`:

```go
// Package web serves the embedded nextupdate UI.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

//go:embed static
var files embed.FS

const csp = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; " +
	"manifest-src 'self'; worker-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'"

func init() {
	// Go's built-in table lacks these on some systems.
	mime.AddExtensionType(".webmanifest", "application/manifest+json")
	mime.AddExtensionType(".js", "text/javascript; charset=utf-8")
	mime.AddExtensionType(".mjs", "text/javascript; charset=utf-8")
}

type asset struct {
	body []byte
	etag string
	ct   string
}

// Handler serves the embedded files.
func Handler() http.Handler {
	sub, err := fs.Sub(files, "static")
	if err != nil {
		panic(err)
	}
	assets := map[string]asset{}
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(sub, p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		ct := mime.TypeByExtension(path.Ext(p))
		if ct == "" {
			ct = http.DetectContentType(b)
		}
		assets["/"+p] = asset{body: b, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, ct: ct}
		return nil
	})
	if err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := r.URL.Path
		if p == "/" {
			p = "/index.html"
		}
		if clean := path.Clean(p); clean != p || strings.Contains(p, "..") {
			http.NotFound(w, r)
			return
		}
		a, ok := assets[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		h.Set("Content-Type", a.ct)
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", a.etag)
		if match := r.Header.Get("If-None-Match"); match == a.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if r.Method == http.MethodGet {
			w.Write(a.body)
		}
	})
}

// Mount serves the API under /api/ and the UI everywhere else.
func Mount(api http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", api)
	mux.Handle("/", Handler())
	return mux
}
```

- [ ] **Step 4: Implement the API additions**

In `internal/api/updates.go`, replace the body of `handleJobs`:

```go
func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	failed := s.jobs.Recent()
	if failed == nil {
		failed = []Failure{}
	}
	last, _ := s.d.Store.GetSetting("last_check")
	writeJSON(w, http.StatusOK, map[string]any{"running": s.jobs.Running(), "failed": failed, "lastCheck": last})
}
```

In `internal/api/notifiers.go`, register the route in `registerNotifiers`:

```go
	s.mux.HandleFunc("POST /api/push/test", s.protected(s.handlePushTest))
```

and add the handler (add `"context"`? not needed; add `"encoding/json"` and `"errors"` is already imported; add the `push` import):

```go
func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if s.d.Push == nil {
		writeError(w, http.StatusServiceUnavailable, "Push notifications are not set up on this server.")
		return
	}
	subs, err := s.d.Store.ListPushSubs()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the subscriptions.")
		return
	}
	payload, _ := json.Marshal(map[string]string{"title": "nextupdate test", "body": "Push notifications work on this device.", "url": s.d.BaseURL})
	sent, removed := 0, 0
	for _, sub := range subs {
		switch err := s.d.Push.Send(r.Context(), sub, payload); {
		case errors.Is(err, push.ErrGone):
			_ = s.d.Store.DeletePushSub(sub.Endpoint)
			removed++
		case err != nil:
			s.logf("api: test push: %v", err)
		default:
			sent++
		}
	}
	writeJSON(w, http.StatusOK, map[string]int{"sent": sent, "removed": removed})
}
```

- [ ] **Step 5: Mount the UI in `serve`**

In `cmd/nextupdate/main.go` add the import `"github.com/jordibrouwer/nextupdate/internal/web"` and change the HTTP server's handler:

```go
		httpServer := &http.Server{Addr: envOr("NEXTUPDATE_LISTEN", ":8099"), Handler: web.Mount(server), ReadHeaderTimeout: 10 * time.Second}
```

`Dockerfile` needs no change (the files are embedded). `.dockerignore`: append

```
node_modules
e2e
web-test
playwright-report
test-results
```

- [ ] **Step 6: Run to verify they pass**

Run: `gofmt -l . ; go vet ./... && go test -race -count=1 ./internal/web/ ./internal/api/`
Expected: `ok` for both.

- [ ] **Step 7: Falsify the CSP and the conditional GET**

Temporarily change `csp` to include `'unsafe-inline'` and make the `If-None-Match` comparison `false`; `TestIndexHasSecurityHeadersAndETag` and `TestETagGivesNotModified` must FAIL. Restore.

- [ ] **Step 8: Commit**

```bash
git add internal cmd .dockerignore
git commit -m "serve the embedded ui, lastCheck in jobs, push test endpoint"
```

---

### Task 2: Demo server, Playwright and Node test setup

**Files:**
- Create: `cmd/uidemo/main.go`, `package.json`, `playwright.config.js`, `.gitignore`, `e2e/fixtures.js`, `e2e/global-setup.js`, `e2e/pwa.spec.js` (smoke spec, extended in Task 9), `web-test/.keep`

**Interfaces:**
- Produces:
  - `go run ./cmd/uidemo -addr 127.0.0.1:8099`: the real API, store and embedded UI over a fake engine. `POST /__demo/reset` (204) rebuilds a fresh seeded state and signs everyone out. `POST /__demo/sink` records a request body, `GET /__demo/sink` returns the recorded bodies as a JSON array of strings (for notifier tests).
  - Seed data (the UI tests rely on these exact names):
    - `immich` (compose): update 1.98.0 to 2.0.0, breaking (`Major version change from 1.98.0 to 2.0.0.`), repo `immich-app/immich`, policy `notify`
    - `sonarr` (run): update 4.0.9 to 4.0.10, repo `Sonarr/Sonarr`, policy `auto`
    - `vaultwarden` (run): update 1.30.0 to 1.31.0, repo `dani-garcia/vaultwarden`, policy `notify`
    - `flaky` (run): update 2.3.0 to 2.3.1, policy `notify`; applying it always ends `rolled_back` with reason `verify: healthcheck unhealthy`
    - `jellyfin` (run): up to date; one earlier `ok` history entry (so a rollback is possible)
    - `postgres` (compose): up to date, image `postgres:16`
  - Release notes (fake changelog) per repo, in Markdown. The `immich-app/immich` v2.0.0 body contains the line `Database migration runs on first start.`, a `<script>alert(1)</script>` line, a `[click me](javascript:alert(1))` link and a `[docs](https://example.com/docs)` link.
  - Update and rollback in the fake engine take about 1.2 s so the busy state is visible; a check takes 0.8 s.
  - Playwright fixtures: `demo` (worker-scoped server on port `8099 + workerIndex`), `baseURL`, automatic reset before each test, and `signIn(page)` which creates the admin account `jordi` / `long enough password` through the API and stores the cookie in the browser context.
  - Commands: `npm run test:unit` (Node), `npm run test:e2e` (Playwright, `PW_WORKERS` default 2).

- [ ] **Step 1: Write the demo server**

`cmd/uidemo/main.go`:

```go
// Command uidemo runs the real nextupdate API and UI over a fake Docker
// engine and seeded data. It exists for UI development and the Playwright
// tests; it is not part of the shipped image.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/jordibrouwer/nextupdate/internal/api"
	"github.com/jordibrouwer/nextupdate/internal/auth"
	"github.com/jordibrouwer/nextupdate/internal/changelog"
	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/push"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/web"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8099", "listen address")
	flag.Parse()
	d := &demo{}
	if err := d.reset(); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("uidemo listening on %s\n", *addr)
	log.Fatal(http.ListenAndServe(*addr, d))
}

type demo struct {
	mu      sync.RWMutex
	handler http.Handler
	cleanup func()
	sinkMu  sync.Mutex
	sink    []string
}

func (d *demo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/__demo/reset" && r.Method == http.MethodPost:
		if err := d.reset(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/__demo/sink" && r.Method == http.MethodPost:
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		d.sinkMu.Lock()
		d.sink = append(d.sink, string(b))
		d.sinkMu.Unlock()
		w.WriteHeader(http.StatusOK)
	case r.URL.Path == "/__demo/sink" && r.Method == http.MethodGet:
		d.sinkMu.Lock()
		out := append([]string{}, d.sink...)
		d.sinkMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(out)
	default:
		d.mu.RLock()
		h := d.handler
		d.mu.RUnlock()
		h.ServeHTTP(w, r)
	}
}

func (d *demo) reset() error {
	dir, err := os.MkdirTemp("", "uidemo-*")
	if err != nil {
		return err
	}
	st, err := store.Open(dir + "/demo.db")
	if err != nil {
		return err
	}
	eng := newEngine(st)
	if err := eng.seed(); err != nil {
		return err
	}
	keys, err := push.EnsureKeys(st)
	if err != nil {
		return err
	}
	clock := time.Now
	server := api.New(api.Deps{
		Store:     st,
		Auth:      &auth.Service{Store: st, Cost: bcrypt.MinCost, Limiter: auth.NewLimiter(50, time.Minute, clock)},
		Engine:    eng,
		Changelog: demoChangelog{},
		Push:      &push.Sender{Keys: keys, Subject: "mailto:demo@localhost"},
		BaseURL:   "http://localhost",
		Version:   "demo",
		BaseCtx:   context.Background(),
	})
	d.sinkMu.Lock()
	d.sink = nil
	d.sinkMu.Unlock()
	d.mu.Lock()
	old := d.cleanup
	d.handler = web.Mount(server)
	d.cleanup = func() { st.Close(); os.RemoveAll(dir) }
	d.mu.Unlock()
	if old != nil {
		go func() { time.Sleep(3 * time.Second); old() }() // let requests in flight finish
	}
	return nil
}

// ---- fake engine -----------------------------------------------------------

type seedUpdate struct {
	container string
	image     string
	source    discovery.Source
	oldV, newV string
	repo      string
	policy    string
	breaking  bool
	reasons   []string
}

var seedUpdates = []seedUpdate{
	{"immich", "ghcr.io/immich-app/immich-server:release", discovery.SourceCompose, "1.98.0", "2.0.0", "immich-app/immich", store.PolicyNotify, true, []string{"Major version change from 1.98.0 to 2.0.0."}},
	{"sonarr", "linuxserver/sonarr:latest", discovery.SourceRun, "4.0.9", "4.0.10", "Sonarr/Sonarr", store.PolicyAuto, false, nil},
	{"vaultwarden", "vaultwarden/server:latest", discovery.SourceRun, "1.30.0", "1.31.0", "dani-garcia/vaultwarden", store.PolicyNotify, false, nil},
	{"flaky", "example/flaky:latest", discovery.SourceRun, "2.3.0", "2.3.1", "", store.PolicyNotify, false, nil},
}

type engine struct {
	st *store.Store
}

func newEngine(st *store.Store) *engine { return &engine{st: st} }

func fakeID(name, tag string) string {
	sum := sha256.Sum256([]byte(name + tag))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (e *engine) containers() []discovery.Container {
	var out []discovery.Container
	for _, u := range seedUpdates {
		out = append(out, discovery.Container{ID: "id-" + u.container, Name: u.container, Image: u.image, Source: u.source})
	}
	out = append(out,
		discovery.Container{ID: "id-jellyfin", Name: "jellyfin", Image: "jellyfin/jellyfin:latest", Source: discovery.SourceRun},
		discovery.Container{ID: "id-postgres", Name: "postgres", Image: "postgres:16", Source: discovery.SourceCompose},
	)
	return out
}

func (e *engine) offer(u seedUpdate) error {
	avail, err := e.st.ListAvailable()
	if err != nil {
		return err
	}
	infos, err := e.st.ListInfo()
	if err != nil {
		return err
	}
	avail = append(avail, store.Available{Container: u.container, Image: u.image, LocalDigest: fakeID(u.container, "old"), RemoteDigest: fakeID(u.container, "new"), DetectedAt: time.Now().Add(-90 * time.Minute)})
	infos = append(infos, store.Info{Container: u.container, OldVersion: u.oldV, NewVersion: u.newV, Repo: u.repo, Breaking: u.breaking, Reasons: u.reasons})
	if err := e.st.ReplaceAvailable(avail); err != nil {
		return err
	}
	return e.st.ReplaceInfo(infos)
}

func (e *engine) withdraw(name string) error {
	avail, _ := e.st.ListAvailable()
	infos, _ := e.st.ListInfo()
	var a2 []store.Available
	var i2 []store.Info
	for _, a := range avail {
		if a.Container != name {
			a2 = append(a2, a)
		}
	}
	for _, i := range infos {
		if i.Container != name {
			i2 = append(i2, i)
		}
	}
	if err := e.st.ReplaceAvailable(a2); err != nil {
		return err
	}
	return e.st.ReplaceInfo(i2)
}

func (e *engine) seed() error {
	for _, u := range seedUpdates {
		if err := e.offer(u); err != nil {
			return err
		}
		if err := e.st.SetSettings(store.Settings{Container: u.container, Policy: u.policy, Repo: ""}); err != nil {
			return err
		}
	}
	now := time.Now()
	if _, err := e.st.AddHistory(store.History{
		Container: "jellyfin", Image: "jellyfin/jellyfin:latest", FromImage: fakeID("jellyfin", "old"), ToImage: fakeID("jellyfin", "new"),
		StartedAt: now.Add(-48 * time.Hour), FinishedAt: now.Add(-48*time.Hour + 20*time.Second), Outcome: "ok",
		Log: []string{"pull jellyfin/jellyfin:latest", "stop jellyfin", "create jellyfin from jellyfin/jellyfin:latest", "verify jellyfin"},
	}); err != nil {
		return err
	}
	return e.st.SetSetting("last_check", now.Add(-25*time.Minute).UTC().Format(time.RFC3339))
}

func (e *engine) Containers(ctx context.Context) ([]discovery.Container, error) {
	return e.containers(), nil
}

func (e *engine) Check(ctx context.Context) ([]store.Available, error) {
	time.Sleep(800 * time.Millisecond)
	return e.st.ListAvailable()
}

func (e *engine) find(name string) (seedUpdate, bool) {
	for _, u := range seedUpdates {
		if u.container == name {
			return u, true
		}
	}
	return seedUpdate{}, false
}

func (e *engine) Update(ctx context.Context, name string) (store.History, error) {
	time.Sleep(1200 * time.Millisecond)
	u, ok := e.find(name)
	if !ok {
		return store.History{}, fmt.Errorf("no update is available for %s", name)
	}
	h := store.History{Container: name, Image: u.image, FromImage: fakeID(name, "old"), ToImage: fakeID(name, "new"),
		StartedAt: time.Now().Add(-1200 * time.Millisecond), FinishedAt: time.Now(),
		Log: []string{"pull " + u.image, "stop " + name, "create " + name + " from " + u.image, "verify " + name}}
	if name == "flaky" {
		h.Outcome, h.Reason = "rolled_back", "verify: healthcheck unhealthy"
		h.Log = append(h.Log, "rollback: verify: healthcheck unhealthy")
	} else {
		h.Outcome = "ok"
		if err := e.withdraw(name); err != nil {
			return h, err
		}
	}
	id, err := e.st.AddHistory(h)
	h.ID = id
	return h, err
}

func (e *engine) Rollback(ctx context.Context, name string) (store.History, error) {
	time.Sleep(1200 * time.Millisecond)
	hist, err := e.st.ListHistory(50)
	if err != nil {
		return store.History{}, err
	}
	var prev *store.History
	for i := range hist {
		if hist[i].Container == name && hist[i].Outcome == "ok" && hist[i].FromImage != hist[i].ToImage {
			prev = &hist[i]
			break
		}
	}
	if prev == nil {
		return store.History{}, fmt.Errorf("there is nothing to roll back to for %s", name)
	}
	h := store.History{Container: name, Image: prev.Image, FromImage: prev.ToImage, ToImage: prev.FromImage,
		StartedAt: time.Now().Add(-1200 * time.Millisecond), FinishedAt: time.Now(), Outcome: "ok",
		Reason: "Manual rollback.", Log: []string{"tag previous image", "stop " + name, "create " + name, "verify " + name}}
	if u, ok := e.find(name); ok {
		if err := e.offer(u); err != nil {
			return h, err
		}
	}
	id, err := e.st.AddHistory(h)
	h.ID = id
	return h, err
}

// ---- fake release notes ------------------------------------------------------

type demoChangelog struct{}

func (demoChangelog) Releases(ctx context.Context, repo string) ([]changelog.Release, error) {
	day := 24 * time.Hour
	now := time.Now()
	switch repo {
	case "immich-app/immich":
		return []changelog.Release{
			{Tag: "v2.0.0", Name: "Two point zero", PublishedAt: now.Add(-3 * day), URL: "https://github.com/immich-app/immich/releases/tag/v2.0.0",
				Body: "## Highlights\n\n- New timeline\n- Faster search\n\nDatabase migration runs on first start.\n\n<script>alert(1)</script>\n\nSee the [docs](https://example.com/docs) or [click me](javascript:alert(1)).\n\n```\nDB_HOST=database\n```\n"},
			{Tag: "v1.99.0", Name: "1.99.0", PublishedAt: now.Add(-10 * day), URL: "https://github.com/immich-app/immich/releases/tag/v1.99.0",
				Body: "Small fixes.\n\n1. Fix upload retry\n2. Fix **duplicate** detection\n"},
			{Tag: "v1.98.0", Name: "1.98.0", PublishedAt: now.Add(-20 * day), Body: "Older release."},
		}, nil
	case "Sonarr/Sonarr":
		return []changelog.Release{
			{Tag: "v4.0.10", Name: "4.0.10", PublishedAt: now.Add(-1 * day), URL: "https://github.com/Sonarr/Sonarr/releases/tag/v4.0.10", Body: "- Fixed: Parsing of anime titles\n- Fixed: Calendar timezone"},
			{Tag: "v4.0.9", Name: "4.0.9", PublishedAt: now.Add(-8 * day), Body: "Older."},
		}, nil
	case "dani-garcia/vaultwarden":
		return []changelog.Release{
			{Tag: "1.31.0", Name: "1.31.0", PublishedAt: now.Add(-5 * day), URL: "https://github.com/dani-garcia/vaultwarden/releases/tag/1.31.0", Body: "> Note: admin page has a new layout.\n\n- Add passkey support\n- Update web vault"},
			{Tag: "1.30.0", Name: "1.30.0", PublishedAt: now.Add(-40 * day), Body: "Older."},
		}, nil
	}
	return nil, nil
}
```

- [ ] **Step 2: Add the Node/Playwright setup**

`package.json`:

```json
{
  "name": "nextupdate-tests",
  "private": true,
  "scripts": {
    "test:unit": "node --test web-test/",
    "test:e2e": "playwright test"
  },
  "devDependencies": {
    "@playwright/test": "1.61.0"
  }
}
```

`.gitignore`:

```
node_modules/
test-results/
playwright-report/
e2e/.bin/
/nextupdate
```

`playwright.config.js`:

```js
// @ts-check
const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
    testDir: 'e2e',
    testMatch: /.*\.spec\.js$/,
    timeout: 30_000,
    fullyParallel: false,
    workers: Number(process.env.PW_WORKERS || 2),
    retries: 0,
    reporter: 'line',
    globalSetup: require.resolve('./e2e/global-setup.js'),
    use: { headless: true, viewport: { width: 1280, height: 800 }, trace: 'retain-on-failure' },
});
```

`e2e/global-setup.js`:

```js
const { execFileSync } = require('node:child_process');
const path = require('node:path');
const fs = require('node:fs');

// Build the demo server once, so every worker only has to start a binary.
module.exports = async () => {
    const root = path.resolve(__dirname, '..');
    fs.mkdirSync(path.join(root, 'e2e', '.bin'), { recursive: true });
    execFileSync('go', ['build', '-o', path.join(root, 'e2e', '.bin', 'uidemo'), './cmd/uidemo'], { cwd: root, stdio: 'inherit' });
};
```

`e2e/fixtures.js`:

```js
const base = require('@playwright/test');
const { spawn } = require('node:child_process');
const path = require('node:path');

const BIN = path.resolve(__dirname, '.bin', 'uidemo');
const PASSWORD = 'long enough password';

async function waitFor(url) {
    for (let i = 0; i < 100; i++) {
        try {
            const res = await fetch(url + '/api/status');
            if (res.ok) return;
        } catch { /* not up yet */ }
        await new Promise((r) => setTimeout(r, 100));
    }
    throw new Error(`demo server did not start at ${url}`);
}

const test = base.test.extend({
    // One demo server per worker, on 8099 + worker index. Never 8080.
    demo: [async ({}, use, workerInfo) => {
        const port = 8099 + workerInfo.workerIndex;
        const url = `http://127.0.0.1:${port}`;
        const proc = spawn(BIN, ['-addr', `127.0.0.1:${port}`], { stdio: 'inherit' });
        await waitFor(url);
        await use({ url, port });
        proc.kill();
    }, { scope: 'worker' }],

    baseURL: async ({ demo }, use) => { await use(demo.url); },

    // Fresh seeded state and no session before every test.
    page: async ({ page, demo }, use) => {
        const res = await fetch(`${demo.url}/__demo/reset`, { method: 'POST' });
        if (!res.ok) throw new Error(`reset failed: ${res.status}`);
        await use(page);
    },
});

/** Creates the admin account through the API; the cookie lands in the page's context. */
async function signIn(page) {
    const res = await page.request.post('/api/setup', {
        headers: { 'X-NextUpdate': '1' },
        data: { name: 'jordi', password: PASSWORD },
    });
    if (!res.ok()) throw new Error(`setup failed: ${res.status()} ${await res.text()}`);
}

module.exports = { test, expect: base.expect, signIn, PASSWORD };
```

`e2e/pwa.spec.js` (smoke; grows in Task 9):

```js
const { test, expect } = require('./fixtures');

test('the demo serves the app shell', async ({ page }) => {
    const res = await page.goto('/');
    expect(res.status()).toBe(200);
    await expect(page).toHaveTitle('nextupdate');
    expect(res.headers()['content-security-policy']).toContain("script-src 'self'");
});
```

`web-test/.keep`: empty file.

- [ ] **Step 3: Install and run the smoke test**

Run: `npm install` (background if slow), then `gofmt -l . ; go vet ./cmd/uidemo/` (expect no output).

Run: `PW_WORKERS=2 npx playwright test e2e/pwa.spec.js > /tmp/pw.log 2>&1; echo exit=$? >> /tmp/pw.log; tail -5 /tmp/pw.log`
Expected: `1 passed`, `exit=0`.

Then check the demo by hand: `go run ./cmd/uidemo -addr 127.0.0.1:8099 &`, `curl -s -X POST -H 'X-NextUpdate: 1' -d '{"name":"jordi","password":"long enough password"}' -c /tmp/c localhost:8099/api/setup`, `curl -s -b /tmp/c localhost:8099/api/updates`. Expected: four updates, `immich` first with `"breaking":true`. Stop the demo server (`kill %1`).

- [ ] **Step 4: Commit**

```bash
git add cmd/uidemo package.json package-lock.json playwright.config.js .gitignore e2e web-test
git commit -m "demo server and playwright setup for the ui"
```

---

### Task 3: Pure helpers with Node tests

**Files:**
- Create: `internal/web/static/js/format.js`, `internal/web/static/js/markdown.js` (parser part), `web-test/format.test.mjs`, `web-test/markdown.test.mjs`

**Interfaces:**
- Produces (`format.js`, no DOM access):
  - `parseVersion(s: string): {major, minor, patch, pre} | null` (accepts `v1.2.3`, `1.2`, `2`, `-pre`, `+build`)
  - `versionChange(from: string, to: string): 'patch'|'minor'|'major'|'downgrade'|'same'|''` (`''` when either version is unknown; while both are `0.x` a minor step is `'major'`, like the Go code)
  - `relativeTime(iso: string, now?: number): string` (`just now`, `5 minutes ago`, `1 hour ago`, `3 days ago`, then `12 Sep 2026`; future or invalid input gives `''`)
  - `shortImageId(id: string): string` (drops `sha256:`, first 12 characters, `''` for empty)
  - `plural(n: number, one: string, many: string): string`
  - `outcomeLabel(outcome: string): string` (`ok` → `Updated`, `rolled_back` → `Rolled back`, `failed` → `Failed`, otherwise the input)
- Produces (`markdown.js`, parser only in this task):
  - `parseMarkdown(src: string): Block[]` and `parseInline(src: string): Inline[]`
  - `Block`: `{t:'p', c:Inline[]}`, `{t:'heading', level:1-6, c}`, `{t:'ul'|'ol', items: Inline[][]}`, `{t:'code', v:string}`, `{t:'quote', c:Block[]}`, `{t:'hr'}`
  - `Inline`: `{t:'text', v}`, `{t:'code', v}`, `{t:'strong'|'em', c:Inline[]}`, `{t:'link', href, c:Inline[]}` (only for `http://` and `https://` URLs; anything else stays plain text including the brackets), `{t:'br'}` is not used
  - HTML comments (`<!-- ... -->`) are dropped; every other `<tag>` is kept as visible text

- [ ] **Step 1: Write the failing tests**

`web-test/format.test.mjs`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { parseVersion, versionChange, relativeTime, shortImageId, plural, outcomeLabel } from '../internal/web/static/js/format.js';

test('parseVersion', () => {
    assert.deepEqual(parseVersion('v1.2.3'), { major: 1, minor: 2, patch: 3, pre: '' });
    assert.deepEqual(parseVersion('2'), { major: 2, minor: 0, patch: 0, pre: '' });
    assert.deepEqual(parseVersion('1.2'), { major: 1, minor: 2, patch: 0, pre: '' });
    assert.deepEqual(parseVersion('1.2.3-rc.1+build5'), { major: 1, minor: 2, patch: 3, pre: 'rc.1' });
    for (const bad of ['', 'latest', '1.2.3.4', '1.x.3', undefined, null]) assert.equal(parseVersion(bad), null, String(bad));
});

test('versionChange', () => {
    const cases = [
        ['1.2.3', '1.2.3', 'same'], ['1.2.3', '1.2.4', 'patch'], ['1.2.3', '1.3.0', 'minor'],
        ['1.2.3', '2.0.0', 'major'], ['1.2.3', '1.2.2', 'downgrade'], ['0.4.1', '0.4.2', 'patch'],
        ['0.4.1', '0.5.0', 'major'], ['', '1.0.0', ''], ['1.0.0', 'latest', ''],
    ];
    for (const [a, b, want] of cases) assert.equal(versionChange(a, b), want, `${a} to ${b}`);
});

test('relativeTime', () => {
    const now = Date.parse('2026-09-25T12:00:00Z');
    const at = (ms) => new Date(now - ms).toISOString();
    assert.equal(relativeTime(at(20_000), now), 'just now');
    assert.equal(relativeTime(at(60_000), now), '1 minute ago');
    assert.equal(relativeTime(at(5 * 60_000), now), '5 minutes ago');
    assert.equal(relativeTime(at(3_600_000), now), '1 hour ago');
    assert.equal(relativeTime(at(3 * 86_400_000), now), '3 days ago');
    assert.match(relativeTime(at(60 * 86_400_000), now), /^\d{1,2} \w{3} 2026$/);
    assert.equal(relativeTime(new Date(now + 60_000).toISOString(), now), '');
    assert.equal(relativeTime('nonsense', now), '');
    assert.equal(relativeTime('', now), '');
});

test('small helpers', () => {
    assert.equal(shortImageId('sha256:0123456789abcdef0123'), '0123456789ab');
    assert.equal(shortImageId(''), '');
    assert.equal(plural(1, 'update', 'updates'), '1 update');
    assert.equal(plural(3, 'update', 'updates'), '3 updates');
    assert.equal(outcomeLabel('ok'), 'Updated');
    assert.equal(outcomeLabel('rolled_back'), 'Rolled back');
    assert.equal(outcomeLabel('failed'), 'Failed');
    assert.equal(outcomeLabel('other'), 'other');
});
```

`web-test/markdown.test.mjs`:

```js
import test from 'node:test';
import assert from 'node:assert/strict';
import { parseMarkdown, parseInline } from '../internal/web/static/js/markdown.js';

const text = (v) => ({ t: 'text', v });

test('paragraphs and soft line breaks', () => {
    assert.deepEqual(parseMarkdown('one\ntwo\n\nthree'), [
        { t: 'p', c: [text('one two')] },
        { t: 'p', c: [text('three')] },
    ]);
});

test('headings', () => {
    assert.deepEqual(parseMarkdown('## What changed ##'), [{ t: 'heading', level: 2, c: [text('What changed')] }]);
});

test('lists', () => {
    assert.deepEqual(parseMarkdown('- a\n- b **bold**\n* c'), [{ t: 'ul', items: [[text('a')], [text('b '), { t: 'strong', c: [text('bold')] }], [text('c')]] }]);
    assert.deepEqual(parseMarkdown('1. x\n2) y'), [{ t: 'ol', items: [[text('x')], [text('y')]] }]);
});

test('fenced code keeps its text and is never parsed', () => {
    assert.deepEqual(parseMarkdown('```yaml\n**not bold**\n<b>x</b>\n```'), [{ t: 'code', v: '**not bold**\n<b>x</b>' }]);
});

test('quotes nest and hr', () => {
    assert.deepEqual(parseMarkdown('> Note: careful\n\n---'), [{ t: 'quote', c: [{ t: 'p', c: [text('Note: careful')] }] }, { t: 'hr' }]);
});

test('inline: code, emphasis, links', () => {
    assert.deepEqual(parseInline('use `x` and *this* or _that_'), [text('use '), { t: 'code', v: 'x' }, text(' and '), { t: 'em', c: [text('this')] }, text(' or '), { t: 'em', c: [text('that')] }]);
    assert.deepEqual(parseInline('[docs](https://example.com/a?b=1)'), [{ t: 'link', href: 'https://example.com/a?b=1', c: [text('docs')] }]);
});

test('untrusted markup stays text', () => {
    assert.deepEqual(parseMarkdown('<script>alert(1)</script>'), [{ t: 'p', c: [text('<script>alert(1)</script>')] }]);
    assert.deepEqual(parseInline('<img src=x onerror=alert(1)>'), [text('<img src=x onerror=alert(1)>')]);
});

test('only http and https links become links', () => {
    for (const bad of ['javascript:alert(1)', 'JaVaScRiPt:alert(1)', 'data:text/html,x', 'vbscript:x', '//evil.example', '/relative', 'mailto:a@b.c']) {
        const got = parseInline(`[click](${bad})`);
        assert.ok(got.every((n) => n.t !== 'link'), `${bad} must not become a link: ${JSON.stringify(got)}`);
        assert.equal(got.map((n) => n.v ?? '').join(''), `[click](${bad})`);
    }
    assert.equal(parseInline('[a](http://example.com)')[0].t, 'link');
});

test('html comments are dropped', () => {
    assert.deepEqual(parseMarkdown('before\n<!-- hidden -->\nafter'), [{ t: 'p', c: [text('before after')] }]);
});

test('empty and whitespace input', () => {
    assert.deepEqual(parseMarkdown(''), []);
    assert.deepEqual(parseMarkdown('  \n\n '), []);
    assert.deepEqual(parseMarkdown(null), []);
});
```

- [ ] **Step 2: Run to verify they fail**

Run: `node --test web-test/ 2>&1 | head -12`
Expected: FAIL (`Cannot find module .../format.js`).

- [ ] **Step 3: Implement `format.js`**

`internal/web/static/js/format.js`:

```js
// Pure helpers. No DOM access, so Node can test them.

export function parseVersion(s) {
    if (typeof s !== 'string') return null;
    let v = s.trim().replace(/^v/, '');
    v = v.split('+')[0];
    const dash = v.indexOf('-');
    const core = dash === -1 ? v : v.slice(0, dash);
    const pre = dash === -1 ? '' : v.slice(dash + 1);
    const parts = core.split('.');
    if (!core || parts.length > 3 || parts.some((p) => !/^\d+$/.test(p))) return null;
    const [major, minor = 0, patch = 0] = parts.map(Number);
    return { major, minor, patch, pre };
}

function compare(a, b) {
    for (const k of ['major', 'minor', 'patch']) {
        if (a[k] !== b[k]) return a[k] < b[k] ? -1 : 1;
    }
    if (a.pre === b.pre) return 0;
    if (a.pre === '') return 1;
    if (b.pre === '') return -1;
    return a.pre < b.pre ? -1 : 1;
}

export function versionChange(from, to) {
    const a = parseVersion(from);
    const b = parseVersion(to);
    if (!a || !b) return '';
    const c = compare(a, b);
    if (c > 0) return 'downgrade';
    if (c === 0) return 'same';
    if (b.major !== a.major) return 'major';
    if (b.minor !== a.minor) return a.major === 0 ? 'major' : 'minor';
    return 'patch';
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

export function relativeTime(iso, now = Date.now()) {
    const t = Date.parse(iso);
    if (!iso || Number.isNaN(t) || t > now) return '';
    const s = Math.floor((now - t) / 1000);
    if (s < 45) return 'just now';
    const m = Math.floor(s / 60);
    if (m < 60) return plural(m, 'minute', 'minutes') + ' ago';
    const h = Math.floor(m / 60);
    if (h < 24) return plural(h, 'hour', 'hours') + ' ago';
    const d = Math.floor(h / 24);
    if (d < 30) return plural(d, 'day', 'days') + ' ago';
    const date = new Date(t);
    return `${date.getUTCDate()} ${MONTHS[date.getUTCMonth()]} ${date.getUTCFullYear()}`;
}

export function shortImageId(id) {
    return id ? id.replace(/^sha256:/, '').slice(0, 12) : '';
}

export function plural(n, one, many) {
    return `${n} ${n === 1 ? one : many}`;
}

export function outcomeLabel(outcome) {
    return { ok: 'Updated', rolled_back: 'Rolled back', failed: 'Failed' }[outcome] || outcome;
}
```

- [ ] **Step 4: Implement the markdown parser**

`internal/web/static/js/markdown.js`:

```js
// A small, safe Markdown reader for release notes. It never produces HTML:
// parseMarkdown returns a plain tree, and renderMarkdown (Task 5) builds DOM
// nodes from it. Raw HTML in the source stays visible text.

const SAFE_URL = /^https?:\/\/[^\s<>"]+$/i;

const INLINE = new RegExp([
    '(`+)([\\s\\S]*?[^`])\\1(?!`)',                 // 1,2: code
    '\\*\\*([\\s\\S]+?)\\*\\*',                     // 3: strong
    '__([\\s\\S]+?)__',                             // 4: strong
    '\\*([^\\s*][\\s\\S]*?)\\*',                    // 5: em
    '(?<![A-Za-z0-9])_([^\\s_][\\s\\S]*?)_(?![A-Za-z0-9])', // 6: em
    '\\[([^\\]]+)\\]\\(([^)\\s]+)\\)',              // 7,8: link
].join('|'));

export function parseInline(src) {
    const out = [];
    let rest = String(src ?? '');
    const pushText = (v) => {
        if (!v) return;
        const last = out[out.length - 1];
        if (last && last.t === 'text') last.v += v;
        else out.push({ t: 'text', v });
    };
    while (rest) {
        const m = INLINE.exec(rest);
        if (!m) { pushText(rest); break; }
        pushText(rest.slice(0, m.index));
        if (m[2] !== undefined) out.push({ t: 'code', v: m[2].trim() });
        else if (m[3] !== undefined) out.push({ t: 'strong', c: parseInline(m[3]) });
        else if (m[4] !== undefined) out.push({ t: 'strong', c: parseInline(m[4]) });
        else if (m[5] !== undefined) out.push({ t: 'em', c: parseInline(m[5]) });
        else if (m[6] !== undefined) out.push({ t: 'em', c: parseInline(m[6]) });
        else if (SAFE_URL.test(m[8])) out.push({ t: 'link', href: m[8], c: parseInline(m[7]) });
        else pushText(m[0]); // unsafe or odd link: keep the source text
        rest = rest.slice(m.index + m[0].length);
    }
    return out;
}

const FENCE = /^(`{3,}|~{3,})\s*\S*\s*$/;
const HEADING = /^(#{1,6})\s+(.*?)\s*#*\s*$/;
const HR = /^\s*([-*_])(\s*\1){2,}\s*$/;
const UL = /^\s*[-*+]\s+(.*)$/;
const OL = /^\s*\d+[.)]\s+(.*)$/;
const QUOTE = /^>\s?(.*)$/;

const startsBlock = (line) => FENCE.test(line) || HEADING.test(line) || HR.test(line) || UL.test(line) || OL.test(line) || QUOTE.test(line);

export function parseMarkdown(src) {
    if (typeof src !== 'string') return [];
    const lines = src.replace(/\r\n?/g, '\n').replace(/<!--[\s\S]*?-->/g, '').split('\n');
    const blocks = [];
    let i = 0;
    while (i < lines.length) {
        const line = lines[i];
        let m;
        if (/^\s*$/.test(line)) { i++; continue; }
        if ((m = FENCE.exec(line))) {
            const fence = m[1];
            const body = [];
            i++;
            while (i < lines.length && !lines[i].trim().startsWith(fence)) body.push(lines[i++]);
            i++; // closing fence
            blocks.push({ t: 'code', v: body.join('\n') });
        } else if ((m = HEADING.exec(line))) {
            blocks.push({ t: 'heading', level: m[1].length, c: parseInline(m[2]) });
            i++;
        } else if (HR.test(line)) {
            blocks.push({ t: 'hr' });
            i++;
        } else if (QUOTE.test(line)) {
            const body = [];
            while (i < lines.length && QUOTE.test(lines[i])) body.push(QUOTE.exec(lines[i++])[1]);
            blocks.push({ t: 'quote', c: parseMarkdown(body.join('\n')) });
        } else if (UL.test(line) || OL.test(line)) {
            const ordered = OL.test(line);
            const re = ordered ? OL : UL;
            const items = [];
            while (i < lines.length && re.test(lines[i])) items.push(parseInline(re.exec(lines[i++])[1]));
            blocks.push({ t: ordered ? 'ol' : 'ul', items });
        } else {
            const para = [];
            while (i < lines.length && !/^\s*$/.test(lines[i]) && (para.length === 0 || !startsBlock(lines[i]))) para.push(lines[i++].trim());
            blocks.push({ t: 'p', c: parseInline(para.join(' ')) });
        }
    }
    return blocks;
}
```

- [ ] **Step 5: Run to verify they pass**

Run: `node --test web-test/ 2>&1 | tail -15`
Expected: all tests pass (`# pass 12` or similar), `# fail 0`.

- [ ] **Step 6: Falsify the link filter and the fenced code rule**

Temporarily change `SAFE_URL` to `/^[a-z]+:/i` and make fenced code call `parseInline`; `only http and https links become links` and `fenced code keeps its text` must FAIL. Restore and rerun.

- [ ] **Step 7: Commit**

```bash
git add internal/web/static/js web-test
git commit -m "ui helpers: versions, times and a safe markdown parser"
```

---

### Task 4: Shell, styles, login and setup

**Files:**
- Create: `internal/web/static/index.html` (replace), `internal/web/static/css/app.css`, `internal/web/static/js/theme.js`, `dom.js`, `icons.js`, `api.js`, `ui.js`, `jobs.js`, `app.js` (replace), `views/login.js`, and stubs `views/updates.js`, `views/history.js`, `views/settings.js`
- Test: `e2e/auth.spec.js`

**Interfaces:**
- Produces (used by every later task):
  - `dom.js`: `h(tag, props, ...children)` (props: `class`, `dataset`, `on<event>` handlers, DOM properties, otherwise attributes; children may be nodes, strings, numbers, arrays), `append(el, children)`, `clear(el)`
  - `icons.js`: `icon(name, size=16)` for `refresh`, `check`, `alert`, `back`, `external`, `contrast`, `box`
  - `api.js`: `api.get/post/put/del(path, body?)` returning parsed JSON; throws `ApiError{status, message}`; a 401 from a protected route dispatches `document` event `nu:signed-out`
  - `ui.js`: `toast(message, kind='info'|'success'|'warn'|'error')`, `badge(text, kind)`, `spinner()`, `confirmDialog({title, body, confirmLabel, danger}) → Promise<boolean>`
  - `jobs.js`: `onJobs(fn) → unsubscribe` where `fn(current, previous)` gets `{running:[{key,kind,since}], failed:[{key,kind,error,at}], lastCheck}`; polling every 1.5 s while something runs and every 15 s otherwise, only one timer for all listeners; `refreshJobs()` polls now
  - `app.js`: boot flow (status → setup or login or shell), header (brand, nav `Updates` `History` `Settings`, check-now button `[data-testid=check-button]`, last-check text `[data-testid=last-check]`, theme button, `Sign out`), hash router, view contract `{ mount(container, params) → { unmount() } }` exported by each view module as `mountUpdates`, `mountHistory`, `mountSettings`
  - Login view: `[data-testid=auth-form]`; in setup mode heading `Create your admin account`, in login mode `Sign in`; fields `name` and `password`; error text in `[data-testid=auth-error]`

- [ ] **Step 1: Write the failing e2e spec**

`e2e/auth.spec.js`:

```js
const { test, expect, signIn, PASSWORD } = require('./fixtures');

test('the first visit asks for an admin account, then signs in', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Create your admin account' })).toBeVisible();

    await page.getByLabel('Name').fill('jordi');
    await page.getByLabel('Password').fill('short');
    await page.getByRole('button', { name: 'Create account' }).click();
    await expect(page.getByTestId('auth-error')).toContainText('at least 10 characters');

    await page.getByLabel('Password').fill(PASSWORD);
    await page.getByRole('button', { name: 'Create account' }).click();
    await expect(page.getByTestId('check-button')).toBeVisible();
    await expect(page).toHaveURL(/#\/updates/);
});

test('signing out returns to the sign-in form, and a wrong password says so', async ({ page }) => {
    await signIn(page);
    await page.goto('/');
    await expect(page.getByTestId('check-button')).toBeVisible();

    await page.getByRole('button', { name: 'Sign out' }).click();
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();

    await page.getByLabel('Name').fill('jordi');
    await page.getByLabel('Password').fill('not the password');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByTestId('auth-error')).toContainText("don't match");

    await page.getByLabel('Password').fill(PASSWORD);
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByTestId('check-button')).toBeVisible();
});

test('a session that ends while the app is open goes back to sign in', async ({ page }) => {
    await signIn(page);
    await page.goto('/');
    await expect(page.getByTestId('check-button')).toBeVisible();
    await page.context().clearCookies();
    await page.getByRole('link', { name: 'History' }).click();
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
});

test('the theme button cycles and the choice survives a reload', async ({ page }) => {
    await signIn(page);
    await page.goto('/');
    const html = page.locator('html');
    await page.getByRole('button', { name: /Theme/ }).click(); // auto to light
    await expect(html).toHaveAttribute('data-theme', 'light');
    await page.getByRole('button', { name: /Theme/ }).click(); // light to dark
    await expect(html).toHaveAttribute('data-theme', 'dark');
    await page.reload();
    await expect(html).toHaveAttribute('data-theme', 'dark');
    await page.getByRole('button', { name: /Theme/ }).click(); // dark to auto
    await expect(html).not.toHaveAttribute('data-theme', /.+/);
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `PW_WORKERS=2 npx playwright test e2e/auth.spec.js > /tmp/pw.log 2>&1; echo exit=$? >> /tmp/pw.log; tail -8 /tmp/pw.log`
Expected: `exit=1`, failures on the missing heading.

- [ ] **Step 3: Write `index.html`, `theme.js`, `dom.js`, `icons.js`**

`internal/web/static/index.html`:

```html
<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>nextupdate</title>
<meta name="theme-color" content="#2563eb">
<link rel="manifest" href="/manifest.webmanifest">
<link rel="icon" href="/icons/icon.svg" type="image/svg+xml">
<link rel="apple-touch-icon" href="/icons/apple-touch-icon.png">
<link rel="stylesheet" href="/css/app.css">
<script src="/js/theme.js"></script>
<script type="module" src="/js/app.js"></script>
</head>
<body>
<div id="app"></div>
<div id="toasts" role="status" aria-live="polite"></div>
</body>
</html>
```

`internal/web/static/js/theme.js`:

```js
// Classic script: runs before the first paint so the chosen theme does not flash.
(function () {
    try {
        var t = localStorage.getItem('nu-theme');
        if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t;
    } catch (e) { /* storage blocked: follow the system theme */ }
})();
```

`internal/web/static/js/dom.js`:

```js
/** Builds an element. Never parses HTML; strings become text nodes. */
export function h(tag, props, ...children) {
    const el = document.createElement(tag);
    for (const [k, v] of Object.entries(props || {})) {
        if (v == null || v === false) continue;
        if (k === 'class') el.className = v;
        else if (k === 'dataset') Object.assign(el.dataset, v);
        else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2).toLowerCase(), v);
        else if (k in el && k !== 'list' && k !== 'form' && typeof v !== 'object') el[k] = v;
        else el.setAttribute(k, v === true ? '' : String(v));
    }
    append(el, children);
    return el;
}

export function append(el, children) {
    for (const c of children.flat(Infinity)) {
        if (c == null || c === false) continue;
        el.append(c.nodeType ? c : document.createTextNode(String(c)));
    }
    return el;
}

export function clear(el) {
    el.replaceChildren();
    return el;
}
```

`internal/web/static/js/icons.js`:

```js
const NS = 'http://www.w3.org/2000/svg';
const PATHS = {
    refresh: ['M21 12a9 9 0 1 1-3-6.7', 'M21 3v6h-6'],
    check: ['M20 6 9 17l-5-5'],
    alert: ['M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z', 'M12 8v5', 'M12 16h.01'],
    back: ['M15 18l-6-6 6-6'],
    external: ['M14 4h6v6', 'M20 4 10 14', 'M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5'],
    contrast: ['M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18z', 'M12 3v18'],
    box: ['M21 8l-9-5-9 5v8l9 5 9-5z', 'M3 8l9 5 9-5', 'M12 13v8'],
};

export function icon(name, size = 16) {
    const svg = document.createElementNS(NS, 'svg');
    const attrs = { viewBox: '0 0 24 24', width: size, height: size, fill: 'none', stroke: 'currentColor', 'stroke-width': 2, 'stroke-linecap': 'round', 'stroke-linejoin': 'round', 'aria-hidden': 'true' };
    for (const [k, v] of Object.entries(attrs)) svg.setAttribute(k, v);
    svg.classList.add('icon');
    for (const d of PATHS[name] || []) {
        const p = document.createElementNS(NS, 'path');
        p.setAttribute('d', d);
        svg.append(p);
    }
    return svg;
}
```

- [ ] **Step 4: Write `api.js`, `ui.js`, `jobs.js`**

`internal/web/static/js/api.js`:

```js
export class ApiError extends Error {
    constructor(status, message) {
        super(message);
        this.status = status;
    }
}

const PUBLIC = ['/api/status', '/api/login', '/api/setup'];

async function request(method, path, body) {
    const opts = { method, headers: {}, credentials: 'same-origin' };
    if (method !== 'GET') opts.headers['X-NextUpdate'] = '1';
    if (body !== undefined) {
        opts.headers['Content-Type'] = 'application/json';
        opts.body = JSON.stringify(body);
    }
    let res;
    try {
        res = await fetch(path, opts);
    } catch {
        throw new ApiError(0, "Can't reach the server. Check your connection and try again.");
    }
    let data = null;
    const text = await res.text();
    if (text) {
        try { data = JSON.parse(text); } catch { /* not JSON */ }
    }
    if (!res.ok) {
        if (res.status === 401 && !PUBLIC.includes(path)) document.dispatchEvent(new CustomEvent('nu:signed-out'));
        throw new ApiError(res.status, (data && data.error) || `The request failed (${res.status}).`);
    }
    return data;
}

export const api = {
    get: (p) => request('GET', p),
    post: (p, b) => request('POST', p, b ?? {}),
    put: (p, b) => request('PUT', p, b),
    del: (p) => request('DELETE', p),
};
```

`internal/web/static/js/ui.js`:

```js
import { h } from './dom.js';

export function toast(message, kind = 'info') {
    const host = document.getElementById('toasts');
    const el = h('button', { class: `toast toast-${kind}`, type: 'button', dataset: { testid: 'toast' }, onclick: () => el.remove() }, message);
    host.append(el);
    setTimeout(() => el.remove(), kind === 'error' || kind === 'warn' ? 9000 : 5000);
}

export function badge(text, kind = 'neutral') {
    return h('span', { class: `badge badge-${kind}` }, text);
}

export function spinner() {
    return h('span', { class: 'spinner', role: 'img', 'aria-label': 'Working' });
}

export function confirmDialog({ title, body, confirmLabel = 'Confirm', danger = false }) {
    return new Promise((resolve) => {
        const dlg = h('dialog', { class: 'dialog', dataset: { testid: 'confirm-dialog' } },
            h('h2', {}, title),
            h('div', { class: 'dialog-body' }, body),
            h('div', { class: 'dialog-actions' },
                h('button', { class: 'btn', type: 'button', onclick: () => dlg.close('cancel') }, 'Cancel'),
                h('button', { class: `btn ${danger ? 'btn-danger' : 'btn-primary'}`, type: 'button', dataset: { testid: 'confirm-yes' }, onclick: () => dlg.close('yes') }, confirmLabel)));
        dlg.addEventListener('close', () => {
            resolve(dlg.returnValue === 'yes');
            dlg.remove();
        });
        document.body.append(dlg);
        dlg.showModal();
    });
}
```

`internal/web/static/js/jobs.js`:

```js
import { api } from './api.js';

const listeners = new Set();
let timer = null;
let last = { running: [], failed: [], lastCheck: '' };

function schedule() {
    clearTimeout(timer);
    if (!listeners.size) return;
    timer = setTimeout(() => refreshJobs().catch(() => schedule()), last.running.length ? 1500 : 15000);
}

export async function refreshJobs() {
    const prev = last;
    last = await api.get('/api/jobs');
    for (const fn of [...listeners]) fn(last, prev);
    schedule();
    return last;
}

/** fn(current, previous) runs on every poll. Returns an unsubscribe function. */
export function onJobs(fn) {
    listeners.add(fn);
    fn(last, last);
    if (listeners.size === 1) refreshJobs().catch(() => schedule());
    return () => {
        listeners.delete(fn);
        if (!listeners.size) clearTimeout(timer);
    };
}

export function currentJobs() {
    return last;
}
```

- [ ] **Step 5: Write `app.css`**

`internal/web/static/css/app.css`:

```css
:root {
    --bg: #f5f5f2;
    --surface: #ffffff;
    --surface-2: #efefeb;
    --text: #1b1b19;
    --text-2: #5a5a55;
    --text-3: #83837d;
    --border: #e1e1da;
    --border-strong: #c9c9c0;
    --accent: #2563eb;
    --accent-hover: #1d4ed8;
    --on-accent: #ffffff;
    --accent-bg: #e8effd;
    --danger: #b42318;
    --danger-bg: #fdecea;
    --success: #146c43;
    --success-bg: #e5f4ec;
    --warn: #8a5300;
    --warn-bg: #fdf1dc;
    --radius: 8px;
    --gap: 12px;
    --font: system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
    --mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
    color-scheme: light;
}
@media (prefers-color-scheme: dark) {
    :root:not([data-theme="light"]) {
        --bg: #131312; --surface: #1c1c1a; --surface-2: #262624; --text: #ececE8; --text-2: #a9a9a2; --text-3: #7d7d77;
        --border: #2f2f2c; --border-strong: #454541; --accent: #6b9bff; --accent-hover: #8bb1ff; --on-accent: #0b1220; --accent-bg: #1a2742;
        --danger: #ff8a7a; --danger-bg: #3a1c19; --success: #6fd3a0; --success-bg: #163224; --warn: #f2b45a; --warn-bg: #3a2b12;
        color-scheme: dark;
    }
}
:root[data-theme="dark"] {
    --bg: #131312; --surface: #1c1c1a; --surface-2: #262624; --text: #ececE8; --text-2: #a9a9a2; --text-3: #7d7d77;
    --border: #2f2f2c; --border-strong: #454541; --accent: #6b9bff; --accent-hover: #8bb1ff; --on-accent: #0b1220; --accent-bg: #1a2742;
    --danger: #ff8a7a; --danger-bg: #3a1c19; --success: #6fd3a0; --success-bg: #163224; --warn: #f2b45a; --warn-bg: #3a2b12;
    color-scheme: dark;
}

* { box-sizing: border-box; }
html, body { height: 100%; }
body { margin: 0; background: var(--bg); color: var(--text); font: 14px/1.5 var(--font); }
a { color: var(--accent); }
h1, h2, h3, h4 { margin: 0; font-weight: 600; line-height: 1.25; }
h1 { font-size: 20px; }
h2 { font-size: 16px; }
h3 { font-size: 14px; }
p { margin: 0 0 8px; }
code, pre, kbd { font-family: var(--mono); font-size: 12.5px; }
:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }
.icon { flex: none; vertical-align: -3px; }
.muted { color: var(--text-2); }
.faint { color: var(--text-3); }
.sr-only { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }

/* Buttons and fields */
.btn { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 12px; border: 1px solid var(--border-strong); border-radius: var(--radius); background: var(--surface); color: var(--text); font: inherit; cursor: pointer; }
.btn:hover { background: var(--surface-2); }
.btn-primary { background: var(--accent); border-color: var(--accent); color: var(--on-accent); }
.btn-primary:hover { background: var(--accent-hover); border-color: var(--accent-hover); }
.btn-danger { background: var(--danger); border-color: var(--danger); color: var(--surface); }
.btn-ghost { border-color: transparent; background: transparent; }
.btn-small { height: 26px; padding: 0 8px; font-size: 13px; }
.btn[aria-busy="true"] { opacity: .7; cursor: progress; }
label { display: block; font-weight: 500; margin: 0 0 4px; }
input[type="text"], input[type="password"], input[type="url"], input[type="number"], select, textarea { width: 100%; height: 34px; padding: 0 10px; border: 1px solid var(--border-strong); border-radius: var(--radius); background: var(--surface); color: var(--text); font: inherit; }
select { padding-right: 24px; }
.field { margin-bottom: 14px; }
.hint { color: var(--text-2); font-size: 13px; margin-top: 4px; }
.error-text { color: var(--danger); font-size: 13px; margin: 8px 0 0; }
kbd { display: inline-block; min-width: 20px; padding: 0 5px; text-align: center; border: 1px solid var(--border-strong); border-bottom-width: 2px; border-radius: 4px; background: var(--surface); }

/* Badges and spinner */
.badge { display: inline-block; padding: 1px 8px; border-radius: 999px; font-size: 12px; font-weight: 500; white-space: nowrap; background: var(--surface-2); color: var(--text-2); }
.badge-danger { background: var(--danger-bg); color: var(--danger); }
.badge-accent { background: var(--accent-bg); color: var(--accent); }
.badge-success { background: var(--success-bg); color: var(--success); }
.badge-warn { background: var(--warn-bg); color: var(--warn); }
.spinner { display: inline-block; width: 14px; height: 14px; border: 2px solid var(--border-strong); border-top-color: var(--accent); border-radius: 50%; animation: spin .8s linear infinite; vertical-align: -2px; }
@keyframes spin { to { transform: rotate(360deg); } }
@media (prefers-reduced-motion: reduce) { .spinner { animation-duration: 3s; } }

/* Shell */
.shell { display: flex; flex-direction: column; height: 100%; }
.header { display: flex; align-items: center; gap: 16px; height: 52px; padding: 0 16px; background: var(--surface); border-bottom: 1px solid var(--border); flex: none; }
.brand { font-weight: 700; font-size: 16px; display: flex; align-items: center; gap: 8px; color: var(--text); text-decoration: none; }
.nav { display: flex; gap: 4px; }
.nav a { padding: 6px 10px; border-radius: var(--radius); color: var(--text-2); text-decoration: none; }
.nav a:hover { background: var(--surface-2); }
.nav a[aria-current="page"] { color: var(--text); background: var(--surface-2); font-weight: 500; }
.header-end { margin-left: auto; display: flex; align-items: center; gap: 10px; }
.last-check { color: var(--text-2); font-size: 13px; }
.main { flex: 1; min-height: 0; overflow: auto; }
.page { max-width: 860px; margin: 0 auto; padding: 20px 16px 48px; }
.page > h1 { margin-bottom: 16px; }

/* Cards */
.card { background: var(--surface); border: 1px solid var(--border); border-radius: 12px; padding: 16px; margin-bottom: 16px; }
.card > h2 { margin-bottom: 12px; }
.row-flex { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
.spacer { flex: 1; }

/* Auth */
.auth { max-width: 380px; margin: 12vh auto 0; padding: 0 16px; }
.auth .card { padding: 24px; }
.auth h1 { margin-bottom: 4px; }
.auth .lede { color: var(--text-2); margin-bottom: 20px; }
.auth .btn { width: 100%; justify-content: center; height: 36px; }

/* Toasts and dialog */
#toasts { position: fixed; right: 16px; bottom: 16px; display: flex; flex-direction: column; gap: 8px; z-index: 50; max-width: min(420px, calc(100vw - 32px)); }
.toast { text-align: left; padding: 10px 14px; border-radius: var(--radius); border: 1px solid var(--border-strong); background: var(--surface); color: var(--text); font: inherit; cursor: pointer; box-shadow: 0 4px 16px rgba(0, 0, 0, .12); }
.toast-success { border-color: var(--success); }
.toast-warn { border-color: var(--warn); background: var(--warn-bg); }
.toast-error { border-color: var(--danger); background: var(--danger-bg); }
.dialog { max-width: 460px; width: calc(100vw - 32px); padding: 20px; border: 1px solid var(--border-strong); border-radius: 12px; background: var(--surface); color: var(--text); }
.dialog::backdrop { background: rgba(0, 0, 0, .45); }
.dialog h2 { margin-bottom: 10px; }
.dialog-body { margin-bottom: 16px; }
.dialog-body ul { margin: 8px 0 0; padding-left: 18px; }
.dialog-actions { display: flex; justify-content: flex-end; gap: 8px; }
```

- [ ] **Step 6: Write the views' shells, `login.js` and `app.js`**

Stubs, so the router can import them (replaced in Tasks 5-8):

`internal/web/static/js/views/updates.js`:

```js
import { h } from '../dom.js';

export function mountUpdates(container) {
    container.replaceChildren(h('div', { class: 'page' }, h('h1', {}, 'Updates')));
    return { unmount() {} };
}
```

`internal/web/static/js/views/history.js`:

```js
import { h } from '../dom.js';

export function mountHistory(container) {
    container.replaceChildren(h('div', { class: 'page' }, h('h1', {}, 'History')));
    return { unmount() {} };
}
```

`internal/web/static/js/views/settings.js`:

```js
import { h } from '../dom.js';

export function mountSettings(container) {
    container.replaceChildren(h('div', { class: 'page' }, h('h1', {}, 'Settings')));
    return { unmount() {} };
}
```

`internal/web/static/js/views/login.js`:

```js
import { h, clear } from '../dom.js';
import { api } from '../api.js';

/** mode is 'setup' or 'login'. onDone runs after a session exists. */
export function renderAuth(root, mode, onDone) {
    const setup = mode === 'setup';
    const error = h('p', { class: 'error-text', dataset: { testid: 'auth-error' }, hidden: true });
    const name = h('input', { id: 'auth-name', type: 'text', autocomplete: 'username', required: true, autofocus: true });
    const password = h('input', { id: 'auth-password', type: 'password', autocomplete: setup ? 'new-password' : 'current-password', required: true });
    const submit = h('button', { class: 'btn btn-primary', type: 'submit' }, setup ? 'Create account' : 'Sign in');

    const form = h('form', {
        dataset: { testid: 'auth-form' },
        onsubmit: async (e) => {
            e.preventDefault();
            error.hidden = true;
            submit.setAttribute('aria-busy', 'true');
            try {
                await api.post(setup ? '/api/setup' : '/api/login', { name: name.value, password: password.value });
                onDone();
            } catch (err) {
                error.textContent = err.message;
                error.hidden = false;
            } finally {
                submit.removeAttribute('aria-busy');
            }
        },
    },
        h('div', { class: 'field' }, h('label', { for: 'auth-name' }, 'Name'), name),
        h('div', { class: 'field' }, h('label', { for: 'auth-password' }, 'Password'), password,
            setup ? h('p', { class: 'hint' }, 'Use at least 10 characters.') : null),
        submit,
        error);

    clear(root).append(h('main', { class: 'auth' },
        h('div', { class: 'card' },
            h('h1', {}, setup ? 'Create your admin account' : 'Sign in'),
            h('p', { class: 'lede' }, setup ? 'This account controls updates for the containers on this host.' : 'Sign in to manage container updates.'),
            form)));
    name.focus();
}
```

`internal/web/static/js/app.js`:

```js
import { h, clear } from './dom.js';
import { icon } from './icons.js';
import { api } from './api.js';
import { toast, spinner } from './ui.js';
import { onJobs, refreshJobs } from './jobs.js';
import { relativeTime } from './format.js';
import { renderAuth } from './views/login.js';
import { mountUpdates } from './views/updates.js';
import { mountHistory } from './views/history.js';
import { mountSettings } from './views/settings.js';

const root = document.getElementById('app');

const ROUTES = {
    updates: mountUpdates,
    history: mountHistory,
    settings: mountSettings,
};

let current = null; // { unmount }
let unsubscribeJobs = null;

function parseHash() {
    const parts = location.hash.replace(/^#\/?/, '').split('/').filter(Boolean);
    const view = ROUTES[parts[0]] ? parts[0] : 'updates';
    const rest = ROUTES[parts[0]] ? parts.slice(1) : [];
    return { view, params: { name: rest.length ? decodeURIComponent(rest.join('/')) : '' } };
}

function themeState() {
    return document.documentElement.dataset.theme || 'auto';
}

function cycleTheme() {
    const next = { auto: 'light', light: 'dark', dark: 'auto' }[themeState()];
    if (next === 'auto') delete document.documentElement.dataset.theme;
    else document.documentElement.dataset.theme = next;
    try {
        if (next === 'auto') localStorage.removeItem('nu-theme');
        else localStorage.setItem('nu-theme', next);
    } catch { /* storage blocked: the choice lasts until reload */ }
    return next;
}

function showShell() {
    const main = h('main', { class: 'main', id: 'view' });
    const navLinks = ['updates', 'history', 'settings'].map((v) => h('a', { href: `#/${v}`, dataset: { view: v } }, v[0].toUpperCase() + v.slice(1)));
    const lastCheck = h('span', { class: 'last-check', dataset: { testid: 'last-check' } });
    const checkLabel = h('span', {}, 'Check now');
    const checkBtn = h('button', {
        class: 'btn', type: 'button', dataset: { testid: 'check-button' },
        onclick: async () => {
            try {
                await api.post('/api/check');
                await refreshJobs();
            } catch (e) {
                toast(e.message, e.status === 409 ? 'info' : 'error');
            }
        },
    }, icon('refresh'), checkLabel);
    const themeBtn = h('button', {
        class: 'btn btn-ghost', type: 'button', 'aria-label': `Theme: ${themeState()}. Switch theme`, title: `Theme: ${themeState()}`,
        onclick: () => {
            const next = cycleTheme();
            themeBtn.setAttribute('aria-label', `Theme: ${next}. Switch theme`);
            themeBtn.title = `Theme: ${next}`;
        },
    }, icon('contrast'));
    const signOut = h('button', {
        class: 'btn btn-ghost', type: 'button',
        onclick: async () => {
            try { await api.post('/api/logout'); } catch { /* the cookie is cleared either way */ }
            boot();
        },
    }, 'Sign out');

    clear(root).append(h('div', { class: 'shell' },
        h('header', { class: 'header' },
            h('a', { class: 'brand', href: '#/updates' }, icon('box', 20), 'nextupdate'),
            h('nav', { class: 'nav', 'aria-label': 'Main' }, navLinks),
            h('div', { class: 'header-end' }, lastCheck, checkBtn, themeBtn, signOut)),
        main));

    let tick = null;
    const paintLastCheck = (iso) => {
        const rel = relativeTime(iso);
        lastCheck.textContent = rel ? `Checked ${rel}` : 'Not checked yet';
    };
    unsubscribeJobs?.();
    unsubscribeJobs = onJobs((jobs) => {
        const checking = jobs.running.some((j) => j.key === 'check');
        checkBtn.setAttribute('aria-busy', String(checking));
        clear(checkBtn).append(checking ? spinner() : icon('refresh'), h('span', {}, checking ? 'Checking' : 'Check now'));
        paintLastCheck(jobs.lastCheck);
        clearInterval(tick);
        tick = setInterval(() => paintLastCheck(jobs.lastCheck), 30000);
    });

    // A view may offer select(name) so a change inside the view (for example
    // another container in the updates list) does not rebuild the whole page.
    let mountedView = '';
    const route = () => {
        const { view, params } = parseHash();
        for (const a of navLinks) {
            if (a.dataset.view === view) a.setAttribute('aria-current', 'page');
            else a.removeAttribute('aria-current');
        }
        if (view === mountedView && current && typeof current.select === 'function') {
            current.select(params.name);
            return;
        }
        current?.unmount();
        clear(main);
        mountedView = view;
        current = ROUTES[view](main, params);
    };
    window.onhashchange = route;
    if (!location.hash) history.replaceState(null, '', '#/updates');
    route();
}

export async function boot() {
    current?.unmount();
    current = null;
    unsubscribeJobs?.();
    unsubscribeJobs = null;
    window.onhashchange = null;
    let status;
    try {
        status = await api.get('/api/status');
    } catch (e) {
        clear(root).append(h('main', { class: 'auth' }, h('div', { class: 'card' }, h('h1', {}, "Can't reach nextupdate"), h('p', { class: 'muted' }, e.message))));
        return;
    }
    if (status.setupNeeded) return renderAuth(root, 'setup', boot);
    if (!status.signedIn) return renderAuth(root, 'login', boot);
    showShell();
}

document.addEventListener('nu:signed-out', () => boot());
boot();
```

- [ ] **Step 7: Run to verify it passes**

Run: `PW_WORKERS=2 npx playwright test e2e/auth.spec.js > /tmp/pw.log 2>&1; echo exit=$? >> /tmp/pw.log; tail -8 /tmp/pw.log`
Expected: `4 passed`, `exit=0`. Also confirm there are no console errors about CSP: add nothing, but if a test fails on a blocked script, the log shows `Refused to ...`; fix the offending inline code.

- [ ] **Step 8: Falsify the sign-out redirect**

Temporarily remove the `document.addEventListener('nu:signed-out', ...)` line; `a session that ends while the app is open goes back to sign in` must FAIL. Restore.

- [ ] **Step 9: Commit**

```bash
git add internal/web e2e
git commit -m "ui shell: styles, sign in, header, theme"
```

---

### Task 5: Updates view (list, detail, changelog, actions)

**Files:**
- Modify: `internal/web/static/js/markdown.js` (add the renderer), `internal/web/static/js/views/updates.js` (replace), `internal/web/static/css/app.css` (append)
- Test: `e2e/updates.spec.js`

**Interfaces:**
- Consumes: Tasks 1-4, the API of Plan 3a.
- Produces:
  - `renderMarkdown(src: string): DocumentFragment` in `markdown.js` (headings shifted down three levels and capped at `h6`, links get `target="_blank" rel="noopener noreferrer"`)
  - Route `#/updates` and `#/updates/<name>`. Layout: `.split` grid; left `.list-pane`, right `.detail-pane[data-testid=detail]`.
  - List: three groups, each `<section data-testid="group-breaking|group-updates|group-current">` with an `h2.group-title` (`Breaking`, `Updates available`, `Up to date`) and a count; a group with no rows is not rendered. Rows are `<a class="row" data-name href="#/updates/<name>">` with name, image, a version line (`1.98.0 to 2.0.0`, or the current version unknown as the image), a change badge (`Breaking` danger, `Major`/`Minor`/`Patch` accent) and a spinner while the container is busy. The selected row has `aria-current="true"`.
  - On a wide screen (`min-width: 761px`) with no name in the URL the first row is selected with `location.replace`.
  - Detail: header with name, image and a `Compose` or `Docker run` badge; a `Policy` select `[data-testid=policy-select]` (options `Notify only`, `Update automatically`, `Never`) that saves on change; when an update exists: version line, `Detected <relative time>`, primary button `Update` `[data-testid=update-button]`, the breaking box `[data-testid=breaking-box]` (title `Breaking update`, one list item per reason) and the changelog `[data-testid=changelog]`; when up to date: text `Up to date`, the latest history entry for this container, and, when a rollback is possible, a secondary button `Roll back` `[data-testid=rollback-button]`; a `<details>` `Container settings` with health check URL, repository (`owner/name`) and verify window seconds, and a `Save` button `[data-testid=settings-save]`.
  - Changelog: fetched from `/api/updates/<name>/changelog`; each release shows tag, name, relative date, a link `View on GitHub` when it has a URL, and the Markdown body via `renderMarkdown`; an empty list shows `No release notes found.` plus a link to `https://github.com/<repo>` when a repo is known.
  - Update: a breaking update first opens the confirm dialog (title `Update <name>?`, body lists the reasons, confirm button `Update anyway`, danger style); then `POST /api/updates/<name>/apply`. While the job runs the row and the detail show a spinner and the update button reads `Updating` (`aria-busy`). When the job finishes the view reloads its data and shows a toast built from the newest history entry: `ok` → `Updated <name>` (success), `rolled_back` → `Update of <name> was rolled back: <reason>` (warn), `failed` → `Update of <name> failed: <reason>` (error); a failed job in `/api/jobs` shows `error` as a toast.
  - Rollback: `POST /api/containers/<name>/rollback`, same busy and toast handling; a toast for a finished manual rollback reads `Rolled back <name>`.
  - Keyboard (ignored while typing in a field or while a dialog is open): `j`/`ArrowDown` next row, `k`/`ArrowUp` previous row, `u` update the selected container (same path as the button, including the breaking confirmation), `c` check now. A legend under the list: `<kbd>j</kbd> <kbd>k</kbd> move`, `<kbd>u</kbd> update`, `<kbd>c</kbd> check`.
  - On a narrow screen (`max-width: 760px`) only one pane shows: the list, or, when a name is in the URL, the detail with a back link `[data-testid=back]` to `#/updates`.

- [ ] **Step 1: Write the failing e2e spec**

`e2e/updates.spec.js`:

```js
const { test, expect, signIn } = require('./fixtures');

test.beforeEach(async ({ page }) => {
    await signIn(page);
});

const row = (page, name) => page.locator(`a.row[data-name="${name}"]`);

test('groups the containers and selects the first one', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByTestId('group-breaking').locator('a.row')).toHaveCount(1);
    await expect(page.getByTestId('group-updates').locator('a.row')).toHaveCount(3);
    await expect(page.getByTestId('group-current').locator('a.row')).toHaveCount(2);
    await expect(page.getByTestId('group-breaking').locator('.group-title')).toContainText('Breaking');
    await expect(row(page, 'immich')).toContainText('1.98.0 to 2.0.0');
    await expect(page).toHaveURL(/#\/updates\/immich$/);
    await expect(row(page, 'immich')).toHaveAttribute('aria-current', 'true');
    await expect(page.getByTestId('detail')).toContainText('immich');
});

test('shows the breaking reasons and the release notes, and keeps untrusted markup inert', async ({ page }) => {
    const dialogs = [];
    page.on('dialog', (d) => { dialogs.push(d.message()); d.dismiss(); });
    await page.goto('/#/updates/immich');

    await expect(page.getByTestId('breaking-box')).toContainText('Major version change from 1.98.0 to 2.0.0.');
    const notes = page.getByTestId('changelog');
    await expect(notes).toContainText('Database migration runs on first start.');
    await expect(notes.locator('.release')).toHaveCount(2); // 2.0.0 and 1.99.0, not 1.98.0

    // The script line is visible text, not a script; the javascript: link is not a link.
    await expect(notes).toContainText('<script>alert(1)</script>');
    await expect(notes.locator('script')).toHaveCount(0);
    await expect(notes.locator('a[href^="javascript:" i]')).toHaveCount(0);
    await expect(notes.getByText('click me')).toBeVisible();
    const docs = notes.getByRole('link', { name: 'docs' });
    await expect(docs).toHaveAttribute('href', 'https://example.com/docs');
    await expect(docs).toHaveAttribute('target', '_blank');
    await expect(docs).toHaveAttribute('rel', 'noopener noreferrer');
    await expect(notes.locator('pre code')).toContainText('DB_HOST=database');
    expect(dialogs).toEqual([]);
});

test('a container without a known repo says there are no release notes', async ({ page }) => {
    await page.goto('/#/updates/flaky');
    await expect(page.getByTestId('changelog')).toContainText('No release notes found.');
});

test('updating a patch release runs in the background and moves the container to up to date', async ({ page }) => {
    await page.goto('/#/updates/sonarr');
    await page.getByTestId('update-button').click();
    await expect(row(page, 'sonarr').locator('.spinner')).toBeVisible();
    await expect(page.getByTestId('update-button')).toHaveAttribute('aria-busy', 'true');
    await expect(page.getByTestId('toast').filter({ hasText: 'Updated sonarr' })).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId('group-current').locator('a.row[data-name="sonarr"]')).toBeVisible();
    await expect(page.getByTestId('group-updates').locator('a.row[data-name="sonarr"]')).toHaveCount(0);
});

test('a breaking update asks first, and cancelling changes nothing', async ({ page }) => {
    await page.goto('/#/updates/immich');
    await page.getByTestId('update-button').click();
    const dialog = page.getByTestId('confirm-dialog');
    await expect(dialog).toContainText('Update immich?');
    await expect(dialog).toContainText('Major version change');
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByTestId('group-breaking').locator('a.row[data-name="immich"]')).toBeVisible();
    await expect(row(page, 'immich').locator('.spinner')).toHaveCount(0);

    await page.getByTestId('update-button').click();
    await page.getByTestId('confirm-yes').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'Updated immich' })).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId('group-current').locator('a.row[data-name="immich"]')).toBeVisible();
});

test('an update that fails its health check is reported as rolled back and stays available', async ({ page }) => {
    await page.goto('/#/updates/flaky');
    await page.getByTestId('update-button').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'Update of flaky was rolled back: verify: healthcheck unhealthy' })).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId('group-updates').locator('a.row[data-name="flaky"]')).toBeVisible();
});

test('a container that was updated before can be rolled back, and then offers the update again', async ({ page }) => {
    await page.goto('/#/updates/jellyfin');
    await expect(page.getByTestId('detail')).toContainText('Up to date');
    await page.getByTestId('rollback-button').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'Rolled back jellyfin' })).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId('group-updates').locator('a.row[data-name="jellyfin"]')).toHaveCount(0); // jellyfin has no seeded update to offer again
    await expect(page.getByTestId('rollback-button')).toBeVisible();
    await expect(page.getByTestId('update-button')).toHaveCount(0);
});

test('a container with no earlier update has no rollback button', async ({ page }) => {
    await page.goto('/#/updates/postgres');
    await expect(page.getByTestId('rollback-button')).toHaveCount(0);
});

test('the policy select saves on change and survives a reload', async ({ page }) => {
    await page.goto('/#/updates/vaultwarden');
    await expect(page.getByTestId('policy-select')).toHaveValue('notify');
    await page.getByTestId('policy-select').selectOption('auto');
    await expect(page.getByTestId('toast').filter({ hasText: 'Policy for vaultwarden is now auto' })).toBeVisible();
    await page.reload();
    await expect(page.getByTestId('policy-select')).toHaveValue('auto');
});

test('container settings validate and save', async ({ page }) => {
    await page.goto('/#/updates/vaultwarden');
    await page.getByText('Container settings').click();
    await page.getByLabel('Repository').fill('not a repo');
    await page.getByTestId('settings-save').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'owner/name' })).toBeVisible();

    await page.getByLabel('Repository').fill('dani-garcia/vaultwarden');
    await page.getByLabel('Health check URL').fill('http://vaultwarden:80/alive');
    await page.getByLabel('Verify window').fill('90');
    await page.getByTestId('settings-save').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'Settings for vaultwarden saved' })).toBeVisible();
    await page.reload();
    await page.getByText('Container settings').click();
    await expect(page.getByLabel('Health check URL')).toHaveValue('http://vaultwarden:80/alive');
    await expect(page.getByLabel('Verify window')).toHaveValue('90');
});

test('keyboard: j and k move, u updates, c checks', async ({ page }) => {
    await page.goto('/');
    await expect(page).toHaveURL(/#\/updates\/immich$/);
    await page.keyboard.press('j');
    await expect(page).toHaveURL(/#\/updates\/(flaky|sonarr|vaultwarden)$/);
    const second = page.url().split('/').pop();
    await page.keyboard.press('k');
    await expect(page).toHaveURL(/#\/updates\/immich$/);
    await page.keyboard.press('ArrowDown');
    await expect(page).toHaveURL(new RegExp(`#/updates/${second}$`));

    await page.keyboard.press('c');
    await expect(page.getByTestId('check-button')).toHaveAttribute('aria-busy', 'true');
    await expect(page.getByTestId('check-button')).toHaveAttribute('aria-busy', 'false', { timeout: 10000 });

    await page.goto('/#/updates/sonarr');
    await page.keyboard.press('u');
    await expect(row(page, 'sonarr').locator('.spinner')).toBeVisible();
    await expect(page.getByTestId('toast').filter({ hasText: 'Updated sonarr' })).toBeVisible({ timeout: 15000 });
});

test('typing in a field does not trigger shortcuts', async ({ page }) => {
    await page.goto('/#/updates/sonarr');
    await page.getByText('Container settings').click();
    await page.getByLabel('Repository').click();
    await page.keyboard.type('ju');
    await expect(row(page, 'sonarr').locator('.spinner')).toHaveCount(0);
    await expect(page).toHaveURL(/#\/updates\/sonarr$/);
});

test('the legend lists the shortcuts', async ({ page }) => {
    await page.goto('/');
    const legend = page.locator('.legend');
    for (const key of ['j', 'k', 'u', 'c']) await expect(legend.locator('kbd', { hasText: new RegExp(`^${key}$`) })).toBeVisible();
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `PW_WORKERS=2 npx playwright test e2e/updates.spec.js > /tmp/pw.log 2>&1; echo exit=$? >> /tmp/pw.log; tail -6 /tmp/pw.log`
Expected: `exit=1` (the view is still a stub).

- [ ] **Step 3: Add the Markdown renderer**

Append to `internal/web/static/js/markdown.js`:

```js
import { h } from './dom.js';

function renderInline(nodes) {
    return nodes.map((n) => {
        switch (n.t) {
            case 'code': return h('code', {}, n.v);
            case 'strong': return h('strong', {}, renderInline(n.c));
            case 'em': return h('em', {}, renderInline(n.c));
            case 'link': return h('a', { href: n.href, target: '_blank', rel: 'noopener noreferrer' }, renderInline(n.c));
            default: return n.v;
        }
    });
}

function renderBlock(b) {
    switch (b.t) {
        case 'heading': return h(`h${Math.min(b.level + 3, 6)}`, {}, renderInline(b.c));
        case 'ul': return h('ul', {}, b.items.map((i) => h('li', {}, renderInline(i))));
        case 'ol': return h('ol', {}, b.items.map((i) => h('li', {}, renderInline(i))));
        case 'code': return h('pre', {}, h('code', {}, b.v));
        case 'quote': return h('blockquote', {}, b.c.map(renderBlock));
        case 'hr': return h('hr');
        default: return h('p', {}, renderInline(b.c));
    }
}

/** Builds DOM for release notes. Only nodes made here reach the page. */
export function renderMarkdown(src) {
    const frag = document.createDocumentFragment();
    for (const b of parseMarkdown(src)) frag.append(renderBlock(b));
    return frag;
}
```

Move the `import { h } from './dom.js';` line to the top of the file so the parser tests (which run in Node) still work: `dom.js` only touches `document` inside functions, so importing it in Node is safe.

- [ ] **Step 4: Write the updates view**

Replace `internal/web/static/js/views/updates.js`:

```js
import { h, clear } from '../dom.js';
import { icon } from '../icons.js';
import { api } from '../api.js';
import { toast, badge, spinner, confirmDialog } from '../ui.js';
import { onJobs, refreshJobs } from '../jobs.js';
import { renderMarkdown } from '../markdown.js';
import { versionChange, relativeTime, outcomeLabel } from '../format.js';

const enc = encodeURIComponent;
const wide = () => window.matchMedia('(min-width: 761px)').matches;
const CHANGE_LABEL = { major: 'Major', minor: 'Minor', patch: 'Patch' };

export function mountUpdates(container, params) {
    let containers = [];
    let updates = new Map();
    let history = [];
    let jobs = { running: [], failed: [] };
    let selected = params.name || '';
    let alive = true;
    const tracking = new Map(); // name -> { kind, topId } for jobs this view started
    const shownFailures = new Set();

    const list = h('nav', { class: 'list', 'aria-label': 'Containers' });
    const legend = h('div', { class: 'legend faint' },
        h('span', {}, h('kbd', {}, 'j'), ' ', h('kbd', {}, 'k'), ' move'),
        h('span', {}, h('kbd', {}, 'u'), ' update'),
        h('span', {}, h('kbd', {}, 'c'), ' check'));
    const detail = h('section', { class: 'detail-pane', dataset: { testid: 'detail' }, 'aria-live': 'polite' });
    const split = h('div', { class: 'split' }, h('div', { class: 'list-pane' }, list, legend), detail);
    clear(container).append(split);

    const busyNames = () => new Set(jobs.running.filter((j) => j.key !== 'check').map((j) => j.key));
    const isBusy = (name) => busyNames().has(name);
    const latestOk = (name) => history.find((e) => e.container === name && e.outcome === 'ok' && e.fromImage && e.fromImage !== e.toImage);

    async function load() {
        try {
            [containers, history] = await Promise.all([api.get('/api/containers'), api.get('/api/history?limit=100')]);
            const ups = await api.get('/api/updates');
            updates = new Map(ups.map((u) => [u.container, u]));
        } catch (e) {
            if (e.status !== 401) toast(e.message, 'error');
            return;
        }
        if (!alive) return;
        if (!selected && wide()) {
            const first = orderedNames()[0];
            if (first) {
                selected = first;
                location.replace(`#/updates/${enc(first)}`);
            }
        }
        render();
    }

    function orderedNames() {
        const groups = grouped();
        return [...groups.breaking, ...groups.updates, ...groups.current].map((c) => c.name);
    }

    function grouped() {
        const g = { breaking: [], updates: [], current: [] };
        for (const c of containers) {
            const u = updates.get(c.name);
            if (u && u.breaking) g.breaking.push(c);
            else if (u) g.updates.push(c);
            else g.current.push(c);
        }
        const byName = (a, b) => a.name.localeCompare(b.name);
        for (const k of Object.keys(g)) g[k].sort(byName);
        return g;
    }

    function render() {
        split.classList.toggle('detail-open', Boolean(selected));
        renderList();
        renderDetail();
    }

    function renderList() {
        const g = grouped();
        clear(list);
        if (!containers.length) {
            list.append(h('p', { class: 'empty' }, 'No containers found. Check that nextupdate can reach the Docker socket.'));
            return;
        }
        const section = (key, title, items) => items.length ? h('section', { dataset: { testid: `group-${key}` } },
            h('h2', { class: 'group-title' }, title, h('span', { class: 'count' }, String(items.length))),
            items.map(rowFor)) : null;
        list.append(
            section('breaking', 'Breaking', g.breaking),
            section('updates', 'Updates available', g.updates),
            section('current', 'Up to date', g.current));
    }

    function rowFor(c) {
        const u = updates.get(c.name);
        const change = u ? versionChange(u.oldVersion, u.newVersion) : '';
        return h('a', { class: 'row', href: `#/updates/${enc(c.name)}`, dataset: { name: c.name }, 'aria-current': selected === c.name ? 'true' : null },
            h('span', { class: 'row-main' },
                h('span', { class: 'row-name' }, c.name),
                h('span', { class: 'row-sub faint' }, u && (u.oldVersion || u.newVersion) ? `${u.oldVersion || 'unknown'} to ${u.newVersion || 'unknown'}` : c.image)),
            h('span', { class: 'row-side' },
                isBusy(c.name) ? spinner() : null,
                u && u.breaking ? badge('Breaking', 'danger') : (CHANGE_LABEL[change] ? badge(CHANGE_LABEL[change], 'accent') : (u ? badge('Update', 'accent') : null))));
    }

    function renderDetail() {
        clear(detail);
        const c = containers.find((x) => x.name === selected);
        if (!c) {
            detail.append(h('div', { class: 'empty-detail' }, selected ? `There is no container called ${selected}.` : 'Select a container to see its details.'));
            return;
        }
        const u = updates.get(c.name);
        const busy = isBusy(c.name);

        const policy = h('select', {
            id: 'policy', dataset: { testid: 'policy-select' }, 'aria-label': 'Update policy',
            onchange: () => saveSettings(c, { policy: policy.value }, `Policy for ${c.name} is now ${policy.value}`),
        },
            h('option', { value: 'notify' }, 'Notify only'),
            h('option', { value: 'auto' }, 'Update automatically'),
            h('option', { value: 'never' }, 'Never'));
        policy.value = c.policy;

        const back = h('a', { class: 'back', href: '#/updates', dataset: { testid: 'back' } }, icon('back'), 'Containers');

        detail.append(
            back,
            h('header', { class: 'detail-head' },
                h('div', {},
                    h('h1', {}, c.name),
                    h('p', { class: 'muted image-line' }, c.image, ' ', badge(c.source === 'compose' ? 'Compose' : 'Docker run'))),
                h('div', { class: 'policy' }, h('label', { for: 'policy' }, 'Policy'), policy)),
            u ? updateSection(c, u, busy) : currentSection(c, busy),
            settingsSection(c));
        if (u) loadChangelog(c, u);
    }

    function updateButton(c, u, busy) {
        return h('button', {
            class: 'btn btn-primary', type: 'button', dataset: { testid: 'update-button' }, 'aria-busy': String(busy),
            onclick: () => startUpdate(c, u),
        }, busy ? spinner() : null, busy ? 'Updating' : 'Update');
    }

    function updateSection(c, u, busy) {
        const change = versionChange(u.oldVersion, u.newVersion);
        return h('div', {},
            h('div', { class: 'action-bar' },
                h('div', {},
                    h('p', { class: 'version-line' }, `${u.oldVersion || 'unknown'} to ${u.newVersion || 'unknown'}`, ' ', CHANGE_LABEL[change] ? badge(CHANGE_LABEL[change], 'accent') : null),
                    h('p', { class: 'faint' }, u.detectedAt ? `Detected ${relativeTime(u.detectedAt)}` : '')),
                updateButton(c, u, busy)),
            u.breaking ? h('div', { class: 'box box-danger', dataset: { testid: 'breaking-box' } },
                h('h3', {}, icon('alert'), ' Breaking update'),
                h('ul', {}, u.reasons.map((r) => h('li', {}, r))),
                h('p', { class: 'hint' }, 'A rollback restores the container, not its data. Back up before you update.')) : null,
            h('section', { class: 'changelog', dataset: { testid: 'changelog' } }, h('h2', {}, 'Release notes'), h('div', { class: 'changelog-body' }, h('p', { class: 'faint' }, 'Loading release notes…'))));
    }

    function currentSection(c, busy) {
        const last = history.find((e) => e.container === c.name);
        const canRollback = Boolean(latestOk(c.name));
        return h('div', {},
            h('div', { class: 'action-bar' },
                h('div', {},
                    h('p', { class: 'version-line' }, icon('check'), ' Up to date'),
                    last ? h('p', { class: 'faint' }, `Last change: ${outcomeLabel(last.outcome).toLowerCase()} ${relativeTime(last.finishedAt)}`) : h('p', { class: 'faint' }, 'No updates recorded yet.')),
                canRollback ? h('button', {
                    class: 'btn', type: 'button', dataset: { testid: 'rollback-button' }, 'aria-busy': String(busy),
                    onclick: () => startRollback(c),
                }, busy ? spinner() : null, busy ? 'Rolling back' : 'Roll back') : null));
    }

    async function loadChangelog(c, u) {
        const body = detail.querySelector('.changelog-body');
        try {
            const data = await api.get(`/api/updates/${enc(c.name)}/changelog`);
            if (!alive || selected !== c.name || !body.isConnected) return;
            clear(body);
            if (!data.releases.length) {
                body.append(h('p', { class: 'muted' }, 'No release notes found.'),
                    data.repo ? h('p', {}, h('a', { href: `https://github.com/${data.repo}/releases`, target: '_blank', rel: 'noopener noreferrer' }, 'Open releases on GitHub ', icon('external', 14))) : null);
                return;
            }
            for (const r of data.releases) {
                body.append(h('article', { class: 'release' },
                    h('header', {},
                        h('h3', {}, r.tag, r.name && r.name !== r.tag ? ` · ${r.name}` : ''),
                        h('span', { class: 'faint' }, relativeTime(r.publishedAt)),
                        r.url ? h('a', { href: r.url, target: '_blank', rel: 'noopener noreferrer' }, 'View on GitHub ', icon('external', 14)) : null),
                    h('div', { class: 'md' }, renderMarkdown(r.body))));
            }
        } catch (e) {
            if (body.isConnected) body.replaceChildren(h('p', { class: 'error-text' }, e.message));
        }
    }

    function settingsSection(c) {
        const url = h('input', { id: 's-url', type: 'url', value: c.httpUrl || '', placeholder: 'http://container:8080/health' });
        const repo = h('input', { id: 's-repo', type: 'text', value: c.repo || '', placeholder: 'owner/name' });
        const win = h('input', { id: 's-window', type: 'number', min: '0', max: '3600', value: String(c.verifyWindowSeconds || 0) });
        return h('details', { class: 'card settings' },
            h('summary', {}, 'Container settings'),
            h('div', { class: 'field' }, h('label', { for: 's-url' }, 'Health check URL'), url, h('p', { class: 'hint' }, 'After an update this must answer with a 2xx status. Leave empty to rely on the Docker health check.')),
            h('div', { class: 'field' }, h('label', { for: 's-repo' }, 'Repository'), repo, h('p', { class: 'hint' }, 'The GitHub repository the release notes come from, as owner/name. Leave empty to detect it.')),
            h('div', { class: 'field' }, h('label', { for: 's-window' }, 'Verify window'), win, h('p', { class: 'hint' }, 'Seconds a new container gets to become healthy. 0 uses the default.')),
            h('button', {
                class: 'btn', type: 'button', dataset: { testid: 'settings-save' },
                onclick: () => saveSettings(c, { httpUrl: url.value, repo: repo.value, verifyWindowSeconds: Number(win.value || 0) }, `Settings for ${c.name} saved`),
            }, 'Save'));
    }

    async function saveSettings(c, patch, message) {
        const body = { policy: c.policy, httpUrl: c.httpUrl || '', repo: c.repo || '', verifyWindowSeconds: c.verifyWindowSeconds || 0, ...patch };
        try {
            await api.put(`/api/containers/${enc(c.name)}/settings`, body);
            Object.assign(c, { policy: body.policy, httpUrl: body.httpUrl, repo: body.repo, verifyWindowSeconds: body.verifyWindowSeconds });
            toast(message, 'success');
        } catch (e) {
            toast(e.message, 'error');
            renderDetail();
        }
    }

    async function startUpdate(c, u) {
        if (isBusy(c.name)) return;
        if (u.breaking) {
            const ok = await confirmDialog({
                title: `Update ${c.name}?`,
                body: h('div', {}, h('p', {}, 'This update looks breaking:'), h('ul', {}, u.reasons.map((r) => h('li', {}, r))), h('p', {}, 'A rollback restores the container, not its data.')),
                confirmLabel: 'Update anyway', danger: true,
            });
            if (!ok) return;
        }
        await startJob(c.name, 'update', `/api/updates/${enc(c.name)}/apply`);
    }

    function startRollback(c) {
        return startJob(c.name, 'rollback', `/api/containers/${enc(c.name)}/rollback`);
    }

    async function startJob(name, kind, path) {
        try {
            await api.post(path);
        } catch (e) {
            toast(e.message, e.status === 409 ? 'info' : 'error');
            return;
        }
        tracking.set(name, { kind, topId: history.length ? history[0].id : 0 });
        await refreshJobs();
    }

    function summarize(name, kind) {
        const t = tracking.get(name);
        const entry = history.find((e) => e.container === name && e.id > t.topId);
        if (!entry) return;
        if (kind === 'rollback') {
            if (entry.outcome === 'ok') toast(`Rolled back ${name}`, 'success');
            else toast(`Rollback of ${name} ${entry.outcome === 'rolled_back' ? 'was undone' : 'failed'}: ${entry.reason}`, 'error');
        } else if (entry.outcome === 'ok') toast(`Updated ${name}`, 'success');
        else if (entry.outcome === 'rolled_back') toast(`Update of ${name} was rolled back: ${entry.reason}`, 'warn');
        else toast(`Update of ${name} failed: ${entry.reason}`, 'error');
    }

    const stop = onJobs(async (now, prev) => {
        jobs = now;
        const running = new Set(now.running.map((j) => j.key));
        const finished = [...tracking.keys()].filter((n) => !running.has(n));
        const anyFinished = prev.running.some((j) => !running.has(j.key));
        for (const f of now.failed || []) {
            const id = `${f.key}|${f.at}`;
            if (!shownFailures.has(id)) {
                shownFailures.add(id);
                if (tracking.has(f.key)) toast(`${f.kind === 'rollback' ? 'Rollback' : 'Update'} of ${f.key} failed: ${f.error}`, 'error');
            }
        }
        if (anyFinished || finished.length) {
            await load();
            for (const name of finished) {
                if (!(now.failed || []).some((f) => f.key === name && !shownFailures.has(`${f.key}|${f.at}`))) summarize(name, tracking.get(name).kind);
                tracking.delete(name);
            }
        } else if (alive && containers.length) {
            render();
        }
    });

    function onKey(e) {
        if (e.metaKey || e.ctrlKey || e.altKey) return;
        if (document.querySelector('dialog[open]')) return;
        const t = e.target;
        if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
        const names = orderedNames();
        const i = names.indexOf(selected);
        const go = (n) => { if (n) location.hash = `#/updates/${enc(n)}`; };
        if (e.key === 'j' || e.key === 'ArrowDown') { e.preventDefault(); go(names[Math.min(i + 1, names.length - 1)]); }
        else if (e.key === 'k' || e.key === 'ArrowUp') { e.preventDefault(); go(names[Math.max(i - 1, 0)]); }
        else if (e.key === 'u') {
            const c = containers.find((x) => x.name === selected);
            const u = c && updates.get(c.name);
            if (c && u) startUpdate(c, u);
        } else if (e.key === 'c') {
            document.querySelector('[data-testid="check-button"]')?.click();
        }
    }
    document.addEventListener('keydown', onKey);

    load();
    return {
        select(name) {
            selected = name;
            render();
        },
        unmount() {
            alive = false;
            stop();
            document.removeEventListener('keydown', onKey);
        },
    };
}
```

- [ ] **Step 5: Append the styles**

Append to `internal/web/static/css/app.css`:

```css
/* Updates: list and detail */
.split { display: grid; grid-template-columns: minmax(280px, 340px) 1fr; height: 100%; }
.list-pane { display: flex; flex-direction: column; border-right: 1px solid var(--border); background: var(--surface); min-height: 0; }
.list { flex: 1; overflow: auto; padding: 8px 0; }
.legend { display: flex; flex-wrap: wrap; gap: 4px 14px; padding: 8px 16px; border-top: 1px solid var(--border); font-size: 12px; }
.group-title { display: flex; align-items: center; gap: 8px; padding: 12px 16px 6px; font-size: 12px; text-transform: none; letter-spacing: .02em; color: var(--text-2); }
.group-title .count { background: var(--surface-2); border-radius: 999px; padding: 0 7px; font-weight: 500; }
.row { display: flex; align-items: center; gap: 8px; padding: 8px 16px; color: var(--text); text-decoration: none; border-left: 3px solid transparent; }
.row:hover { background: var(--surface-2); }
.row[aria-current="true"] { background: var(--accent-bg); border-left-color: var(--accent); }
.row-main { display: flex; flex-direction: column; min-width: 0; flex: 1; }
.row-name { font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row-sub { font-size: 12px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.row-side { display: flex; align-items: center; gap: 8px; }
.empty, .empty-detail { color: var(--text-2); padding: 24px 16px; }
.detail-pane { overflow: auto; padding: 20px 24px 48px; min-width: 0; }
.back { display: none; align-items: center; gap: 4px; margin-bottom: 12px; text-decoration: none; }
.detail-head { display: flex; align-items: flex-start; gap: 16px; justify-content: space-between; flex-wrap: wrap; margin-bottom: 16px; }
.image-line { margin: 4px 0 0; word-break: break-all; }
.policy { min-width: 200px; }
.action-bar { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 14px 16px; background: var(--surface); border: 1px solid var(--border); border-radius: 12px; margin-bottom: 16px; }
.action-bar p { margin: 0; }
.version-line { font-weight: 600; font-size: 15px; }
.box { border-radius: 12px; padding: 12px 16px; margin-bottom: 16px; }
.box-danger { background: var(--danger-bg); color: var(--text); border: 1px solid var(--danger); }
.box-danger h3 { color: var(--danger); margin-bottom: 6px; }
.box ul { margin: 0 0 6px; padding-left: 18px; }
.changelog h2 { margin-bottom: 8px; }
.release { background: var(--surface); border: 1px solid var(--border); border-radius: 12px; padding: 12px 16px; margin-bottom: 12px; }
.release > header { display: flex; align-items: baseline; gap: 12px; flex-wrap: wrap; margin-bottom: 6px; }
.release > header a { margin-left: auto; font-size: 13px; }
.md { overflow-wrap: anywhere; }
.md h4, .md h5, .md h6 { margin: 10px 0 4px; }
.md ul, .md ol { margin: 0 0 8px; padding-left: 20px; }
.md pre { background: var(--surface-2); border-radius: var(--radius); padding: 8px 10px; overflow: auto; margin: 0 0 8px; }
.md code { background: var(--surface-2); border-radius: 4px; padding: 0 4px; }
.md pre code { background: none; padding: 0; }
.md blockquote { margin: 0 0 8px; padding-left: 12px; border-left: 3px solid var(--border-strong); color: var(--text-2); }
.settings summary { cursor: pointer; font-weight: 600; }
.settings[open] summary { margin-bottom: 12px; }
@media (max-width: 760px) {
    .split { grid-template-columns: 1fr; }
    .split.detail-open .list-pane { display: none; }
    .split:not(.detail-open) .detail-pane { display: none; }
    .back { display: inline-flex; }
    .detail-pane { padding: 16px; }
    .action-bar { flex-direction: column; align-items: stretch; }
    .header { gap: 8px; padding: 0 10px; }
    .last-check { display: none; }
    .btn { min-height: 36px; }
}
```

- [ ] **Step 6: Run to verify it passes**

Run: `node --test web-test/ 2>&1 | tail -4` (the markdown parser tests must still pass).
Run: `PW_WORKERS=2 npx playwright test e2e/updates.spec.js > /tmp/pw.log 2>&1; echo exit=$? >> /tmp/pw.log; tail -12 /tmp/pw.log`
Expected: all specs pass, `exit=0`. If a test fails, read the trace or screenshot in `test-results/`, fix the view (not the test), and rerun only that spec with `-g "<title>"`.

- [ ] **Step 7: Falsify three behaviors**

Run each mutation on its own, confirm the named test FAILS, then restore:
1. In `parseInline`, allow every link (`SAFE_URL = /^.+$/`): `keeps untrusted markup inert` must fail.
2. In `startUpdate`, skip the confirmation (`if (false && u.breaking)`): `a breaking update asks first` must fail.
3. In `onKey`, remove the `INPUT` check: `typing in a field does not trigger shortcuts` must fail.

- [ ] **Step 8: Commit**

```bash
git add internal/web e2e
git commit -m "updates view: list, detail, release notes, update and rollback"
```

---

### Task 6: History view

**Files:**
- Modify: `internal/web/static/js/views/history.js` (replace), `internal/web/static/css/app.css` (append)
- Test: `e2e/history.spec.js`

**Interfaces:**
- Produces: route `#/history`; page heading `History`; a list of entries newest first, each `<article data-testid="history-entry" data-container>` with: container name, outcome badge (`Updated` success, `Rolled back` warn, `Failed` danger; manual rollbacks read `Rolled back` too because the reason starts with `Manual rollback.`, shown as text), relative time with the full date in a `title`, image reference, `from` and `to` image IDs as `shortImageId` in `<code>`, the reason when there is one, and a `<details>` `Steps` listing the log lines in a `<pre>`. An empty history shows `Nothing has been updated yet.` A `Container` filter select (`All containers` plus every container that appears) narrows the list.

- [ ] **Step 1: Write the failing e2e spec**

`e2e/history.spec.js`:

```js
const { test, expect, signIn } = require('./fixtures');

test.beforeEach(async ({ page }) => {
    await signIn(page);
});

test('lists the earlier update with its steps', async ({ page }) => {
    await page.goto('/#/history');
    const entries = page.getByTestId('history-entry');
    await expect(entries).toHaveCount(1);
    const e = entries.first();
    await expect(e).toContainText('jellyfin');
    await expect(e).toContainText('Updated');
    await expect(e).toContainText('2 days ago');
    await e.getByText('Steps').click();
    await expect(e).toContainText('verify jellyfin');
});

test('shows new entries, newest first, with the reason of a rollback', async ({ page }) => {
    await page.goto('/#/updates/flaky');
    await page.getByTestId('update-button').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'was rolled back' })).toBeVisible({ timeout: 15000 });
    await page.getByRole('link', { name: 'History' }).click();
    const entries = page.getByTestId('history-entry');
    await expect(entries).toHaveCount(2);
    await expect(entries.first()).toContainText('flaky');
    await expect(entries.first()).toContainText('Rolled back');
    await expect(entries.first()).toContainText('verify: healthcheck unhealthy');
    await expect(entries.nth(1)).toContainText('jellyfin');
});

test('the container filter narrows the list', async ({ page }) => {
    await page.goto('/#/updates/sonarr');
    await page.getByTestId('update-button').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'Updated sonarr' })).toBeVisible({ timeout: 15000 });
    await page.getByRole('link', { name: 'History' }).click();
    await expect(page.getByTestId('history-entry')).toHaveCount(2);
    await page.getByLabel('Container').selectOption('sonarr');
    await expect(page.getByTestId('history-entry')).toHaveCount(1);
    await expect(page.getByTestId('history-entry').first()).toContainText('sonarr');
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `PW_WORKERS=2 npx playwright test e2e/history.spec.js > /tmp/pw.log 2>&1; echo exit=$? >> /tmp/pw.log; tail -5 /tmp/pw.log`
Expected: `exit=1`.

- [ ] **Step 3: Implement**

Replace `internal/web/static/js/views/history.js`:

```js
import { h, clear } from '../dom.js';
import { api } from '../api.js';
import { badge } from '../ui.js';
import { relativeTime, shortImageId, outcomeLabel } from '../format.js';
import { onJobs } from '../jobs.js';

const KIND = { ok: 'success', rolled_back: 'warn', failed: 'danger' };

export function mountHistory(container) {
    let entries = [];
    let filter = '';
    let alive = true;
    const listEl = h('div', { class: 'history' });
    const select = h('select', { id: 'history-filter', onchange: () => { filter = select.value; render(); } });
    const page = h('div', { class: 'page' },
        h('div', { class: 'row-flex' },
            h('h1', {}, 'History'), h('span', { class: 'spacer' }),
            h('label', { for: 'history-filter', class: 'sr-only' }, 'Container'), select),
        listEl);
    clear(container).append(page);

    function render() {
        const names = [...new Set(entries.map((e) => e.container))].sort();
        clear(select).append(h('option', { value: '' }, 'All containers'), names.map((n) => h('option', { value: n }, n)));
        select.value = names.includes(filter) ? filter : '';
        if (!names.includes(filter)) filter = '';
        const shown = entries.filter((e) => !filter || e.container === filter);
        clear(listEl);
        if (!shown.length) {
            listEl.append(h('p', { class: 'muted' }, 'Nothing has been updated yet.'));
            return;
        }
        for (const e of shown) listEl.append(entry(e));
    }

    function entry(e) {
        return h('article', { class: 'card history-entry', dataset: { testid: 'history-entry', container: e.container } },
            h('header', { class: 'row-flex' },
                h('h2', {}, e.container),
                badge(outcomeLabel(e.outcome), KIND[e.outcome] || 'neutral'),
                h('span', { class: 'spacer' }),
                h('time', { class: 'faint', title: new Date(e.finishedAt).toLocaleString(), datetime: e.finishedAt }, relativeTime(e.finishedAt))),
            h('p', { class: 'muted image-line' }, e.image),
            e.fromImage || e.toImage ? h('p', { class: 'faint' }, h('code', {}, shortImageId(e.fromImage) || 'unknown'), ' to ', h('code', {}, shortImageId(e.toImage) || 'unknown')) : null,
            e.reason ? h('p', {}, e.reason) : null,
            e.log && e.log.length ? h('details', {}, h('summary', {}, 'Steps'), h('pre', { class: 'steps' }, e.log.join('\n'))) : null);
    }

    async function load() {
        try {
            entries = await api.get('/api/history?limit=100');
        } catch (err) {
            if (alive) listEl.replaceChildren(h('p', { class: 'error-text' }, err.message));
            return;
        }
        if (alive) render();
    }

    const stop = onJobs((now, prev) => {
        if (prev.running.length > now.running.length) load();
    });
    load();
    return { unmount() { alive = false; stop(); } };
}
```

Append to `app.css`:

```css
/* History */
.history-entry { margin-bottom: 12px; }
.history-entry h2 { font-size: 15px; }
.history-entry p { margin: 6px 0 0; }
.steps { background: var(--surface-2); border-radius: var(--radius); padding: 8px 10px; margin: 8px 0 0; overflow: auto; }
.history-entry summary { cursor: pointer; margin-top: 8px; color: var(--text-2); }
```

- [ ] **Step 4: Run to verify it passes**

Run: `PW_WORKERS=2 npx playwright test e2e/history.spec.js > /tmp/pw.log 2>&1; echo exit=$? >> /tmp/pw.log; tail -6 /tmp/pw.log`
Expected: `3 passed`, `exit=0`.

- [ ] **Step 5: Falsify the filter and the ordering**

Make `shown` ignore `filter` (`const shown = entries`) → `the container filter narrows the list` must FAIL; reverse the entries (`entries.slice().reverse()`) → `shows new entries, newest first` must FAIL. Restore.

- [ ] **Step 6: Commit**

```bash
git add internal/web e2e
git commit -m "history view"
```

---

### Task 7: Settings view (notifiers, widget, account)

**Files:**
- Modify: `internal/web/static/js/views/settings.js` (replace), `internal/web/static/css/app.css` (append)
- Test: `e2e/settings.spec.js`

**Interfaces:**
- Produces: route `#/settings` with these cards, each an `<section class="card">` with an `h2`:
  - `Notifications`: the configured notifiers as rows (`[data-testid=notifier-row]`, name, type label, an `Enabled` checkbox that saves on change, `Test`, `Edit`, `Delete` buttons); `Add notifier` button `[data-testid=add-notifier]` opens a form (`[data-testid=notifier-form]`) with `Name`, `Type` (from `GET /api/notifiers/types`) and the type's fields (secret fields are `type="password"`, required fields marked with an asterisk in the label text `*` and `required`), `Enabled` checkbox, `Save` and `Cancel`. When editing, secret fields start empty with the placeholder `Unchanged`; an empty secret sends `********` (keep). A `Test` click calls `/api/notifiers/<id>/test` and toasts `Test message sent to <name>` (success) or the API error (error). Delete asks with `confirmDialog`.
  - `nextdash widget`: shows the URL `<origin>/api/widget`, a masked token with `Show`/`Hide`, `Copy token`, and `Create a new token` (confirm first; explains the old one stops working).
  - `Account`: `Signed in as <name>` and a `Sign out` button.
  - The `Push notifications` card is added in Task 8.

- [ ] **Step 1: Write the failing e2e spec**

`e2e/settings.spec.js`:

```js
const { test, expect, signIn } = require('./fixtures');

test.beforeEach(async ({ page }) => {
    await signIn(page);
});

async function addWebhook(page, demo, name = 'Sink') {
    await page.getByTestId('add-notifier').click();
    const form = page.getByTestId('notifier-form');
    await form.getByLabel('Name').fill(name);
    await form.getByLabel('Type').selectOption('webhook');
    await form.getByLabel('URL').fill(`${demo.url}/__demo/sink`);
    await form.getByRole('button', { name: 'Save' }).click();
    await expect(page.getByTestId('notifier-row').filter({ hasText: name })).toBeVisible();
}

test('adds a notifier, sends a test message and deletes it', async ({ page, demo }) => {
    await page.goto('/#/settings');
    await expect(page.getByTestId('notifier-row')).toHaveCount(0);
    await addWebhook(page, demo);

    const row = page.getByTestId('notifier-row').filter({ hasText: 'Sink' });
    await expect(row).toContainText('Webhook');
    await row.getByRole('button', { name: 'Test' }).click();
    await expect(page.getByTestId('toast').filter({ hasText: 'Test message sent to Sink' })).toBeVisible();
    const received = await (await fetch(`${demo.url}/__demo/sink`)).json();
    expect(received.some((b) => b.includes('nextupdate test'))).toBe(true);

    await row.getByRole('button', { name: 'Delete' }).click();
    await page.getByTestId('confirm-yes').click();
    await expect(page.getByTestId('notifier-row')).toHaveCount(0);
});

test('the form validates required fields with the server message', async ({ page }) => {
    await page.goto('/#/settings');
    await page.getByTestId('add-notifier').click();
    const form = page.getByTestId('notifier-form');
    await form.getByLabel('Name').fill('Broken');
    await form.getByLabel('Type').selectOption('ntfy');
    await form.getByLabel('Server URL').fill('https://ntfy.example');
    await form.getByLabel('Topic').fill('   ');
    await form.getByRole('button', { name: 'Save' }).click();
    await expect(form.getByTestId('form-error')).toContainText('Topic');
});

test('secrets are masked, kept when left empty and replaced when typed', async ({ page }) => {
    await page.goto('/#/settings');
    await page.getByTestId('add-notifier').click();
    const form = page.getByTestId('notifier-form');
    await form.getByLabel('Name').fill('Phone');
    await form.getByLabel('Type').selectOption('ntfy');
    await form.getByLabel('Server URL').fill('https://ntfy.example');
    await form.getByLabel('Topic').fill('updates');
    await form.getByLabel('Access token').fill('s3cret-token');
    await form.getByRole('button', { name: 'Save' }).click();

    const row = page.getByTestId('notifier-row').filter({ hasText: 'Phone' });
    await expect(row).toBeVisible();
    await expect(page.locator('body')).not.toContainText('s3cret-token');

    await row.getByRole('button', { name: 'Edit' }).click();
    const edit = page.getByTestId('notifier-form');
    await expect(edit.getByLabel('Access token')).toHaveValue('');
    await expect(edit.getByLabel('Access token')).toHaveAttribute('placeholder', 'Unchanged');
    await edit.getByLabel('Topic').fill('updates-2');
    await edit.getByRole('button', { name: 'Save' }).click();

    const list = await (await page.request.get('/api/notifiers')).json();
    expect(list[0].config.topic).toBe('updates-2');
    expect(list[0].config.token).toBe('********'); // still set
});

test('the enabled checkbox saves on change', async ({ page, demo }) => {
    await page.goto('/#/settings');
    await addWebhook(page, demo);
    const row = page.getByTestId('notifier-row').filter({ hasText: 'Sink' });
    await row.getByLabel('Enabled').uncheck();
    await expect.poll(async () => (await (await page.request.get('/api/notifiers')).json())[0].enabled).toBe(false);
});

test('the widget card shows a masked token that can be revealed, copied and replaced', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await page.goto('/#/settings');
    const card = page.locator('section.card', { has: page.getByRole('heading', { name: 'nextdash widget' }) });
    await expect(card).toContainText('/api/widget');
    const token = page.getByTestId('widget-token');
    await expect(token).toHaveText(/^•+$/);
    await card.getByRole('button', { name: 'Show' }).click();
    const first = (await token.textContent()).trim();
    expect(first.length).toBeGreaterThan(20);

    await card.getByRole('button', { name: 'Copy token' }).click();
    await expect(page.getByTestId('toast').filter({ hasText: 'Token copied' })).toBeVisible();

    await card.getByRole('button', { name: 'Create a new token' }).click();
    await page.getByTestId('confirm-yes').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'New widget token created' })).toBeVisible();
    await card.getByRole('button', { name: /Show|Hide/ }).first().click();
    await expect.poll(async () => (await token.textContent()).trim()).not.toBe(first);

    const old = await page.request.get(`/api/widget?token=${first}`);
    expect(old.status()).toBe(401);
});

test('the account card names the user and signs out', async ({ page }) => {
    await page.goto('/#/settings');
    await expect(page.locator('section.card', { hasText: 'Account' })).toContainText('Signed in as jordi');
    await page.locator('section.card', { hasText: 'Account' }).getByRole('button', { name: 'Sign out' }).click();
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `PW_WORKERS=2 npx playwright test e2e/settings.spec.js > /tmp/pw.log 2>&1; echo exit=$? >> /tmp/pw.log; tail -5 /tmp/pw.log`
Expected: `exit=1`.

- [ ] **Step 3: Implement**

Replace `internal/web/static/js/views/settings.js`:

```js
import { h, clear } from '../dom.js';
import { api } from '../api.js';
import { toast, confirmDialog, badge } from '../ui.js';

const MASK = '********';

export function mountSettings(container) {
    let alive = true;
    let types = [];
    let notifiers = [];
    let me = { name: '' };
    let token = '';
    let tokenShown = false;

    const notifiersHost = h('div', {});
    const widgetHost = h('div', {});
    const accountHost = h('div', {});
    const extraHost = h('div', {}); // Task 8 adds the push card here
    const page = h('div', { class: 'page' }, h('h1', {}, 'Settings'),
        h('section', { class: 'card' }, h('h2', {}, 'Notifications'), notifiersHost),
        extraHost,
        h('section', { class: 'card' }, h('h2', {}, 'nextdash widget'), widgetHost),
        h('section', { class: 'card' }, h('h2', {}, 'Account'), accountHost));
    clear(container).append(page);

    const typeLabel = (t) => (types.find((x) => x.type === t) || { label: t }).label;

    // ---- notifiers ----
    function renderNotifiers(editing) {
        clear(notifiersHost);
        if (!notifiers.length && !editing) notifiersHost.append(h('p', { class: 'muted' }, 'No notifiers yet. Add one to hear about updates outside this page.'));
        for (const n of notifiers) {
            if (editing && editing.id === n.id) continue;
            const enabled = h('input', { type: 'checkbox', checked: n.enabled, id: `en-${n.id}`, onchange: () => save({ ...n, enabled: enabled.checked }, false) });
            notifiersHost.append(h('div', { class: 'row-flex notifier-row', dataset: { testid: 'notifier-row' } },
                h('strong', {}, n.name), badge(typeLabel(n.type)), h('span', { class: 'spacer' }),
                h('label', { class: 'check', for: `en-${n.id}` }, enabled, 'Enabled'),
                h('button', { class: 'btn btn-small', type: 'button', onclick: () => testSend(n) }, 'Test'),
                h('button', { class: 'btn btn-small', type: 'button', onclick: () => renderNotifiers(n) }, 'Edit'),
                h('button', { class: 'btn btn-small', type: 'button', onclick: () => remove(n) }, 'Delete')));
        }
        if (editing !== undefined) notifiersHost.append(form(editing));
        else notifiersHost.append(h('button', { class: 'btn', type: 'button', dataset: { testid: 'add-notifier' }, onclick: () => renderNotifiers(null) }, 'Add notifier'));
    }

    function form(existing) {
        const error = h('p', { class: 'error-text', dataset: { testid: 'form-error' }, hidden: true });
        const name = h('input', { id: 'n-name', type: 'text', value: existing ? existing.name : '' });
        const type = h('select', { id: 'n-type', onchange: () => paintFields() }, types.map((t) => h('option', { value: t.type }, t.label)));
        if (existing) { type.value = existing.type; type.disabled = true; }
        const enabled = h('input', { type: 'checkbox', id: 'n-enabled', checked: existing ? existing.enabled : true });
        const fieldsHost = h('div', {});
        const inputs = {};

        function paintFields() {
            clear(fieldsHost);
            const t = types.find((x) => x.type === type.value);
            for (const f of t ? t.fields : []) {
                const id = `nf-${f.key}`;
                const current = existing && existing.type === type.value ? existing.config[f.key] : '';
                const input = h('input', {
                    id, type: f.secret ? 'password' : 'text', autocomplete: 'off',
                    value: f.secret ? '' : (current || ''), placeholder: f.secret && current === MASK ? 'Unchanged' : '',
                    required: f.required && !(f.secret && current === MASK),
                });
                inputs[f.key] = { input, secret: f.secret, hadSecret: current === MASK };
                fieldsHost.append(h('div', { class: 'field' }, h('label', { for: id }, f.label, f.required ? ' *' : ''), input));
            }
        }
        paintFields();

        const save1 = h('button', { class: 'btn btn-primary', type: 'submit' }, 'Save');
        const el = h('form', {
            class: 'notifier-form', dataset: { testid: 'notifier-form' }, novalidate: true,
            onsubmit: async (e) => {
                e.preventDefault();
                error.hidden = true;
                const config = {};
                for (const [key, f] of Object.entries(inputs)) {
                    const v = f.input.value;
                    config[key] = f.secret && v === '' && f.hadSecret ? MASK : v;
                }
                save1.setAttribute('aria-busy', 'true');
                try {
                    await save({ id: existing ? existing.id : 0, name: name.value, type: type.value, enabled: enabled.checked, config }, true);
                } catch (err) {
                    error.textContent = err.message;
                    error.hidden = false;
                } finally {
                    save1.removeAttribute('aria-busy');
                }
            },
        },
            h('div', { class: 'field' }, h('label', { for: 'n-name' }, 'Name'), name),
            h('div', { class: 'field' }, h('label', { for: 'n-type' }, 'Type'), type),
            fieldsHost,
            h('label', { class: 'check', for: 'n-enabled' }, enabled, 'Enabled'),
            h('div', { class: 'row-flex form-actions' }, save1, h('button', { class: 'btn', type: 'button', onclick: () => renderNotifiers() }, 'Cancel')),
            error);
        return el;
    }

    async function save(n, closeForm) {
        const body = { name: n.name, type: n.type, enabled: n.enabled, config: n.config };
        try {
            if (n.id) await api.put(`/api/notifiers/${n.id}`, body);
            else await api.post('/api/notifiers', body);
        } catch (e) {
            if (closeForm) throw e;
            toast(e.message, 'error');
            await loadNotifiers();
            return;
        }
        await loadNotifiers();
    }

    async function loadNotifiers() {
        notifiers = await api.get('/api/notifiers');
        if (alive) renderNotifiers();
    }

    async function testSend(n) {
        try {
            await api.post(`/api/notifiers/${n.id}/test`);
            toast(`Test message sent to ${n.name}`, 'success');
        } catch (e) {
            toast(e.message, 'error');
        }
    }

    async function remove(n) {
        const ok = await confirmDialog({ title: `Delete ${n.name}?`, body: h('p', {}, 'You will stop receiving messages through this notifier.'), confirmLabel: 'Delete', danger: true });
        if (!ok) return;
        try {
            await api.del(`/api/notifiers/${n.id}`);
            await loadNotifiers();
        } catch (e) {
            toast(e.message, 'error');
        }
    }

    // ---- widget ----
    function renderWidget() {
        clear(widgetHost);
        const url = `${location.origin}/api/widget`;
        widgetHost.append(
            h('p', { class: 'muted' }, 'Point a nextdash custom widget at this address and send the token as a Bearer token. It returns the number of updates, the number of breaking updates and the time of the last check.'),
            h('p', {}, h('code', {}, url)),
            h('div', { class: 'row-flex' },
                h('code', { class: 'token', dataset: { testid: 'widget-token' } }, tokenShown ? token : '•'.repeat(24)),
                h('button', { class: 'btn btn-small', type: 'button', onclick: () => { tokenShown = !tokenShown; renderWidget(); } }, tokenShown ? 'Hide' : 'Show'),
                h('button', { class: 'btn btn-small', type: 'button', onclick: copyToken }, 'Copy token'),
                h('span', { class: 'spacer' }),
                h('button', { class: 'btn btn-small', type: 'button', onclick: rotate }, 'Create a new token')));
    }

    async function copyToken() {
        try {
            await navigator.clipboard.writeText(token);
            toast('Token copied', 'success');
        } catch {
            toast("Couldn't copy. Show the token and copy it by hand.", 'error');
        }
    }

    async function rotate() {
        const ok = await confirmDialog({ title: 'Create a new widget token?', body: h('p', {}, 'The current token stops working right away. Update your nextdash widget with the new one.'), confirmLabel: 'Create token' });
        if (!ok) return;
        try {
            token = (await api.post('/api/widget/token/rotate')).token;
            toast('New widget token created', 'success');
            renderWidget();
        } catch (e) {
            toast(e.message, 'error');
        }
    }

    // ---- account ----
    function renderAccount() {
        clear(accountHost).append(h('div', { class: 'row-flex' },
            h('span', {}, `Signed in as ${me.name}`), h('span', { class: 'spacer' }),
            h('button', {
                class: 'btn', type: 'button',
                onclick: async () => {
                    try { await api.post('/api/logout'); } catch { /* cleared either way */ }
                    document.dispatchEvent(new CustomEvent('nu:signed-out'));
                },
            }, 'Sign out')));
    }

    (async () => {
        try {
            [types, me, token] = await Promise.all([api.get('/api/notifiers/types'), api.get('/api/me'), api.get('/api/widget/token').then((r) => r.token)]);
            if (!alive) return;
            await loadNotifiers();
            renderWidget();
            renderAccount();
        } catch (e) {
            if (alive && e.status !== 401) notifiersHost.replaceChildren(h('p', { class: 'error-text' }, e.message));
        }
    })();

    return { unmount() { alive = false; }, extraHost };
}
```

Append to `app.css`:

```css
/* Settings */
.notifier-row { padding: 8px 0; border-bottom: 1px solid var(--border); }
.check { display: inline-flex; align-items: center; gap: 6px; margin: 0; font-weight: 400; }
.check input { width: auto; height: auto; }
.notifier-form { margin-top: 12px; padding: 14px; background: var(--surface-2); border-radius: var(--radius); }
.form-actions { margin-top: 12px; }
.token { background: var(--surface-2); border-radius: 4px; padding: 2px 8px; overflow-wrap: anywhere; }
```

- [ ] **Step 4: Run to verify it passes**

Run: `PW_WORKERS=2 npx playwright test e2e/settings.spec.js > /tmp/pw.log 2>&1; echo exit=$? >> /tmp/pw.log; tail -8 /tmp/pw.log`
Expected: all pass, `exit=0`.

- [ ] **Step 5: Falsify the secret handling**

Make the form send `''` instead of `MASK` when a secret is left empty (`config[key] = v`); `secrets are masked, kept when left empty and replaced when typed` must FAIL (the token would be cleared). Restore.

- [ ] **Step 6: Commit**

```bash
git add internal/web e2e
git commit -m "settings view: notifiers, widget token, account"
```

---

### Task 8: PWA: manifest, icons, service worker, push

**Files:**
- Create: `tools/genicons/main.go`, `internal/web/static/manifest.webmanifest`, `internal/web/static/sw.js`, `internal/web/static/js/push.js`, generated `internal/web/static/icons/*`
- Modify: `internal/web/static/js/views/settings.js` (add the push card), `internal/web/static/js/app.js` (register the service worker), `internal/web/static/css/app.css`
- Test: `e2e/pwa.spec.js` (extend), `e2e/settings.spec.js` (append)

**Interfaces:**
- Produces:
  - `manifest.webmanifest`: `name` `nextupdate`, `short_name` `nextupdate`, `start_url` `/`, `scope` `/`, `display` `standalone`, `background_color` `#f5f5f2`, `theme_color` `#2563eb`, icons `icon-192.png` (192), `icon-512.png` (512), `icon-maskable-512.png` (512, `purpose: maskable`), `icon.svg` (`sizes: any`)
  - `icons/icon.svg`, `icon-192.png`, `icon-512.png`, `icon-maskable-512.png`, `apple-touch-icon.png` (180), all written by `go run ./tools/genicons`; the SVG is written by hand
  - `sw.js` (served from `/`, so its scope is `/`): `push` event shows a notification from the JSON payload `{title, body, url, container, kind}` (title falls back to `nextupdate`), with `data.url`; `notificationclick` closes the notification and focuses an open window or opens `data.url`, else `/`; `fetch` handler that does nothing (present for installability, never caches)
  - `push.js`: `pushSupported()`, `pushState() → 'unsupported'|'denied'|'subscribed'|'off'`, `enablePush()`, `disablePush()`, `sendTestPush()` (POST `/api/push/test`), `bytesFromBase64Url(s)`
  - Settings card `Push notifications` (in `extraHost`): status text `[data-testid=push-status]`, buttons `Turn on for this device` / `Turn off for this device` and `Send a test`; when unsupported or blocked, the text explains why (`This browser does not support push notifications.` / `Notifications are blocked for this site. Allow them in the browser settings.` / `Push needs HTTPS or localhost.`)
  - `app.js` registers `/sw.js` after boot when `serviceWorker` exists (failures are ignored)

- [ ] **Step 1: Write the failing tests**

Extend `e2e/pwa.spec.js` (replace the file):

```js
const { test, expect, signIn } = require('./fixtures');

test('the demo serves the app shell', async ({ page }) => {
    const res = await page.goto('/');
    expect(res.status()).toBe(200);
    await expect(page).toHaveTitle('nextupdate');
    expect(res.headers()['content-security-policy']).toContain("script-src 'self'");
});

test('the manifest describes an installable app with real icons', async ({ page, request }) => {
    await page.goto('/');
    const href = await page.locator('link[rel="manifest"]').getAttribute('href');
    const res = await request.get(href);
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('application/manifest+json');
    const m = await res.json();
    expect(m).toMatchObject({ name: 'nextupdate', start_url: '/', scope: '/', display: 'standalone', theme_color: '#2563eb' });
    const sizes = m.icons.map((i) => i.sizes);
    expect(sizes).toEqual(expect.arrayContaining(['192x192', '512x512', 'any']));
    expect(m.icons.some((i) => i.purpose === 'maskable')).toBe(true);
    for (const icon of m.icons) {
        const r = await request.get(icon.src);
        expect(r.status(), icon.src).toBe(200);
        expect(r.headers()['content-type'], icon.src).toContain(icon.type);
    }
});

test('the icons are valid PNGs of the right size', async ({ request }) => {
    for (const [file, size] of [['icon-192.png', 192], ['icon-512.png', 512], ['icon-maskable-512.png', 512], ['apple-touch-icon.png', 180]]) {
        const buf = await (await request.get(`/icons/${file}`)).body();
        expect(buf.subarray(1, 4).toString(), file).toBe('PNG');
        expect(buf.readUInt32BE(16), `${file} width`).toBe(size);
        expect(buf.readUInt32BE(20), `${file} height`).toBe(size);
    }
});

test('the service worker is served from the root as JavaScript and registers', async ({ page, request }) => {
    const res = await request.get('/sw.js');
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('text/javascript');
    await signIn(page);
    await page.goto('/');
    await expect(page.getByTestId('check-button')).toBeVisible();
    const scope = await page.evaluate(async () => (await navigator.serviceWorker.ready).scope);
    expect(scope).toMatch(/\/$/);
});

test('the service worker shows a notification for a push message', async ({ page }) => {
    await signIn(page);
    await page.goto('/');
    await expect(page.getByTestId('check-button')).toBeVisible();
    // Run the worker's own handler in isolation, with the same globals it gets in a worker.
    const shown = await page.evaluate(async () => {
        const src = await (await fetch('/sw.js')).text();
        const listeners = {};
        const shownList = [];
        const self = {
            addEventListener: (t, fn) => { listeners[t] = fn; },
            registration: { showNotification: (title, opts) => { shownList.push({ title, opts }); return Promise.resolve(); } },
            clients: { matchAll: async () => [], openWindow: async () => null },
            skipWaiting: () => {},
        };
        new Function('self', src)(self);
        const data = { json: () => ({ title: 'Update available: app', body: 'Details', url: 'https://nu.example', container: 'app' }) };
        let p;
        listeners.push({ data, waitUntil: (x) => { p = x; } });
        await p;
        return shownList;
    });
    expect(shown).toHaveLength(1);
    expect(shown[0].title).toBe('Update available: app');
    expect(shown[0].opts.body).toBe('Details');
    expect(shown[0].opts.data.url).toBe('https://nu.example');
});
```

Append to `e2e/settings.spec.js`:

```js
test('the push card explains what is possible in this browser', async ({ page }) => {
    await page.goto('/#/settings');
    const card = page.locator('section.card', { has: page.getByRole('heading', { name: 'Push notifications' }) });
    await expect(card).toBeVisible();
    const status = page.getByTestId('push-status');
    await expect(status).toHaveText(/(Push notifications are off on this device\.|This browser does not support push notifications\.|Push needs HTTPS or localhost\.|Notifications are blocked)/);
});

test('turning push on in a browser that cannot subscribe shows an error, not a broken page', async ({ page, context }) => {
    await context.grantPermissions(['notifications']);
    await page.goto('/#/settings');
    const card = page.locator('section.card', { has: page.getByRole('heading', { name: 'Push notifications' }) });
    const on = card.getByRole('button', { name: 'Turn on for this device' });
    if (await on.count() === 0) test.skip(true, 'this browser cannot offer push');
    await on.click();
    // Headless Chromium has no push service, so subscribing fails; the card must stay usable.
    await expect(page.getByTestId('toast')).toBeVisible();
    await expect(page.getByTestId('push-status')).toBeVisible();
    await expect(page.getByTestId('check-button')).toBeVisible();
});
```

- [ ] **Step 2: Run to verify they fail**

Run: `PW_WORKERS=2 npx playwright test e2e/pwa.spec.js > /tmp/pw.log 2>&1; echo exit=$? >> /tmp/pw.log; tail -8 /tmp/pw.log`
Expected: `exit=1` (no manifest, icons or service worker yet).

- [ ] **Step 3: Write the icon generator and the manifest**

`tools/genicons/main.go`:

```go
// Command genicons writes the PNG icons of the PWA into internal/web/static/icons.
// Run it with: go run ./tools/genicons
package main

import (
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
)

var (
	blue  = color.NRGBA{0x25, 0x63, 0xeb, 0xff}
	white = color.NRGBA{0xff, 0xff, 0xff, 0xff}
)

// glyph reports whether the point (x, y) in unit coordinates is inside the
// white "update" mark: an arrow pointing up above a bar.
func glyph(x, y, scale float64) bool {
	x = 0.5 + (x-0.5)/scale
	y = 0.5 + (y-0.5)/scale
	inTri := func(ax, ay, bx, by, cx, cy float64) bool {
		d := (by-cy)*(ax-cx) + (cx-bx)*(ay-cy)
		if d == 0 {
			return false
		}
		a := ((by-cy)*(x-cx) + (cx-bx)*(y-cy)) / d
		b := ((cy-ay)*(x-cx) + (ax-cx)*(y-cy)) / d
		return a >= 0 && b >= 0 && 1-a-b >= 0
	}
	head := inTri(0.5, 0.22, 0.28, 0.46, 0.72, 0.46)
	shaft := x >= 0.44 && x <= 0.56 && y >= 0.44 && y <= 0.64
	bar := x >= 0.28 && x <= 0.72 && y >= 0.71 && y <= 0.79
	return head || shaft || bar
}

func insideRounded(x, y, r float64) bool {
	cx := math.Min(math.Max(x, r), 1-r)
	cy := math.Min(math.Max(y, r), 1-r)
	return math.Hypot(x-cx, y-cy) <= r
}

func render(size int, maskable bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	const ss = 4 // supersampling per axis
	glyphScale := 1.0
	if maskable {
		glyphScale = 0.78 // keep the mark inside the safe zone
	}
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/ss) / float64(size)
					var c color.NRGBA
					var on bool
					switch {
					case !maskable && !insideRounded(x, y, 0.22):
					case glyph(x, y, glyphScale):
						c, on = white, true
					default:
						c, on = blue, true
					}
					if on {
						r += float64(c.R)
						g += float64(c.G)
						b += float64(c.B)
						a += 255
					}
				}
			}
			n := float64(ss * ss)
			if a > 0 {
				cnt := a / 255
				img.SetNRGBA(px, py, color.NRGBA{uint8(r / cnt), uint8(g / cnt), uint8(b / cnt), uint8(a / n)})
			}
		}
	}
	return img
}

func main() {
	dir := filepath.Join("internal", "web", "static", "icons")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	for _, s := range []struct {
		name     string
		size     int
		maskable bool
	}{
		{"icon-192.png", 192, false},
		{"icon-512.png", 512, false},
		{"icon-maskable-512.png", 512, true},
		{"apple-touch-icon.png", 180, true}, // iOS rounds the corners itself
	} {
		f, err := os.Create(filepath.Join(dir, s.name))
		if err != nil {
			log.Fatal(err)
		}
		if err := png.Encode(f, render(s.size, s.maskable)); err != nil {
			log.Fatal(err)
		}
		f.Close()
	}
}
```

`internal/web/static/icons/icon.svg`:

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512"><rect width="512" height="512" rx="113" fill="#2563eb"/><path fill="#fff" d="M256 113 143 236h226z"/><rect x="225" y="225" width="62" height="102" fill="#fff"/><rect x="143" y="364" width="226" height="41" fill="#fff"/></svg>
```

`internal/web/static/manifest.webmanifest`:

```json
{
  "name": "nextupdate",
  "short_name": "nextupdate",
  "description": "Update cockpit for Docker containers",
  "start_url": "/",
  "scope": "/",
  "display": "standalone",
  "background_color": "#f5f5f2",
  "theme_color": "#2563eb",
  "icons": [
    { "src": "/icons/icon-192.png", "sizes": "192x192", "type": "image/png" },
    { "src": "/icons/icon-512.png", "sizes": "512x512", "type": "image/png" },
    { "src": "/icons/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable" },
    { "src": "/icons/icon.svg", "sizes": "any", "type": "image/svg+xml" }
  ]
}
```

Run `go run ./tools/genicons` and check with `file internal/web/static/icons/*.png` (expected `PNG image data, 192 x 192` and so on).

- [ ] **Step 4: Write the service worker and `push.js`**

`internal/web/static/sw.js`:

```js
// nextupdate service worker: shows push notifications. It never caches anything.
self.addEventListener('install', () => self.skipWaiting());

self.addEventListener('push', (event) => {
    let payload = {};
    try {
        payload = event.data ? event.data.json() : {};
    } catch (e) {
        payload = { body: event.data ? String(event.data) : '' };
    }
    const title = payload.title || 'nextupdate';
    event.waitUntil(self.registration.showNotification(title, {
        body: payload.body || '',
        icon: '/icons/icon-192.png',
        badge: '/icons/icon-192.png',
        tag: payload.container ? `nextupdate-${payload.container}` : 'nextupdate',
        data: { url: payload.url || '/' },
    }));
});

self.addEventListener('notificationclick', (event) => {
    event.notification.close();
    const url = (event.notification.data && event.notification.data.url) || '/';
    event.waitUntil((async () => {
        const open = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
        for (const c of open) {
            if ('focus' in c) return c.focus();
        }
        return self.clients.openWindow(url);
    })());
});

// A fetch handler keeps the app installable. It passes every request through.
self.addEventListener('fetch', () => {});
```

`internal/web/static/js/push.js`:

```js
import { api } from './api.js';

export function bytesFromBase64Url(s) {
    const pad = '='.repeat((4 - (s.length % 4)) % 4);
    const raw = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'));
    return Uint8Array.from(raw, (c) => c.charCodeAt(0));
}

export function pushSupported() {
    return 'serviceWorker' in navigator && 'PushManager' in window && 'Notification' in window && window.isSecureContext;
}

/** 'unsupported' | 'insecure' | 'denied' | 'subscribed' | 'off' */
export async function pushState() {
    if (!('serviceWorker' in navigator) || !('PushManager' in window) || !('Notification' in window)) return 'unsupported';
    if (!window.isSecureContext) return 'insecure';
    if (Notification.permission === 'denied') return 'denied';
    try {
        const reg = await navigator.serviceWorker.getRegistration('/');
        const sub = reg && (await reg.pushManager.getSubscription());
        return sub ? 'subscribed' : 'off';
    } catch {
        return 'off';
    }
}

export async function enablePush() {
    const reg = await navigator.serviceWorker.register('/sw.js');
    await navigator.serviceWorker.ready;
    const permission = await Notification.requestPermission();
    if (permission !== 'granted') throw new Error('Notifications were not allowed, so push stays off.');
    const { publicKey } = await api.get('/api/push/key');
    let sub;
    try {
        sub = await reg.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: bytesFromBase64Url(publicKey) });
    } catch {
        throw new Error("This browser couldn't subscribe to push. It may not have a push service available.");
    }
    await api.post('/api/push/subscribe', sub.toJSON());
}

export async function disablePush() {
    const reg = await navigator.serviceWorker.getRegistration('/');
    const sub = reg && (await reg.pushManager.getSubscription());
    if (!sub) return;
    const endpoint = sub.endpoint;
    await sub.unsubscribe();
    await api.post('/api/push/unsubscribe', { endpoint });
}

export function sendTestPush() {
    return api.post('/api/push/test');
}
```

- [ ] **Step 5: Register the worker, add the push card**

In `internal/web/static/js/app.js`, after `boot();` at the bottom add:

```js
if ('serviceWorker' in navigator) {
    window.addEventListener('load', () => navigator.serviceWorker.register('/sw.js').catch(() => { /* push is optional */ }));
}
```

In `settings.js` add the import `import { pushState, enablePush, disablePush, sendTestPush } from '../push.js';` and, inside `mountSettings`, before the final `return`, add:

```js
    // ---- push ----
    const pushCard = h('section', { class: 'card' }, h('h2', {}, 'Push notifications'));
    const pushBody = h('div', {});
    pushCard.append(pushBody);
    extraHost.append(pushCard);

    async function renderPush() {
        const state = await pushState();
        if (!alive) return;
        const text = {
            unsupported: 'This browser does not support push notifications.',
            insecure: 'Push needs HTTPS or localhost.',
            denied: 'Notifications are blocked for this site. Allow them in the browser settings.',
            subscribed: 'Push notifications are on for this device.',
            off: 'Push notifications are off on this device.',
        }[state];
        clear(pushBody).append(
            h('p', { class: 'muted', dataset: { testid: 'push-status' } }, text),
            state === 'off' ? h('button', { class: 'btn', type: 'button', onclick: turnOn }, 'Turn on for this device') : null,
            state === 'subscribed' ? h('div', { class: 'row-flex' },
                h('button', { class: 'btn', type: 'button', onclick: turnOff }, 'Turn off for this device'),
                h('button', { class: 'btn', type: 'button', onclick: test }, 'Send a test')) : null);
    }
    async function turnOn() {
        try {
            await enablePush();
            toast('Push notifications are on for this device', 'success');
        } catch (e) {
            toast(e.message, 'error');
        }
        renderPush();
    }
    async function turnOff() {
        try {
            await disablePush();
            toast('Push notifications are off for this device', 'success');
        } catch (e) {
            toast(e.message, 'error');
        }
        renderPush();
    }
    async function test() {
        try {
            const r = await sendTestPush();
            toast(r.sent ? 'Test notification sent' : 'No device received the test. Turn push off and on again.', r.sent ? 'success' : 'warn');
        } catch (e) {
            toast(e.message, 'error');
        }
    }
    renderPush();
```

- [ ] **Step 6: Run to verify it passes**

Run: `PW_WORKERS=2 npx playwright test e2e/pwa.spec.js e2e/settings.spec.js -g "push|manifest|icons|service worker|shell" > /tmp/pw.log 2>&1; echo exit=$? >> /tmp/pw.log; tail -8 /tmp/pw.log`
Expected: all matching specs pass (the "cannot subscribe" spec may be reported as skipped; that is acceptable only if the browser truly offers no push button, so check the log), `exit=0`.

- [ ] **Step 7: Falsify the worker and the manifest icons**

Change `payload.title || 'nextupdate'` to `'nextupdate'` in `sw.js`: `the service worker shows a notification for a push message` must FAIL. Delete the `icon-512.png` reference from the manifest: `the manifest describes an installable app with real icons` must FAIL. Restore both.

- [ ] **Step 8: Commit**

```bash
git add tools internal/web e2e
git commit -m "pwa: manifest, icons, service worker and push settings"
```

---

### Task 9: Phone layout, README, and a look at the real thing

**Files:**
- Create: `e2e/mobile.spec.js`
- Modify: `README.md`, `Dockerfile` (no change expected), `internal/web/static/css/app.css` (only if the mobile spec exposes a problem)

**Interfaces:**
- Produces: the phone layout is verified; README describes the UI, the widget setup and push (needs HTTPS or localhost); screenshots of the real UI in light, dark and phone width are shown to the user.

- [ ] **Step 1: Write the mobile spec**

`e2e/mobile.spec.js`:

```js
const { test, expect, signIn } = require('./fixtures');

test.use({ viewport: { width: 390, height: 800 } });

test.beforeEach(async ({ page }) => {
    await signIn(page);
});

test('the list shows first, tapping a container opens its details, back returns', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('a.row[data-name="immich"]')).toBeVisible();
    await expect(page.getByTestId('detail')).toBeHidden();
    await expect(page).toHaveURL(/#\/updates$/); // no automatic selection on a phone

    await page.locator('a.row[data-name="sonarr"]').click();
    await expect(page.getByTestId('detail')).toBeVisible();
    await expect(page.getByTestId('detail')).toContainText('sonarr');
    await expect(page.locator('.list-pane')).toBeHidden();

    await page.getByTestId('back').click();
    await expect(page.locator('.list-pane')).toBeVisible();
    await expect(page.getByTestId('detail')).toBeHidden();
});

test('nothing overflows the screen sideways', async ({ page }) => {
    for (const hash of ['#/updates', '#/updates/immich', '#/history', '#/settings']) {
        await page.goto(`/${hash}`);
        await expect(page.getByTestId('check-button')).toBeVisible();
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
        expect(overflow, hash).toBeLessThanOrEqual(0);
    }
});

test('actions stay usable with a touch-sized target', async ({ page }) => {
    await page.goto('/#/updates/sonarr');
    const box = await page.getByTestId('update-button').boundingBox();
    expect(box.height).toBeGreaterThanOrEqual(36);
});
```

- [ ] **Step 2: Run it; fix the CSS if a test shows a real problem**

Run: `PW_WORKERS=2 npx playwright test e2e/mobile.spec.js > /tmp/pw.log 2>&1; echo exit=$? >> /tmp/pw.log; tail -8 /tmp/pw.log`
Expected: `3 passed`, `exit=0`. If `nothing overflows` fails, find the element (`document.querySelectorAll('*')` with `getBoundingClientRect().right > innerWidth`), fix its CSS (for example `min-width: 0`, `overflow-wrap: anywhere`), and rerun. Do not weaken the test.

- [ ] **Step 3: Falsify the phone layout**

Remove the `.split.detail-open .list-pane { display: none; }` rule: `the list shows first, tapping a container opens its details, back returns` must FAIL. Restore.

- [ ] **Step 4: Update the README**

In `README.md`, replace the sentence `The first visit to the API creates the admin account (`POST /api/setup`); the web UI for this comes in a later release.` with:

```markdown
Open `http://your-server:8099`. The first visit creates the admin account.

## The web UI

- **Updates**: containers grouped as breaking, updates available and up to date. Pick one to read the release notes between your version and the new one, see why an update looks breaking, and update or roll back with one click. Set the policy (notify, automatic, never) per container. Keys: `j` and `k` move, `u` updates, `c` checks now.
- **History**: every update and rollback, with its steps.
- **Settings**: notifiers (ntfy, Gotify, Discord, Telegram, webhook, e-mail), push on this device, the token for the nextdash widget, and your account.

Install it as an app from the browser menu. Push notifications need HTTPS (a reverse proxy) or `localhost`, and a browser that has a push service.

Release notes come from GitHub and can contain anything, so the UI shows them as plain formatted text: no HTML, and only `http` and `https` links.
```

- [ ] **Step 5: Run the unit tests, the Go tests and the specs for this plan once**

Run: `node --test web-test/ 2>&1 | tail -4` and `go test -race -count=1 ./... 2>&1 | grep -v 'no test files'`
Expected: Node `# fail 0`; Go all `ok`.

Do not run the whole Playwright suite. Each earlier task already ran its own specs.

- [ ] **Step 6: Rebuild the image and look at the real UI**

Run in the background: `docker build -t nextupdate:dev . > /tmp/nu-build4.log 2>&1; echo exit=$? >> /tmp/nu-build4.log`. When `exit=0`, run `docker run --rm -d --name nu-look -p 8099:8099 -v /var/run/docker.sock:/var/run/docker.sock nextupdate:dev`.

Use the Browser pane (`preview_start` with `url: http://localhost:8099`) or a Playwright script to create the account and take screenshots: the updates page at 1280 px in light and dark, the detail of a container, settings, and the updates page at 390 px. The real host has one container (`nextdash`) and no updates, so also take the same screenshots from the demo server (`go run ./cmd/uidemo -addr 127.0.0.1:8099` after stopping the container) because it has all states. Send the screenshots to the user with `SendUserFile`. Then `docker rm -f nu-look` and stop the demo server.

- [ ] **Step 7: Commit**

```bash
git add e2e README.md internal/web
git commit -m "phone layout tests and readme for the ui"
```

---

## Spec coverage (Plan 3b)

| Spec item | Task |
|---|---|
| Main list grouped as breaking, update available, up to date; icon, version transition, source badge, policy, update button | 5 |
| Detail panel with the changelog of all releases in between, keyword reasons, repo links | 5 |
| History with expandable steps and a roll-back button | 5 (rollback), 6 (history) |
| Settings: notifiers, policy per container, health check URL, verify window | 5, 7 |
| PWA: manifest, service worker, push | 8 |
| Light and dark theme | 4 |
| English only, i18n-ready | all: every string is a literal in one place per view; no translation layer yet |
| nextdash widget | 7 (token and URL), API in 3a |
| Confirmation before breaking updates | 5 |
| Layout chosen from mockups | decided before this plan: list and detail panel |
| GitHub release notes are untrusted | 3, 5 (parser, renderer, tests, CSP) |

**Known limits, accepted for now:**
- No translations; the strings are English literals.
- No offline mode; the service worker only handles push and never caches.
- Real push delivery cannot be tested in headless Chromium (there is no push service); the worker's handler and the subscribe flow are tested in isolation, and the first real test is on a phone or desktop browser over HTTPS.
- The updates list does not update by itself when another person changes something; it refreshes when a job finishes, when you press `c`, or when you reload.
- The Markdown reader covers what release notes use (headings, lists, code, quotes, links, emphasis). Tables, nested lists and images stay plain text.
