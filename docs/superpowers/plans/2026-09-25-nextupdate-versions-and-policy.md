# nextupdate Versions, Changelog and Policy Implementation Plan (Plan 2 of 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the headless engine of Plan 1 into a self-running updater: it reads old and new versions from image labels, fetches the release notes in between from GitHub, flags breaking updates, applies a per-container policy (`notify`, `auto`, `never`), announces new updates once, cleans up old images and runs on a schedule (`nextupdate serve`).

**Architecture:** New small packages (`semver`, `changelog`, `classify`, `policy`, `scheduler`) plug into the existing `engine`. `engine.Check` now also writes an `Info` row per available update (versions, repo, breaking flag, reasons). The scheduler is a loop over `Check`, `policy.Decide` and `Update`; it talks to the engine and to a `Notifier` through interfaces, so it is tested with fakes. Notifier targets (push, ntfy, ...) arrive in Plan 3; here a log notifier is the only implementation.

**Tech Stack:** Go 1.26, SQLite (`modernc.org/sqlite`), go-containerregistry, GitHub REST API (`/repos/{owner}/{repo}/releases`), stdlib `net/http`.

**Spec:** `docs/superpowers/specs/2026-09-25-nextupdate-design.md`

**Builds on:** Plan 1 (`docs/superpowers/plans/2026-09-25-nextupdate-core-engine.md`), implemented on branch `dev` (last commit `0fc3e7e`). Follow-up: Plan 3 (web UI, auth, PWA/push, notifier targets, nextdash widget).

## Global Constraints

- Module path `github.com/jordibrouwer/nextupdate`, Go 1.26, `CGO_ENABLED=0` must build.
- Policy values are exactly `notify` (default), `auto`, `never` (`store.PolicyNotify`, `PolicyAuto`, `PolicyNever`).
- `auto` updates only patch or minor version changes and equal-version rebuilds. It never updates a breaking update, a major change, a downgrade, an unknown version, or a protected image.
- Manual repo (container setting) beats the image label, which beats the bundled mapping.
- GitHub calls send `If-None-Match` from the cache; a 304 costs no rate limit. Optional token from `NEXTUPDATE_GITHUB_TOKEN`.
- Old images are kept `NEXTUPDATE_KEEP_OLD_IMAGES` (default `168h`) after a successful update.
- Tests: unit tests with `go test ./...`; integration tests only with `-tags integration`. Keep Bash calls under 30 s; run slow things in the background. Port 8099 for anything that listens; never 8080.
- Every task ends with a commit. Short plain subject line, no `Co-Authored-By` trailer.
- New strings that reach a user (reasons, event details) are plain English sentences.

## File Structure

```
internal/store/schema.sql            (modify: new tables per task)
internal/store/settings.go           per-container settings
internal/store/info.go               Info rows, changelog cache, old images, seen markers
internal/store/settings_test.go
internal/store/info_test.go
internal/semver/semver.go            Parse, Compare, Diff
internal/semver/semver_test.go
internal/registry/registry.go        (modify: RemoteLabels, LocalLabels, label constants)
internal/changelog/mapping.go        bundled mappings, Resolve, RepoFromURL
internal/changelog/mappings.json     bundled image → repo table
internal/changelog/github.go         GitHub releases with ETag cache, Between
internal/changelog/changelog_test.go
internal/classify/classify.go        breaking detection
internal/classify/classify_test.go
internal/policy/policy.go            Decide, Protected
internal/policy/policy_test.go
internal/scheduler/scheduler.go      RunOnce, Run, Notifier, Event
internal/scheduler/scheduler_test.go
internal/engine/verifier.go          per-container verifier
internal/engine/verifier_test.go
internal/engine/engine.go            (modify: describe, Retention, Cleanup, mutex)
internal/engine/engine_test.go       (modify)
internal/updater/*.go                (modify: Verifier gets the container name)
internal/docker/*.go, dockertest/fake.go  (modify: RemoveImage, ErrConflict)
cmd/nextupdate/main.go               (modify: serve, policy, richer check)
Dockerfile                           (modify: CMD serve)
```

---

### Task 1: Per-container settings and verifier

**Files:**
- Modify: `internal/store/schema.sql`, `internal/updater/updater.go`, `internal/updater/run.go`, `internal/updater/compose.go`, `internal/updater/run_test.go`, `test/integration/update_test.go`, `cmd/nextupdate/main.go`
- Create: `internal/store/settings.go`, `internal/engine/verifier.go`
- Test: `internal/store/settings_test.go`, `internal/engine/verifier_test.go`

**Interfaces:**
- Produces:
  - `store.PolicyNotify`, `store.PolicyAuto`, `store.PolicyNever` (string constants), `store.ValidPolicy(p string) bool`
  - `store.Settings{Container, Policy, HTTPURL, Repo string; VerifyWindow time.Duration}`
  - `(*Store).GetSettings(container string) (Settings, error)`: returns `Policy: "notify"` and zero values when no row exists
  - `(*Store).SetSettings(s Settings) error`: rejects an invalid policy
  - `updater.Verifier` becomes `func(ctx context.Context, name, id string) verify.Result` (`name` is the container name, `id` the new container ID)
  - `engine.NewVerifier(api docker.API, st *store.Store, base verify.Check) updater.Verifier`

- [ ] **Step 1: Write the failing store test**

`internal/store/settings_test.go`:

```go
package store

import (
	"testing"
	"time"
)

func TestSettingsDefaultsAndRoundTrip(t *testing.T) {
	s := openTest(t)
	got, err := s.GetSettings("app")
	if err != nil {
		t.Fatal(err)
	}
	if got.Policy != PolicyNotify || got.HTTPURL != "" || got.Repo != "" || got.VerifyWindow != 0 || got.Container != "app" {
		t.Fatalf("defaults: %+v", got)
	}
	want := Settings{Container: "app", Policy: PolicyAuto, HTTPURL: "http://app:8080/health", Repo: "owner/app", VerifyWindow: 90 * time.Second}
	if err := s.SetSettings(want); err != nil {
		t.Fatal(err)
	}
	want.Policy = PolicyNever
	if err := s.SetSettings(want); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSettings("app")
	if err != nil || got != want {
		t.Fatalf("got %+v %v, want %+v", got, err, want)
	}
}

func TestSetSettingsRejectsInvalidPolicy(t *testing.T) {
	if err := openTest(t).SetSettings(Settings{Container: "app", Policy: "yolo"}); err == nil {
		t.Fatal("invalid policy accepted")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/store/ -run TestSettings`
Expected: FAIL, `undefined: Settings`.

- [ ] **Step 3: Implement settings**

Append to `internal/store/schema.sql`:

```sql

CREATE TABLE IF NOT EXISTS container_settings (
  container            TEXT PRIMARY KEY,
  policy               TEXT    NOT NULL DEFAULT 'notify',
  http_url             TEXT    NOT NULL DEFAULT '',
  repo                 TEXT    NOT NULL DEFAULT '',
  verify_window_seconds INTEGER NOT NULL DEFAULT 0
);
```

`internal/store/settings.go`:

```go
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	PolicyNotify = "notify"
	PolicyAuto   = "auto"
	PolicyNever  = "never"
)

func ValidPolicy(p string) bool {
	return p == PolicyNotify || p == PolicyAuto || p == PolicyNever
}

// Settings are the per-container options. A container without a row gets
// the defaults: notify only, no HTTP check, default verify window.
type Settings struct {
	Container    string
	Policy       string
	HTTPURL      string
	Repo         string // manual GitHub repo "owner/name"; beats label and mapping
	VerifyWindow time.Duration
}

func (s *Store) GetSettings(container string) (Settings, error) {
	st := Settings{Container: container, Policy: PolicyNotify}
	var window int64
	err := s.db.QueryRow(`SELECT policy, http_url, repo, verify_window_seconds FROM container_settings WHERE container = ?`, container).
		Scan(&st.Policy, &st.HTTPURL, &st.Repo, &window)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.VerifyWindow = time.Duration(window) * time.Second
	return st, nil
}

func (s *Store) SetSettings(st Settings) error {
	if !ValidPolicy(st.Policy) {
		return fmt.Errorf("invalid policy %q", st.Policy)
	}
	_, err := s.db.Exec(`INSERT INTO container_settings (container, policy, http_url, repo, verify_window_seconds)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(container) DO UPDATE SET policy = excluded.policy, http_url = excluded.http_url,
			repo = excluded.repo, verify_window_seconds = excluded.verify_window_seconds`,
		st.Container, st.Policy, st.HTTPURL, st.Repo, int64(st.VerifyWindow/time.Second))
	return err
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./internal/store/`
Expected: `ok`

- [ ] **Step 5: Write the failing verifier test**

`internal/engine/verifier_test.go`:

```go
package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/dockertest"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/verify"
)

func TestNewVerifierUsesContainerSettings(t *testing.T) {
	f := dockertest.New()
	f.AddImage("app:1", docker.ImageJSON{ID: "sha256:1"})
	c := f.AddContainer("c1", "app", "app:1", nil, true)
	c.State.Health = &docker.Health{Status: "healthy"}

	st, err := store.Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()

	v := NewVerifier(f, st, verify.Check{Window: 50 * time.Millisecond, Interval: 5 * time.Millisecond})
	ctx := context.Background()

	if got := v(ctx, "app", "c1"); !got.OK {
		t.Fatalf("no settings: %+v", got)
	}
	if err := st.SetSettings(store.Settings{Container: "app", Policy: store.PolicyNotify, HTTPURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if got := v(ctx, "app", "c1"); !got.OK {
		t.Fatalf("200 should pass: %+v", got)
	}
	status = http.StatusBadGateway
	if got := v(ctx, "app", "c1"); got.OK || !strings.Contains(got.Reason, "502") {
		t.Fatalf("502 should fail: %+v", got)
	}
	if got := v(ctx, "other", "c1"); !got.OK {
		t.Fatalf("settings of another container leaked: %+v", got)
	}
}
```

- [ ] **Step 6: Run to verify it fails**

Run: `go test ./internal/engine/ -run TestNewVerifier`
Expected: FAIL, `undefined: NewVerifier`.

- [ ] **Step 7: Change the `Verifier` type and its callers**

`internal/updater/updater.go`, replace the `Verifier` line:

```go
// Verifier judges a new container. name is the container name (its settings
// are looked up by name), id is the ID of the new container.
type Verifier func(ctx context.Context, name, id string) verify.Result
```

`internal/updater/run.go`: change `r.Verify(ctx, newID)` to `r.Verify(ctx, c.Name, newID)`.

`internal/updater/compose.go`: change `cp.Verify(ctx, newID)` to `cp.Verify(ctx, c.Name, newID)`.

`internal/updater/run_test.go`, replace the `verifier` helper:

```go
func verifier(ok bool) Verifier {
	return func(ctx context.Context, name, id string) verify.Result {
		if ok {
			return verify.Result{OK: true}
		}
		return verify.Result{Reason: "healthcheck unhealthy"}
	}
}
```

`test/integration/update_test.go`, in `setup`, replace the `e.verify = ...` line:

```go
	e.verify = func(ctx context.Context, name, id string) verify.Result { return verify.Verify(ctx, api, id, chk) }
```

- [ ] **Step 8: Implement `NewVerifier` and wire `main.go`**

`internal/engine/verifier.go`:

```go
package engine

import (
	"context"

	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
	"github.com/jordibrouwer/nextupdate/internal/verify"
)

// NewVerifier builds a verifier that reads the HTTP check and the verify
// window of each container from its settings, on top of the base check.
func NewVerifier(api docker.API, st *store.Store, base verify.Check) updater.Verifier {
	return func(ctx context.Context, name, id string) verify.Result {
		chk := base
		s, err := st.GetSettings(name)
		if err != nil {
			return verify.Result{Reason: "read settings: " + err.Error()}
		}
		if s.VerifyWindow > 0 {
			chk.Window = s.VerifyWindow
		}
		chk.HTTPURL = s.HTTPURL
		return verify.Verify(ctx, api, id, chk)
	}
}
```

`cmd/nextupdate/main.go`, replace these two lines

```go
	chk := verify.Check{Window: window, Interval: time.Second, MaxRestarts: 3}
	verifier := func(ctx context.Context, id string) verify.Result { return verify.Verify(ctx, api, id, chk) }
```

with

```go
	verifier := engine.NewVerifier(api, st, verify.Check{Window: window, Interval: time.Second, MaxRestarts: 3})
```

- [ ] **Step 9: Run everything**

Run: `gofmt -l . ; go vet ./... && go vet -tags integration ./test/... && go test -count=1 ./...`
Expected: no gofmt output, all packages `ok`.

- [ ] **Step 10: Commit**

```bash
git add internal cmd test
git commit -m "per-container settings and verifier"
```

---

### Task 2: Image labels

**Files:**
- Modify: `internal/registry/registry.go`, `internal/registry/registry_test.go`, `internal/engine/engine_test.go`

**Interfaces:**
- Produces:
  - Constants `registry.LabelVersion = "org.opencontainers.image.version"`, `registry.LabelSource = "org.opencontainers.image.source"`
  - `(*Remote).RemoteLabels(ctx context.Context, ref string) (map[string]string, error)`
  - `registry.LocalLabels(img docker.ImageJSON) map[string]string` (never nil)
  - `registry.Checker` gains `RemoteLabels(ctx context.Context, ref string) (map[string]string, error)`

- [ ] **Step 1: Write the failing tests**

Append to `internal/registry/registry_test.go`, and add the imports `v1 "github.com/google/go-containerregistry/pkg/v1"` and `"github.com/google/go-containerregistry/pkg/v1/mutate"`:

```go
func TestRemoteLabels(t *testing.T) {
	srv := httptest.NewServer(ggcrregistry.New())
	defer srv.Close()
	ref := strings.TrimPrefix(srv.URL, "http://") + "/team/app:2.0"

	base, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	img, err := mutate.Config(base, v1.Config{Labels: map[string]string{
		LabelVersion: "2.0.0", LabelSource: "https://github.com/team/app",
	}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(r, img); err != nil {
		t.Fatal(err)
	}

	got, err := NewRemote().RemoteLabels(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if got[LabelVersion] != "2.0.0" || got[LabelSource] != "https://github.com/team/app" {
		t.Fatalf("labels %v", got)
	}
}

func TestLocalLabels(t *testing.T) {
	img := docker.ImageJSON{Config: map[string]any{"Labels": map[string]any{LabelVersion: "1.4.0", "n": 5}}}
	got := LocalLabels(img)
	if got[LabelVersion] != "1.4.0" {
		t.Fatalf("labels %v", got)
	}
	if got := LocalLabels(docker.ImageJSON{}); got == nil || len(got) != 0 {
		t.Fatalf("want empty non-nil map, got %v", got)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/registry/`
Expected: FAIL, `undefined: LabelVersion` and `RemoteLabels`.

- [ ] **Step 3: Implement**

In `internal/registry/registry.go`, add the constants and change the `Checker` interface:

```go
const (
	LabelVersion = "org.opencontainers.image.version"
	LabelSource  = "org.opencontainers.image.source"
)

type Checker interface {
	RemoteDigest(ctx context.Context, ref string) (string, error)
	RemoteLabels(ctx context.Context, ref string) (map[string]string, error)
}
```

Add after `RemoteDigest`:

```go
// RemoteLabels reads the labels of the image config a registry serves. It
// downloads the manifest and the config blob, so call it only once an
// update is known.
func (r *Remote) RemoteLabels(ctx context.Context, ref string) (map[string]string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", ref, err)
	}
	img, err := remote.Image(parsed, append([]remote.Option{remote.WithContext(ctx)}, r.opts...)...)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", ref, err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", ref, err)
	}
	if cfg == nil || cfg.Config.Labels == nil {
		return map[string]string{}, nil
	}
	return cfg.Config.Labels, nil
}

// LocalLabels returns the labels of a local image.
func LocalLabels(img docker.ImageJSON) map[string]string {
	out := map[string]string{}
	m, _ := img.Config["Labels"].(map[string]any)
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}
```

Keep the `RemoteDigest` 401/403/404 mapping to `ErrNotPublished` unchanged.

- [ ] **Step 4: Keep the engine test compiling**

In `internal/engine/engine_test.go`, add after the `RemoteDigest` method of `fakeRegistry`:

```go
func (r fakeRegistry) RemoteLabels(ctx context.Context, ref string) (map[string]string, error) {
	return nil, nil
}
```

- [ ] **Step 5: Run everything**

Run: `gofmt -l . ; go vet ./... && go test -count=1 ./...`
Expected: all `ok`.

- [ ] **Step 6: Commit**

```bash
git add internal
git commit -m "read image labels for versions"
```

---

### Task 3: Semver

**Files:**
- Create: `internal/semver/semver.go`
- Test: `internal/semver/semver_test.go`

**Interfaces:**
- Produces:
  - `semver.Version{Major, Minor, Patch int; Pre string}` with `String() string`
  - `semver.Parse(s string) (Version, bool)`: accepts a leading `v`, `1`, `1.2`, `1.2.3`, `-pre` and `+build` suffixes
  - `semver.Compare(a, b Version) int` (-1, 0, 1; a pre-release sorts below its release)
  - `semver.Change` with constants `None, Patch, Minor, Major, Downgrade` and `String()`
  - `semver.Diff(from, to Version) Change`; while both are `0.x`, a minor difference counts as `Major`

- [ ] **Step 1: Write the failing tests**

`internal/semver/semver_test.go`:

```go
package semver

import "testing"

func TestParse(t *testing.T) {
	cases := []struct {
		in   string
		want Version
		ok   bool
	}{
		{"1.2.3", Version{1, 2, 3, ""}, true},
		{"v1.2.3", Version{1, 2, 3, ""}, true},
		{"2", Version{2, 0, 0, ""}, true},
		{"2.1", Version{2, 1, 0, ""}, true},
		{"1.2.3-rc.1", Version{1, 2, 3, "rc.1"}, true},
		{"1.2.3+build5", Version{1, 2, 3, ""}, true},
		{"", Version{}, false},
		{"latest", Version{}, false},
		{"1.2.3.4", Version{}, false},
		{"1.x.3", Version{}, false},
		{"-1.2.3", Version{}, false},
	}
	for _, c := range cases {
		got, ok := Parse(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("Parse(%q) = %+v, %v; want %+v, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestCompare(t *testing.T) {
	v := func(s string) Version { x, _ := Parse(s); return x }
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"1.2.4", "1.2.3", 1},
		{"1.10.0", "1.9.0", 1},
		{"2.0.0", "10.0.0", -1},
		{"1.2.3-rc.1", "1.2.3", -1},
		{"1.2.3", "1.2.3-rc.1", 1},
		{"1.2.3-alpha", "1.2.3-beta", -1},
	}
	for _, c := range cases {
		if got := Compare(v(c.a), v(c.b)); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestDiff(t *testing.T) {
	v := func(s string) Version { x, _ := Parse(s); return x }
	cases := []struct {
		from, to string
		want     Change
	}{
		{"1.2.3", "1.2.3", None},
		{"1.2.3", "1.2.4", Patch},
		{"1.2.3", "1.3.0", Minor},
		{"1.2.3", "2.0.0", Major},
		{"1.2.3", "1.2.2", Downgrade},
		{"1.2.3", "1.2.4-rc.1", Patch},
		{"0.4.1", "0.4.2", Patch},
		{"0.4.1", "0.5.0", Major},
		{"0.9.0", "1.0.0", Major},
	}
	for _, c := range cases {
		if got := Diff(v(c.from), v(c.to)); got != c.want {
			t.Errorf("Diff(%s, %s) = %s, want %s", c.from, c.to, got, c.want)
		}
	}
}

func TestString(t *testing.T) {
	if got := (Version{1, 2, 3, "rc.1"}).String(); got != "1.2.3-rc.1" {
		t.Errorf("got %q", got)
	}
	if got := (Version{1, 2, 3, ""}).String(); got != "1.2.3" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/semver/`
Expected: FAIL, `undefined: Parse`.

- [ ] **Step 3: Implement**

`internal/semver/semver.go`:

```go
// Package semver parses and compares the version labels of container images.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Parse reads "1", "1.2", "1.2.3", "v1.2.3", "1.2.3-rc.1" and "1.2.3+build".
func Parse(s string) (Version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if core == "" || len(parts) > 3 {
		return Version{}, false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return Version{}, false
		}
		n[i] = v
	}
	return Version{n[0], n[1], n[2], pre}, true
}

// Compare returns -1, 0 or 1. A pre-release sorts below its release.
func Compare(a, b Version) int {
	for _, d := range []int{a.Major - b.Major, a.Minor - b.Minor, a.Patch - b.Patch} {
		if d < 0 {
			return -1
		}
		if d > 0 {
			return 1
		}
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "":
		return 1
	case b.Pre == "":
		return -1
	case a.Pre < b.Pre:
		return -1
	}
	return 1
}

type Change int

const (
	None Change = iota
	Patch
	Minor
	Major
	Downgrade
)

func (c Change) String() string {
	return [...]string{"none", "patch", "minor", "major", "downgrade"}[c]
}

// Diff classifies the step from one version to another. While both are 0.x
// a minor step counts as major, because 0.x has no stability promise.
func Diff(from, to Version) Change {
	switch c := Compare(from, to); {
	case c > 0:
		return Downgrade
	case c == 0:
		return None
	}
	switch {
	case to.Major != from.Major:
		return Major
	case to.Minor != from.Minor:
		if from.Major == 0 {
			return Major
		}
		return Minor
	}
	return Patch
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `gofmt -l internal/semver; go vet ./internal/semver/ && go test ./internal/semver/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/semver
git commit -m "semver parse, compare and diff"
```

---

### Task 4: Changelog

**Files:**
- Create: `internal/changelog/mapping.go`, `internal/changelog/mappings.json`, `internal/changelog/github.go`
- Test: `internal/changelog/changelog_test.go`

**Interfaces:**
- Consumes: `semver.Parse`, `semver.Compare`, `registry.LabelSource` is **not** imported (keep the package free of registry); the label key is passed in by the caller as part of the labels map, using the literal `org.opencontainers.image.source`.
- Produces:
  - `changelog.Release{Tag, Name, Body, URL string; PublishedAt time.Time; Prerelease bool}`
  - `changelog.Source` interface: `Releases(ctx context.Context, repo string) ([]Release, error)`
  - `changelog.Cache` interface: `Get(repo string) (etag string, body []byte, ok bool)`, `Put(repo, etag string, body []byte) error`
  - `changelog.GitHub{Client *http.Client; BaseURL, Token string; Cache Cache}` implementing `Source`
  - `changelog.ErrRateLimited`
  - `changelog.Between(releases []Release, oldV, newV string) []Release` (newest first)
  - `changelog.Mapping{Image, Repo string; Breaking []string}`, `changelog.Mappings`
  - `changelog.LoadMappings(data []byte) (*Mappings, error)`, `changelog.DefaultMappings() *Mappings`, `(*Mappings).Lookup(image string) (Mapping, bool)` (nil-safe)
  - `changelog.RepoFromURL(u string) (string, bool)`
  - `changelog.Repo{Name, Source string}` and `changelog.Resolve(image string, labels map[string]string, m *Mappings, manual string) (Repo, bool)`; `Source` is `manual`, `label` or `mapping`

- [ ] **Step 1: Write the failing tests**

`internal/changelog/changelog_test.go`:

```go
package changelog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRepoFromURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/owner/name":          "owner/name",
		"https://github.com/owner/name.git":      "owner/name",
		"https://www.github.com/owner/name/":     "owner/name",
		"github.com/owner/name":                  "owner/name",
		"https://github.com/owner/name/tree/dev": "owner/name",
	}
	for in, want := range cases {
		if got, ok := RepoFromURL(in); !ok || got != want {
			t.Errorf("RepoFromURL(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "https://gitlab.com/o/n", "https://github.com/onlyowner", "not a url"} {
		if got, ok := RepoFromURL(in); ok {
			t.Errorf("RepoFromURL(%q) = %q, want no match", in, got)
		}
	}
}

func TestMappingsLookupNormalisesImage(t *testing.T) {
	m, err := LoadMappings([]byte(`{"entries":[{"image":"docker.io/vaultwarden/server","repo":"dani-garcia/vaultwarden","breaking":["1.99.0"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := m.Lookup("vaultwarden/server:1.30.0")
	if !ok || got.Repo != "dani-garcia/vaultwarden" || len(got.Breaking) != 1 {
		t.Fatalf("got %+v %v", got, ok)
	}
	if _, ok := m.Lookup("ghcr.io/other/app"); ok {
		t.Fatal("unrelated image matched")
	}
	var nilMappings *Mappings
	if _, ok := nilMappings.Lookup("x"); ok {
		t.Fatal("nil mappings must not match")
	}
}

func TestDefaultMappingsLoad(t *testing.T) {
	if _, ok := DefaultMappings().Lookup("docker.io/vaultwarden/server"); !ok {
		t.Fatal("bundled mapping missing vaultwarden")
	}
}

func TestResolvePriority(t *testing.T) {
	m, _ := LoadMappings([]byte(`{"entries":[{"image":"ghcr.io/team/app","repo":"mapped/app"}]}`))
	labels := map[string]string{"org.opencontainers.image.source": "https://github.com/labelled/app"}

	if r, ok := Resolve("ghcr.io/team/app:1", labels, m, "manual/app"); !ok || r.Name != "manual/app" || r.Source != "manual" {
		t.Errorf("manual: %+v", r)
	}
	if r, ok := Resolve("ghcr.io/team/app:1", labels, m, ""); !ok || r.Name != "labelled/app" || r.Source != "label" {
		t.Errorf("label: %+v", r)
	}
	if r, ok := Resolve("ghcr.io/team/app:1", nil, m, ""); !ok || r.Name != "mapped/app" || r.Source != "mapping" {
		t.Errorf("mapping: %+v", r)
	}
	if _, ok := Resolve("ghcr.io/team/none:1", nil, m, ""); ok {
		t.Error("nothing known, want no repo")
	}
}

func rel(tag string, pre bool) Release { return Release{Tag: tag, Prerelease: pre} }

func TestBetween(t *testing.T) {
	all := []Release{rel("v2.1.0", false), rel("v2.0.0", false), rel("v2.0.0-rc.1", true), rel("v1.5.0", false), rel("v1.4.0", false), rel("nightly", false)}

	tags := func(rs []Release) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Tag)
		}
		return out
	}
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	if got := tags(Between(all, "1.4.0", "2.0.0")); !eq(got, []string{"v2.0.0", "v1.5.0"}) {
		t.Errorf("range: %v", got)
	}
	if got := tags(Between(all, "", "2.0.0")); !eq(got, []string{"v2.0.0"}) {
		t.Errorf("new only: %v", got)
	}
	if got := Between(all, "1.4.0", ""); got != nil {
		t.Errorf("no new version: %v", got)
	}
}

type memCache map[string]struct {
	etag string
	body []byte
}

func (m memCache) Get(repo string) (string, []byte, bool) {
	e, ok := m[repo]
	return e.etag, e.body, ok
}
func (m memCache) Put(repo, etag string, body []byte) error {
	m[repo] = struct {
		etag string
		body []byte
	}{etag, body}
	return nil
}

func TestGitHubReleasesUsesETag(t *testing.T) {
	var gotAuth, gotINM string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotAuth, gotINM = r.Header.Get("Authorization"), r.Header.Get("If-None-Match")
		if r.URL.Path != "/repos/o/n/releases" {
			t.Errorf("path %s", r.URL.Path)
		}
		if gotINM == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		io.WriteString(w, `[{"tag_name":"v1.1.0","name":"One one","body":"notes","html_url":"https://x/1.1.0","published_at":"2026-01-02T03:04:05Z","prerelease":false,"draft":false},
			{"tag_name":"v1.2.0","draft":true}]`)
	}))
	defer srv.Close()
	g := &GitHub{BaseURL: srv.URL, Token: "tok", Cache: memCache{}}

	got, err := g.Releases(context.Background(), "o/n")
	if err != nil || len(got) != 1 || got[0].Tag != "v1.1.0" || got[0].Body != "notes" || got[0].URL != "https://x/1.1.0" {
		t.Fatalf("first call: %+v %v", got, err)
	}
	if gotAuth != "Bearer tok" || gotINM != "" {
		t.Fatalf("first call headers: auth %q inm %q", gotAuth, gotINM)
	}
	got, err = g.Releases(context.Background(), "o/n")
	if err != nil || len(got) != 1 || got[0].Tag != "v1.1.0" {
		t.Fatalf("second call (304): %+v %v", got, err)
	}
	if gotINM != `"abc"` || calls != 2 {
		t.Fatalf("second call: inm %q calls %d", gotINM, calls)
	}
}

func TestGitHubStatuses(t *testing.T) {
	for status, check := range map[int]func(rs []Release, err error) bool{
		http.StatusNotFound:     func(rs []Release, err error) bool { return err == nil && rs == nil },
		http.StatusForbidden:    func(rs []Release, err error) bool { return errors.Is(err, ErrRateLimited) },
		http.StatusTooManyRequests: func(rs []Release, err error) bool { return errors.Is(err, ErrRateLimited) },
		http.StatusInternalServerError: func(rs []Release, err error) bool { return err != nil && !errors.Is(err, ErrRateLimited) },
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		rs, err := (&GitHub{BaseURL: srv.URL}).Releases(context.Background(), "o/n")
		srv.Close()
		if !check(rs, err) {
			t.Errorf("status %d: %v %v", status, rs, err)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/changelog/`
Expected: FAIL, `undefined: RepoFromURL`.

- [ ] **Step 3: Implement mapping and resolve**

`internal/changelog/mappings.json`:

```json
{
  "entries": [
    { "image": "docker.io/vaultwarden/server", "repo": "dani-garcia/vaultwarden", "breaking": [] },
    { "image": "docker.io/jellyfin/jellyfin", "repo": "jellyfin/jellyfin", "breaking": [] },
    { "image": "docker.io/louislam/uptime-kuma", "repo": "louislam/uptime-kuma", "breaking": [] },
    { "image": "ghcr.io/immich-app/immich-server", "repo": "immich-app/immich", "breaking": [] },
    { "image": "ghcr.io/home-assistant/home-assistant", "repo": "home-assistant/core", "breaking": [] },
    { "image": "docker.io/jordibrouwer/nextdash", "repo": "jordibrouwer/nextdash", "breaking": [] }
  ]
}
```

`internal/changelog/mapping.go`:

```go
// Package changelog finds the release notes between two versions of an image.
package changelog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

const labelSource = "org.opencontainers.image.source"

//go:embed mappings.json
var defaultMappings []byte

// Mapping ties an image to its GitHub repo. Breaking lists versions that
// are known to need manual steps.
type Mapping struct {
	Image    string   `json:"image"`
	Repo     string   `json:"repo"`
	Breaking []string `json:"breaking"`
}

type Mappings struct{ byImage map[string]Mapping }

func normalize(image string) (string, bool) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", false
	}
	return ref.Context().Name(), true
}

func LoadMappings(data []byte) (*Mappings, error) {
	var f struct {
		Entries []Mapping `json:"entries"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse mappings: %w", err)
	}
	m := &Mappings{byImage: map[string]Mapping{}}
	for _, e := range f.Entries {
		key, ok := normalize(e.Image)
		if !ok {
			return nil, fmt.Errorf("mapping image %q is not a valid reference", e.Image)
		}
		m.byImage[key] = e
	}
	return m, nil
}

// DefaultMappings loads the mappings bundled in the binary.
func DefaultMappings() *Mappings {
	m, err := LoadMappings(defaultMappings)
	if err != nil {
		panic("bundled mappings.json is invalid: " + err.Error())
	}
	return m
}

func (m *Mappings) Lookup(image string) (Mapping, bool) {
	if m == nil {
		return Mapping{}, false
	}
	key, ok := normalize(image)
	if !ok {
		return Mapping{}, false
	}
	e, ok := m.byImage[key]
	return e, ok
}

// RepoFromURL turns a GitHub URL into "owner/name".
func RepoFromURL(u string) (string, bool) {
	if u == "" || strings.ContainsAny(u, " \t") {
		return "", false
	}
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	p, err := url.Parse(u)
	if err != nil {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(p.Host), "www.")
	if host != "github.com" {
		return "", false
	}
	parts := strings.Split(strings.Trim(p.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"), true
}

type Repo struct {
	Name   string // "owner/name"
	Source string // "manual", "label" or "mapping"
}

// Resolve finds the GitHub repo of an image: a manual entry wins, then the
// source label of the image, then the bundled mapping.
func Resolve(image string, labels map[string]string, m *Mappings, manual string) (Repo, bool) {
	if manual != "" {
		return Repo{manual, "manual"}, true
	}
	if r, ok := RepoFromURL(labels[labelSource]); ok {
		return Repo{r, "label"}, true
	}
	if e, ok := m.Lookup(image); ok && e.Repo != "" {
		return Repo{e.Repo, "mapping"}, true
	}
	return Repo{}, false
}
```

- [ ] **Step 4: Implement GitHub and Between**

`internal/changelog/github.go`:

```go
package changelog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/semver"
)

type Release struct {
	Tag         string
	Name        string
	Body        string
	URL         string
	PublishedAt time.Time
	Prerelease  bool
}

type Source interface {
	Releases(ctx context.Context, repo string) ([]Release, error)
}

// Cache keeps the last response per repo, so a repeat call can use a
// conditional request.
type Cache interface {
	Get(repo string) (etag string, body []byte, ok bool)
	Put(repo, etag string, body []byte) error
}

var ErrRateLimited = errors.New("github rate limit reached")

type GitHub struct {
	Client  *http.Client
	BaseURL string // default https://api.github.com
	Token   string
	Cache   Cache
}

func (g *GitHub) Releases(ctx context.Context, repo string) ([]Release, error) {
	base := g.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/repos/"+repo+"/releases?per_page=30", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	var cached []byte
	if g.Cache != nil {
		if etag, body, ok := g.Cache.Get(repo); ok && etag != "" {
			req.Header.Set("If-None-Match", etag)
			cached = body
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github %s: %w", repo, err)
	}
	defer resp.Body.Close()

	var body []byte
	switch resp.StatusCode {
	case http.StatusOK:
		body, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, fmt.Errorf("github %s: %w", repo, err)
		}
		if g.Cache != nil {
			_ = g.Cache.Put(repo, resp.Header.Get("ETag"), body) // a cache miss next time is harmless
		}
	case http.StatusNotModified:
		if cached == nil {
			return nil, fmt.Errorf("github %s: 304 without a cached copy", repo)
		}
		body = cached
	case http.StatusNotFound:
		return nil, nil // no such repo, or private
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, ErrRateLimited
	default:
		return nil, fmt.Errorf("github %s: status %d", repo, resp.StatusCode)
	}
	return parseReleases(body)
}

func parseReleases(body []byte) ([]Release, error) {
	var raw []struct {
		Tag         string    `json:"tag_name"`
		Name        string    `json:"name"`
		Body        string    `json:"body"`
		URL         string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Prerelease  bool      `json:"prerelease"`
		Draft       bool      `json:"draft"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse releases: %w", err)
	}
	var out []Release
	for _, r := range raw {
		if r.Draft {
			continue
		}
		out = append(out, Release{Tag: r.Tag, Name: r.Name, Body: r.Body, URL: r.URL, PublishedAt: r.PublishedAt, Prerelease: r.Prerelease})
	}
	return out, nil
}

// Between returns the releases newer than oldV and up to newV, newest first.
// Without a known old version only the release of newV itself is returned;
// without a known new version nothing is. Tags that are not versions are
// skipped, and pre-releases only count when newV is one.
func Between(releases []Release, oldV, newV string) []Release {
	n, okN := semver.Parse(newV)
	if !okN {
		return nil
	}
	o, okO := semver.Parse(oldV)
	type item struct {
		r Release
		v semver.Version
	}
	var picked []item
	for _, r := range releases {
		v, ok := semver.Parse(r.Tag)
		if !ok || (r.Prerelease && n.Pre == "") {
			continue
		}
		if okO && semver.Compare(v, o) > 0 && semver.Compare(v, n) <= 0 || !okO && semver.Compare(v, n) == 0 {
			picked = append(picked, item{r, v})
		}
	}
	sort.SliceStable(picked, func(i, j int) bool { return semver.Compare(picked[i].v, picked[j].v) > 0 })
	var out []Release
	for _, p := range picked {
		out = append(out, p.r)
	}
	return out
}
```

- [ ] **Step 5: Run to verify it passes**

Run: `gofmt -l internal/changelog; go vet ./internal/changelog/ && go test ./internal/changelog/`
Expected: `ok`. If `gofmt -l` lists the test file (map-literal alignment), run `gofmt -w internal/changelog` and rerun.

- [ ] **Step 6: Commit**

```bash
git add internal/changelog
git commit -m "changelog: repo lookup, github releases, version range"
```

---

### Task 5: Classifier

**Files:**
- Create: `internal/classify/classify.go`
- Test: `internal/classify/classify_test.go`

**Interfaces:**
- Consumes: `changelog.Release`, `semver`.
- Produces: `classify.Result{Breaking bool; Reasons []string}` and `classify.Classify(oldV, newV string, releases []changelog.Release, flagged []string) Result`.

Rules: a major step or downgrade is breaking; a flagged version `f` with `old < f <= new` (or `f == new` when old is unknown) is breaking; a release whose name or body contains a keyword is breaking, one reason per release.

- [ ] **Step 1: Write the failing tests**

`internal/classify/classify_test.go`:

```go
package classify

import (
	"strings"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/changelog"
)

func TestClassify(t *testing.T) {
	rel := func(tag, body string) changelog.Release { return changelog.Release{Tag: tag, Body: body} }
	cases := []struct {
		name     string
		oldV     string
		newV     string
		releases []changelog.Release
		flagged  []string
		breaking bool
		reason   string
	}{
		{"patch is fine", "1.4.0", "1.4.1", []changelog.Release{rel("v1.4.1", "Fixes a typo.")}, nil, false, ""},
		{"major jump", "1.9.0", "2.0.0", nil, nil, true, "major version"},
		{"downgrade", "2.0.0", "1.9.0", nil, nil, true, "older version"},
		{"keyword in notes", "1.4.0", "1.5.0", []changelog.Release{rel("v1.5.0", "BREAKING: config keys renamed")}, nil, true, `mentions "breaking"`},
		{"migration keyword", "1.4.0", "1.5.0", []changelog.Release{rel("v1.5.0", "Run the database Migration first")}, nil, true, `mentions "migration"`},
		{"flagged version in range", "1.4.0", "1.6.0", nil, []string{"1.5.0"}, true, "community mapping"},
		{"flagged version already passed", "1.5.0", "1.6.0", nil, []string{"1.5.0"}, false, ""},
		{"flagged, old unknown, equals new", "", "1.5.0", nil, []string{"v1.5.0"}, true, "community mapping"},
		{"unknown versions, quiet notes", "", "", nil, nil, false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.oldV, c.newV, c.releases, c.flagged)
			if got.Breaking != c.breaking {
				t.Fatalf("breaking = %v, want %v (%v)", got.Breaking, c.breaking, got.Reasons)
			}
			if c.breaking && !strings.Contains(strings.Join(got.Reasons, "|"), c.reason) {
				t.Fatalf("reasons %v do not contain %q", got.Reasons, c.reason)
			}
			if !c.breaking && len(got.Reasons) != 0 {
				t.Fatalf("reasons on a clean update: %v", got.Reasons)
			}
		})
	}
}

func TestOneReasonPerRelease(t *testing.T) {
	got := Classify("1.0.0", "1.1.0", []changelog.Release{{Tag: "v1.1.0", Body: "breaking and deprecated and migration"}}, nil)
	if len(got.Reasons) != 1 {
		t.Fatalf("want one reason for one release, got %v", got.Reasons)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/classify/`
Expected: FAIL, `undefined: Classify`.

- [ ] **Step 3: Implement**

`internal/classify/classify.go`:

```go
// Package classify decides whether an update is breaking.
package classify

import (
	"fmt"
	"strings"

	"github.com/jordibrouwer/nextupdate/internal/changelog"
	"github.com/jordibrouwer/nextupdate/internal/semver"
)

// keywords in release notes that suggest manual work.
var keywords = []string{"breaking", "migration", "deprecated", "backwards incompatible", "backward incompatible", "action required", "manual step"}

type Result struct {
	Breaking bool
	Reasons  []string
}

// Classify judges an update from oldV to newV. releases are the notes in
// between; flagged are versions the community mapping marks as breaking.
func Classify(oldV, newV string, releases []changelog.Release, flagged []string) Result {
	var res Result
	add := func(format string, a ...any) {
		res.Breaking = true
		res.Reasons = append(res.Reasons, fmt.Sprintf(format, a...))
	}
	o, okO := semver.Parse(oldV)
	n, okN := semver.Parse(newV)
	if okO && okN {
		switch semver.Diff(o, n) {
		case semver.Major:
			add("Major version change from %s to %s.", o, n)
		case semver.Downgrade:
			add("The new version %s is older than the running version %s.", n, o)
		}
	}
	if okN {
		for _, f := range flagged {
			fv, ok := semver.Parse(f)
			if !ok || semver.Compare(fv, n) > 0 || okO && semver.Compare(fv, o) <= 0 || !okO && semver.Compare(fv, n) != 0 {
				continue
			}
			add("Version %s is marked as breaking in the community mapping.", fv)
		}
	}
	for _, r := range releases {
		text := strings.ToLower(r.Name + "\n" + r.Body)
		for _, k := range keywords {
			if strings.Contains(text, k) {
				add("Release %s mentions %q.", r.Tag, k)
				break
			}
		}
	}
	return res
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `gofmt -l internal/classify; go vet ./internal/classify/ && go test ./internal/classify/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/classify
git commit -m "classify breaking updates"
```

---

### Task 6: Update info and changelog cache in the store, and engine enrichment

**Files:**
- Modify: `internal/store/schema.sql`, `internal/engine/engine.go`, `internal/engine/engine_test.go`
- Create: `internal/store/info.go`
- Test: `internal/store/info_test.go`

**Interfaces:**
- Consumes: Tasks 1-5.
- Produces:
  - `store.Info{Container, OldVersion, NewVersion, Repo string; Breaking bool; Reasons []string}`
  - `(*Store).ReplaceInfo(list []Info) error`, `(*Store).ListInfo() ([]Info, error)` (ordered by container)
  - `store.ChangelogCache` with `Get(repo string) (etag string, body []byte, ok bool)` and `Put(repo, etag string, body []byte) error`; obtained from `(*Store).ChangelogCache() *ChangelogCache`
  - `engine.Engine` gains `Changelog changelog.Source` and `Mappings *changelog.Mappings`; `Check` writes one `Info` per available update

- [ ] **Step 1: Write the failing store tests**

`internal/store/info_test.go`:

```go
package store

import "testing"

func TestInfoReplaceAndList(t *testing.T) {
	s := openTest(t)
	if err := s.ReplaceInfo([]Info{
		{Container: "b", OldVersion: "1.0.0", NewVersion: "2.0.0", Repo: "o/b", Breaking: true, Reasons: []string{"Major version change from 1.0.0 to 2.0.0."}},
		{Container: "a", OldVersion: "1.0.0", NewVersion: "1.0.1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceInfo([]Info{
		{Container: "b", OldVersion: "1.0.0", NewVersion: "2.0.0", Repo: "o/b", Breaking: true, Reasons: []string{"Major version change from 1.0.0 to 2.0.0."}},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListInfo()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Container != "b" || !got[0].Breaking || got[0].Repo != "o/b" || len(got[0].Reasons) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestChangelogCache(t *testing.T) {
	c := openTest(t).ChangelogCache()
	if _, _, ok := c.Get("o/n"); ok {
		t.Fatal("empty cache returned a hit")
	}
	if err := c.Put("o/n", `"e1"`, []byte(`[1]`)); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("o/n", `"e2"`, []byte(`[2]`)); err != nil {
		t.Fatal(err)
	}
	etag, body, ok := c.Get("o/n")
	if !ok || etag != `"e2"` || string(body) != `[2]` {
		t.Fatalf("got %q %q %v", etag, body, ok)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/store/ -run 'TestInfo|TestChangelog'`
Expected: FAIL, `undefined: Info`.

- [ ] **Step 3: Implement the store side**

Append to `internal/store/schema.sql`:

```sql

CREATE TABLE IF NOT EXISTS available_info (
  container   TEXT PRIMARY KEY,
  old_version TEXT    NOT NULL DEFAULT '',
  new_version TEXT    NOT NULL DEFAULT '',
  repo        TEXT    NOT NULL DEFAULT '',
  breaking    INTEGER NOT NULL DEFAULT 0,
  reasons     TEXT    NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS changelog_cache (
  repo       TEXT PRIMARY KEY,
  etag       TEXT    NOT NULL,
  body       BLOB    NOT NULL,
  fetched_at INTEGER NOT NULL
);
```

`internal/store/info.go`:

```go
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Info describes an available update: versions, the GitHub repo the notes
// come from, and whether the update looks breaking (and why).
type Info struct {
	Container  string
	OldVersion string
	NewVersion string
	Repo       string
	Breaking   bool
	Reasons    []string
}

func (s *Store) ReplaceInfo(list []Info) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM available_info`); err != nil {
		return err
	}
	for _, i := range list {
		reasons, err := json.Marshal(nonNilStrings(i.Reasons))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO available_info (container, old_version, new_version, repo, breaking, reasons) VALUES (?, ?, ?, ?, ?, ?)`,
			i.Container, i.OldVersion, i.NewVersion, i.Repo, i.Breaking, string(reasons)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListInfo() ([]Info, error) {
	rows, err := s.db.Query(`SELECT container, old_version, new_version, repo, breaking, reasons FROM available_info ORDER BY container`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Info
	for rows.Next() {
		var i Info
		var reasons string
		if err := rows.Scan(&i.Container, &i.OldVersion, &i.NewVersion, &i.Repo, &i.Breaking, &reasons); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(reasons), &i.Reasons); err != nil {
			return nil, fmt.Errorf("info %s reasons: %w", i.Container, err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ChangelogCache stores the last GitHub response per repo.
type ChangelogCache struct{ db *sql.DB }

func (s *Store) ChangelogCache() *ChangelogCache { return &ChangelogCache{db: s.db} }

func (c *ChangelogCache) Get(repo string) (etag string, body []byte, ok bool) {
	if err := c.db.QueryRow(`SELECT etag, body FROM changelog_cache WHERE repo = ?`, repo).Scan(&etag, &body); err != nil {
		return "", nil, false
	}
	return etag, body, true
}

func (c *ChangelogCache) Put(repo, etag string, body []byte) error {
	_, err := c.db.Exec(`INSERT INTO changelog_cache (repo, etag, body, fetched_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(repo) DO UPDATE SET etag = excluded.etag, body = excluded.body, fetched_at = excluded.fetched_at`,
		repo, etag, body, time.Now().UnixMilli())
	return err
}
```

- [ ] **Step 4: Run to verify the store passes**

Run: `go test -count=1 ./internal/store/`
Expected: `ok`

- [ ] **Step 5: Update the engine test (failing first)**

In `internal/engine/engine_test.go`:

(a) Replace the whole `fakeRegistry` type and its two methods:

```go
type fakeRegistry struct {
	digests map[string]string
	labels  map[string]map[string]string
}

func (r fakeRegistry) RemoteDigest(ctx context.Context, ref string) (string, error) {
	if d, ok := r.digests[ref]; ok {
		return d, nil
	}
	if ref == "down:1" {
		return "", fmt.Errorf("head %s: %w", ref, registry.ErrNotPublished)
	}
	return "", fmt.Errorf("unknown %s", ref)
}

func (r fakeRegistry) RemoteLabels(ctx context.Context, ref string) (map[string]string, error) {
	return r.labels[ref], nil
}

type fakeChangelog struct{ releases []changelog.Release }

func (c fakeChangelog) Releases(ctx context.Context, repo string) ([]changelog.Release, error) {
	return c.releases, nil
}
```

(b) In `newEngine`, give the `app:latest` image local labels and replace the registry literal. Change the `AddImage("app:latest", ...)` line to

```go
	f.AddImage("app:latest", docker.ImageJSON{ID: "sha256:a", RepoDigests: []string{"app@" + dOld},
		Config: map[string]any{"Labels": map[string]any{"org.opencontainers.image.version": "1.4.0"}}})
```

and the `Registry:` line to

```go
		Registry: fakeRegistry{
			digests: map[string]string{"app:latest": dNew, "same:1": dOld},
			labels: map[string]map[string]string{"app:latest": {
				"org.opencontainers.image.version": "2.0.0",
				"org.opencontainers.image.source":  "https://github.com/o/app",
			}},
		},
		Changelog: fakeChangelog{releases: []changelog.Release{
			{Tag: "v2.0.0", Body: "Config keys were renamed."}, {Tag: "v1.5.0", Body: "Small fixes."}, {Tag: "v1.4.0", Body: "Old."},
		}},
```

Add `"github.com/jordibrouwer/nextupdate/internal/changelog"` to the imports.

(c) Add `"strings"` to the imports and add this test:

```go
func TestCheckDescribesUpdates(t *testing.T) {
	e, _, _ := newEngine(t)
	if _, err := e.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	infos, err := e.Store.ListInfo()
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 2 || infos[0].Container != "app" {
		t.Fatalf("want info for app and web, got %+v", infos)
	}
	app := infos[0]
	if app.OldVersion != "1.4.0" || app.NewVersion != "2.0.0" || app.Repo != "o/app" || !app.Breaking {
		t.Fatalf("app info: %+v", app)
	}
	if !strings.Contains(strings.Join(app.Reasons, " "), "Major version change from 1.4.0 to 2.0.0") {
		t.Fatalf("missing major-change reason: %v", app.Reasons)
	}
}
```

(d) In `TestCheck` the assertion on `buf.Len()` must still hold: the fake registry returns no error for labels, and the fake changelog no error, so no log lines are written.

- [ ] **Step 6: Run to verify it fails**

Run: `go test -count=1 ./internal/engine/`
Expected: FAIL, `unknown field Changelog in struct literal`.

- [ ] **Step 7: Implement the enrichment**

In `internal/engine/engine.go`, add to the imports `"github.com/jordibrouwer/nextupdate/internal/changelog"` and `"github.com/jordibrouwer/nextupdate/internal/classify"`, add two fields to `Engine`:

```go
	Changelog changelog.Source
	Mappings  *changelog.Mappings
```

and replace the body of `Check` with:

```go
func (e *Engine) Check(ctx context.Context) ([]store.Available, error) {
	containers, err := discovery.Discover(ctx, e.API)
	if err != nil {
		return nil, err
	}
	var out []store.Available
	var infos []store.Info
	for _, c := range containers {
		img, err := e.API.InspectImage(ctx, c.ImageID)
		if err != nil {
			e.logf("check %s: inspect image: %v", c.Name, err)
			continue
		}
		local, ok := registry.LocalDigest(img, c.Image)
		if !ok {
			continue // built locally, no registry to compare with
		}
		remote, err := e.Registry.RemoteDigest(ctx, c.Image)
		if errors.Is(err, registry.ErrNotPublished) {
			continue // local build or private repo: nothing to compare with
		}
		if err != nil {
			e.logf("check %s: %v", c.Name, err)
			continue
		}
		if remote == local {
			continue
		}
		out = append(out, store.Available{Container: c.Name, Image: c.Image, LocalDigest: local, RemoteDigest: remote, DetectedAt: e.now()})
		infos = append(infos, e.describe(ctx, c, img))
	}
	if err := e.Store.ReplaceAvailable(out); err != nil {
		return nil, err
	}
	if err := e.Store.ReplaceInfo(infos); err != nil {
		return nil, err
	}
	return e.Store.ListAvailable()
}

// describe collects versions, release notes and the breaking verdict of an
// available update. Missing pieces are logged and left empty: an update
// without notes is still an update.
func (e *Engine) describe(ctx context.Context, c discovery.Container, local docker.ImageJSON) store.Info {
	info := store.Info{Container: c.Name, OldVersion: registry.LocalLabels(local)[registry.LabelVersion]}
	remoteLabels, err := e.Registry.RemoteLabels(ctx, c.Image)
	if err != nil {
		e.logf("check %s: read remote labels: %v", c.Name, err)
	}
	info.NewVersion = remoteLabels[registry.LabelVersion]
	settings, err := e.Store.GetSettings(c.Name)
	if err != nil {
		e.logf("check %s: read settings: %v", c.Name, err)
	}
	mapping, _ := e.Mappings.Lookup(c.Image)
	var releases []changelog.Release
	if repo, ok := changelog.Resolve(c.Image, remoteLabels, e.Mappings, settings.Repo); ok {
		info.Repo = repo.Name
		if e.Changelog != nil {
			all, err := e.Changelog.Releases(ctx, repo.Name)
			if err != nil {
				e.logf("check %s: release notes: %v", c.Name, err)
			}
			releases = changelog.Between(all, info.OldVersion, info.NewVersion)
		}
	}
	verdict := classify.Classify(info.OldVersion, info.NewVersion, releases, mapping.Breaking)
	info.Breaking, info.Reasons = verdict.Breaking, verdict.Reasons
	return info
}
```

(`Mappings.Lookup` is nil-safe, so an engine without mappings works.)

- [ ] **Step 8: Run everything**

Run: `gofmt -l . ; go vet ./... && go test -race -count=1 ./...`
Expected: all `ok`.

- [ ] **Step 9: Commit**

```bash
git add internal
git commit -m "describe available updates: versions, notes, breaking"
```

---

### Task 7: Policy

**Files:**
- Create: `internal/policy/policy.go`
- Test: `internal/policy/policy_test.go`

**Interfaces:**
- Consumes: `store.Settings`, `store.Info`, `semver`.
- Produces: `policy.Action` (`Notify`, `Update`, `Skip`), `policy.Decide(s store.Settings, info store.Info, image string) (Action, string)`, `policy.Protected(image string) bool`.

- [ ] **Step 1: Write the failing tests**

`internal/policy/policy_test.go`:

```go
package policy

import (
	"strings"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

func TestDecide(t *testing.T) {
	info := func(oldV, newV string, breaking bool, reasons ...string) store.Info {
		return store.Info{OldVersion: oldV, NewVersion: newV, Breaking: breaking, Reasons: reasons}
	}
	cases := []struct {
		name   string
		policy string
		image  string
		info   store.Info
		want   Action
		why    string
	}{
		{"never", store.PolicyNever, "app:1", info("1.0.0", "1.0.1", false), Skip, "never"},
		{"notify", store.PolicyNotify, "app:1", info("1.0.0", "1.0.1", false), Notify, "notify"},
		{"empty policy behaves as notify", "", "app:1", info("1.0.0", "1.0.1", false), Notify, "notify"},
		{"auto patch", store.PolicyAuto, "app:1", info("1.0.0", "1.0.1", false), Update, ""},
		{"auto minor", store.PolicyAuto, "app:1", info("1.0.0", "1.1.0", false), Update, ""},
		{"auto same version, new digest", store.PolicyAuto, "app:1", info("1.0.0", "1.0.0", false), Update, ""},
		{"auto major", store.PolicyAuto, "app:1", info("1.0.0", "2.0.0", false), Notify, "major"},
		{"auto breaking", store.PolicyAuto, "app:1", info("1.0.0", "1.1.0", true, "Release v1.1.0 mentions \"breaking\"."), Notify, "breaking"},
		{"auto unknown version", store.PolicyAuto, "app:latest", info("", "", false), Notify, "unknown"},
		{"auto protected", store.PolicyAuto, "postgres:16", info("16.1.0", "16.2.0", false), Notify, "protected"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why := Decide(store.Settings{Container: "app", Policy: c.policy}, c.info, c.image)
			if got != c.want || !strings.Contains(why, c.why) {
				t.Fatalf("got %v %q, want %v containing %q", got, why, c.want, c.why)
			}
		})
	}
}

func TestProtected(t *testing.T) {
	for _, img := range []string{"postgres:16", "docker.io/library/mariadb", "ghcr.io/tecnativa/docker-socket-proxy:latest", "redis", "mongo:7"} {
		if !Protected(img) {
			t.Errorf("%s should be protected", img)
		}
	}
	for _, img := range []string{"linuxserver/sonarr", "ghcr.io/immich-app/immich-server", "nginx", "not a ref!"} {
		if Protected(img) {
			t.Errorf("%s should not be protected", img)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/policy/`
Expected: FAIL, `undefined: Decide`.

- [ ] **Step 3: Implement**

`internal/policy/policy.go`:

```go
// Package policy decides what to do with an available update.
package policy

import (
	"path"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/jordibrouwer/nextupdate/internal/semver"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

type Action int

const (
	Notify Action = iota // tell the user, do not update
	Update               // update now
	Skip                 // say nothing
)

func (a Action) String() string { return [...]string{"notify", "update", "skip"}[a] }

// protected are images whose update can break other containers or lose
// data; they are never updated automatically.
var protected = map[string]bool{
	"postgres": true, "mysql": true, "mariadb": true, "mongo": true, "redis": true, "valkey": true,
	"couchdb": true, "influxdb": true, "timescaledb": true, "clickhouse": true, "docker-socket-proxy": true,
}

func Protected(image string) bool {
	ref, err := name.ParseReference(image)
	if err != nil {
		return false
	}
	return protected[path.Base(ref.Context().RepositoryStr())]
}

// Decide returns the action for one available update and a plain-English
// reason (empty when the update simply goes ahead).
func Decide(s store.Settings, info store.Info, image string) (Action, string) {
	switch s.Policy {
	case store.PolicyNever:
		return Skip, "The policy for this container is never."
	case store.PolicyAuto:
	default:
		return Notify, "The policy for this container is notify."
	}
	if Protected(image) {
		return Notify, "This is a protected image (database or socket proxy); update it by hand."
	}
	if info.Breaking {
		return Notify, "Breaking update: " + strings.Join(info.Reasons, " ")
	}
	o, okO := semver.Parse(info.OldVersion)
	n, okN := semver.Parse(info.NewVersion)
	if !okO || !okN {
		return Notify, "The version is unknown, so the update is not applied automatically."
	}
	switch semver.Diff(o, n) {
	case semver.None, semver.Patch, semver.Minor:
		return Update, ""
	}
	return Notify, "This is a major version change or a downgrade, so the update is not applied automatically."
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `gofmt -l internal/policy; go vet ./internal/policy/ && go test ./internal/policy/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/policy
git commit -m "update policy: notify, auto, never"
```

---

### Task 8: Old-image cleanup

**Files:**
- Modify: `internal/store/schema.sql`, `internal/docker/types.go`, `internal/docker/client.go`, `internal/docker/client_test.go`, `internal/dockertest/fake.go`, `internal/engine/engine.go`, `internal/engine/engine_test.go`
- Create: (extend) `internal/store/info.go`, `internal/store/info_test.go`

**Interfaces:**
- Produces:
  - `docker.ErrConflict` (wrapped on HTTP 409) and `docker.API.RemoveImage(ctx context.Context, id string) error`
  - `store.OldImage{ImageID, Container string; RemoveAfter time.Time}`, `(*Store).AddOldImage(o OldImage) error`, `(*Store).DueOldImages(now time.Time) ([]OldImage, error)`, `(*Store).DeleteOldImage(imageID string) error`
  - `engine.Engine.Retention time.Duration` (0 keeps nothing tracked, so nothing is removed later) and `(*Engine).Cleanup(ctx context.Context) error`

- [ ] **Step 1: Write the failing Docker client test**

Append to `internal/docker/client_test.go`:

```go
func TestRemoveImageConflict(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1.44/images/sha256:abc" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"message":"image is being used by running container"}`)
	})
	err := c.RemoveImage(context.Background(), "sha256:abc")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/docker/`
Expected: FAIL, `c.RemoveImage undefined`.

- [ ] **Step 3: Implement in the client, interface and fake**

`internal/docker/types.go`, add to the `API` interface:

```go
	RemoveImage(ctx context.Context, id string) error
```

`internal/docker/client.go`: add next to `ErrNotFound`

```go
var ErrConflict = errors.New("conflict")
```

add this case to the `switch` in `do`, right after the `StatusNotFound` case:

```go
	case resp.StatusCode == http.StatusConflict:
		var e struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("docker %s %s: %s: %w", method, path, e.Message, ErrConflict)
```

and add the method:

```go
func (c *Client) RemoveImage(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/images/"+id, nil, nil, nil)
}
```

(Without `force`, Docker refuses to remove an image that a container still uses, which is what we want.)

`internal/dockertest/fake.go`, add:

```go
// RemoveImage refuses while a container uses the image, like Docker.
func (f *Fake) RemoveImage(ctx context.Context, id string) error {
	f.record("rmi %s", id)
	found := false
	for _, img := range f.Images {
		if img.ID == id {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("image %s: %w", id, docker.ErrNotFound)
	}
	for _, c := range f.Containers {
		if c.Image == id {
			return fmt.Errorf("image %s in use: %w", id, docker.ErrConflict)
		}
	}
	for k, img := range f.Images {
		if img.ID == id {
			delete(f.Images, k)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go vet ./... && go test -count=1 ./internal/docker/ ./internal/dockertest/ ./internal/updater/ ./internal/discovery/`
Expected: `ok`

- [ ] **Step 5: Write the failing store and engine tests**

Append to `internal/store/info_test.go` (add imports `"time"`):

```go
func TestOldImages(t *testing.T) {
	s := openTest(t)
	base := time.UnixMilli(1_700_000_000_000)
	for _, o := range []OldImage{
		{ImageID: "sha256:a", Container: "app", RemoveAfter: base.Add(time.Hour)},
		{ImageID: "sha256:b", Container: "app", RemoveAfter: base.Add(48 * time.Hour)},
	} {
		if err := s.AddOldImage(o); err != nil {
			t.Fatal(err)
		}
	}
	due, err := s.DueOldImages(base.Add(2 * time.Hour))
	if err != nil || len(due) != 1 || due[0].ImageID != "sha256:a" {
		t.Fatalf("due %+v %v", due, err)
	}
	if err := s.DeleteOldImage("sha256:a"); err != nil {
		t.Fatal(err)
	}
	if due, _ = s.DueOldImages(base.Add(2 * time.Hour)); len(due) != 0 {
		t.Fatalf("still due: %+v", due)
	}
}
```

Append to `internal/engine/engine_test.go`:

```go
func TestUpdateTracksOldImageAndCleanupRemovesIt(t *testing.T) {
	e, _, _ := newEngine(t)
	f := e.API.(*dockertest.Fake)
	now := time.UnixMilli(1_700_000_000_000)
	e.Now = func() time.Time { return now }
	e.Retention = 24 * time.Hour
	ctx := context.Background()

	f.AddImage("sha256:a", docker.ImageJSON{ID: "sha256:a"}) // the image the update left behind
	if _, err := e.Update(ctx, "app"); err != nil {         // fakeAdapter: ok, sha256:a → sha256:b
		t.Fatal(err)
	}
	if err := e.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Images["sha256:a"]; !ok {
		t.Fatal("old image removed before the retention period ended")
	}
	now = now.Add(25 * time.Hour)
	if err := e.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Images["sha256:a"]; ok {
		t.Fatal("old image not removed after the retention period")
	}
	if due, _ := e.Store.DueOldImages(now); len(due) != 0 {
		t.Fatalf("row not deleted: %+v", due)
	}
}

func TestCleanupKeepsImageStillInUse(t *testing.T) {
	e, _, _ := newEngine(t)
	f := e.API.(*dockertest.Fake)
	now := time.UnixMilli(1_700_000_000_000)
	e.Now = func() time.Time { return now }
	if err := e.Store.AddOldImage(store.OldImage{ImageID: "sha256:a", Container: "app", RemoveAfter: now.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	// container "app" in the fixture runs sha256:a
	if err := e.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Images["sha256:a"]; !ok {
		t.Fatal("image in use was removed")
	}
	if due, _ := e.Store.DueOldImages(now); len(due) != 1 {
		t.Fatalf("row must stay for the next cycle: %+v", due)
	}
}
```

- [ ] **Step 6: Run to verify they fail**

Run: `go test ./internal/store/ ./internal/engine/`
Expected: FAIL, `undefined: OldImage`, `unknown field Retention`.

- [ ] **Step 7: Implement**

Append to `internal/store/schema.sql`:

```sql

CREATE TABLE IF NOT EXISTS old_images (
  image_id     TEXT PRIMARY KEY,
  container    TEXT    NOT NULL,
  remove_after INTEGER NOT NULL
);
```

Append to `internal/store/info.go`:

```go
// OldImage is an image a successful update left behind. It is kept until
// RemoveAfter, so a manual rollback stays possible for a while.
type OldImage struct {
	ImageID     string
	Container   string
	RemoveAfter time.Time
}

func (s *Store) AddOldImage(o OldImage) error {
	_, err := s.db.Exec(`INSERT INTO old_images (image_id, container, remove_after) VALUES (?, ?, ?)
		ON CONFLICT(image_id) DO UPDATE SET container = excluded.container, remove_after = excluded.remove_after`,
		o.ImageID, o.Container, o.RemoveAfter.UnixMilli())
	return err
}

func (s *Store) DueOldImages(now time.Time) ([]OldImage, error) {
	rows, err := s.db.Query(`SELECT image_id, container, remove_after FROM old_images WHERE remove_after <= ? ORDER BY remove_after`, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OldImage
	for rows.Next() {
		var o OldImage
		var after int64
		if err := rows.Scan(&o.ImageID, &o.Container, &after); err != nil {
			return nil, err
		}
		o.RemoveAfter = time.UnixMilli(after)
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) DeleteOldImage(imageID string) error {
	_, err := s.db.Exec(`DELETE FROM old_images WHERE image_id = ?`, imageID)
	return err
}
```

In `internal/engine/engine.go` add a field to `Engine`:

```go
	Retention time.Duration // how long an update keeps the previous image; 0 = do not track
```

In `Update`, replace the `if res.Outcome == updater.OutcomeOK { ... }` block with:

```go
	if res.Outcome == updater.OutcomeOK {
		if err := e.Store.RemoveAvailable(name); err != nil {
			return h, err
		}
		if e.Retention > 0 && res.FromImage != "" && res.FromImage != res.ToImage {
			if err := e.Store.AddOldImage(store.OldImage{ImageID: res.FromImage, Container: name, RemoveAfter: e.now().Add(e.Retention)}); err != nil {
				return h, err
			}
		}
	}
```

Add the method:

```go
// Cleanup removes old images whose retention period has passed. An image
// that is still in use is kept and tried again on the next cycle.
func (e *Engine) Cleanup(ctx context.Context) error {
	due, err := e.Store.DueOldImages(e.now())
	if err != nil {
		return err
	}
	for _, o := range due {
		err := e.API.RemoveImage(ctx, o.ImageID)
		switch {
		case err == nil, errors.Is(err, docker.ErrNotFound):
			if err := e.Store.DeleteOldImage(o.ImageID); err != nil {
				return err
			}
			e.logf("cleanup: removed old image %s of %s", o.ImageID, o.Container)
		case errors.Is(err, docker.ErrConflict):
			e.logf("cleanup: old image %s of %s is still in use, keeping it", o.ImageID, o.Container)
		default:
			e.logf("cleanup: remove %s: %v", o.ImageID, err)
		}
	}
	return nil
}
```

- [ ] **Step 8: Run everything**

Run: `gofmt -l . ; go vet ./... && go vet -tags integration ./test/... && go test -race -count=1 ./...`
Expected: all `ok`.

- [ ] **Step 9: Commit**

```bash
git add internal
git commit -m "remove old images after a retention period"
```

---

### Task 9: Scheduler, `serve` and CLI

**Files:**
- Modify: `internal/store/schema.sql`, `internal/store/info.go`, `internal/store/info_test.go`, `internal/engine/engine.go`, `internal/engine/engine_test.go`, `cmd/nextupdate/main.go`, `Dockerfile`
- Create: `internal/scheduler/scheduler.go`
- Test: `internal/scheduler/scheduler_test.go`

**Interfaces:**
- Consumes: everything above.
- Produces:
  - `(*Store).Seen(container, kind string) (digest string, err error)` and `(*Store).MarkSeen(container, kind, digest string) error`; kinds are `"available"` and `"attempted"`
  - `scheduler.Engine` interface: `Check(ctx) ([]store.Available, error)`, `Update(ctx, name string) (store.History, error)`, `Cleanup(ctx) error`
  - `scheduler.Event{Kind, Container, Image, OldVersion, NewVersion string; Breaking bool; Reasons []string; Detail string}` with kinds `scheduler.KindAvailable`, `KindUpdated`, `KindRolledBack`, `KindFailed`
  - `scheduler.Notifier` interface `Notify(ctx context.Context, e Event)`, and `scheduler.LogNotifier{Log *log.Logger}`
  - `scheduler.Scheduler{Engine Engine; Store *store.Store; Notifier Notifier; Interval time.Duration; Log *log.Logger}` with `RunOnce(ctx) error` and `Run(ctx) error`
  - `(*Engine).Update` becomes safe to call concurrently: one update at a time
  - CLI: `serve`, `policy <container> <notify|auto|never>`, `check` prints versions and the breaking mark

- [ ] **Step 1: Write the failing store test for seen markers**

Append to `internal/store/info_test.go`:

```go
func TestSeen(t *testing.T) {
	s := openTest(t)
	if d, err := s.Seen("app", "available"); err != nil || d != "" {
		t.Fatalf("empty: %q %v", d, err)
	}
	if err := s.MarkSeen("app", "available", "sha256:1"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSeen("app", "attempted", "sha256:2"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSeen("app", "available", "sha256:3"); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Seen("app", "available"); d != "sha256:3" {
		t.Fatalf("available: %q", d)
	}
	if d, _ := s.Seen("app", "attempted"); d != "sha256:2" {
		t.Fatalf("kinds must not mix: %q", d)
	}
}
```

- [ ] **Step 2: Implement the store side**

Append to `internal/store/schema.sql`:

```sql

CREATE TABLE IF NOT EXISTS seen (
  container TEXT NOT NULL,
  kind      TEXT NOT NULL,
  digest    TEXT NOT NULL,
  PRIMARY KEY (container, kind)
);
```

Append to `internal/store/info.go`:

```go
// Seen returns the digest last announced ("available") or last attempted
// ("attempted") for a container, or "" when there is none.
func (s *Store) Seen(container, kind string) (string, error) {
	var digest string
	err := s.db.QueryRow(`SELECT digest FROM seen WHERE container = ? AND kind = ?`, container, kind).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return digest, err
}

func (s *Store) MarkSeen(container, kind, digest string) error {
	_, err := s.db.Exec(`INSERT INTO seen (container, kind, digest) VALUES (?, ?, ?)
		ON CONFLICT(container, kind) DO UPDATE SET digest = excluded.digest`, container, kind, digest)
	return err
}
```

Add `"errors"` to the imports of `internal/store/info.go`. Run `go test -count=1 ./internal/store/`, expect `ok`.

- [ ] **Step 3: Write the failing engine concurrency test**

Append to `internal/engine/engine_test.go` (add imports `"sync"` and `"sync/atomic"`):

```go
type slowAdapter struct {
	active, peak atomic.Int32
}

func (a *slowAdapter) Update(ctx context.Context, c discovery.Container) updater.Result {
	n := a.active.Add(1)
	for {
		p := a.peak.Load()
		if n <= p || a.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	a.active.Add(-1)
	return updater.Result{Outcome: updater.OutcomeOK}
}

func TestUpdateRunsOneAtATime(t *testing.T) {
	e, _, _ := newEngine(t)
	slow := &slowAdapter{}
	e.Run = slow
	var wg sync.WaitGroup
	for _, name := range []string{"app", "same", "local"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.Update(context.Background(), name); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if slow.peak.Load() != 1 {
		t.Fatalf("updates overlapped: peak %d", slow.peak.Load())
	}
}
```

Run: `go test -count=1 ./internal/engine/ -run TestUpdateRunsOneAtATime`
Expected: FAIL, `updates overlapped: peak 3` (or 2).

- [ ] **Step 4: Add the update lock**

In `internal/engine/engine.go` add `"sync"` to the imports, a field `mu sync.Mutex` to `Engine`, and at the top of `Update`:

```go
	e.mu.Lock()
	defer e.mu.Unlock()
```

Run `go test -race -count=1 ./internal/engine/`, expect `ok`.

- [ ] **Step 5: Write the failing scheduler tests**

`internal/scheduler/scheduler_test.go`:

```go
package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
)

type fakeEngine struct {
	avail   []store.Available
	outcome string
	err     error
	updated []string
	cleaned int
}

func (f *fakeEngine) Check(ctx context.Context) ([]store.Available, error) { return f.avail, nil }
func (f *fakeEngine) Update(ctx context.Context, name string) (store.History, error) {
	f.updated = append(f.updated, name)
	return store.History{Container: name, Outcome: f.outcome, Reason: "why"}, f.err
}
func (f *fakeEngine) Cleanup(ctx context.Context) error { f.cleaned++; return nil }

type recorder struct{ events []Event }

func (r *recorder) Notify(ctx context.Context, e Event) { r.events = append(r.events, e) }

func setup(t *testing.T, policy string, info store.Info) (*Scheduler, *fakeEngine, *recorder) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	info.Container = "app"
	if err := st.ReplaceInfo([]store.Info{info}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSettings(store.Settings{Container: "app", Policy: policy}); err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{outcome: updater.OutcomeOK, avail: []store.Available{{Container: "app", Image: "app:latest", RemoteDigest: "sha256:new"}}}
	rec := &recorder{}
	return &Scheduler{Engine: eng, Store: st, Notifier: rec, Interval: time.Hour}, eng, rec
}

func TestNotifyPolicyAnnouncesOnce(t *testing.T) {
	s, eng, rec := setup(t, store.PolicyNotify, store.Info{OldVersion: "1.0.0", NewVersion: "1.0.1"})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := s.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(eng.updated) != 0 {
		t.Fatalf("notify must not update: %v", eng.updated)
	}
	if len(rec.events) != 1 || rec.events[0].Kind != KindAvailable || rec.events[0].NewVersion != "1.0.1" {
		t.Fatalf("want one announcement, got %+v", rec.events)
	}
	if eng.cleaned != 2 {
		t.Fatalf("cleanup should run every cycle, ran %d", eng.cleaned)
	}
	// A newer digest is announced again.
	eng.avail[0].RemoteDigest = "sha256:newer"
	s.RunOnce(ctx)
	if len(rec.events) != 2 {
		t.Fatalf("new digest must be announced: %+v", rec.events)
	}
}

func TestAutoPolicyUpdates(t *testing.T) {
	s, eng, rec := setup(t, store.PolicyAuto, store.Info{OldVersion: "1.0.0", NewVersion: "1.1.0"})
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(eng.updated) != 1 || len(rec.events) != 1 || rec.events[0].Kind != KindUpdated {
		t.Fatalf("updated %v events %+v", eng.updated, rec.events)
	}
}

func TestAutoPolicyLeavesBreakingUpdatesToTheUser(t *testing.T) {
	s, eng, rec := setup(t, store.PolicyAuto, store.Info{OldVersion: "1.0.0", NewVersion: "2.0.0", Breaking: true, Reasons: []string{"Major version change from 1.0.0 to 2.0.0."}})
	s.RunOnce(context.Background())
	if len(eng.updated) != 0 || len(rec.events) != 1 || rec.events[0].Kind != KindAvailable || !rec.events[0].Breaking {
		t.Fatalf("updated %v events %+v", eng.updated, rec.events)
	}
}

func TestRolledBackUpdateIsNotRetriedForTheSameDigest(t *testing.T) {
	s, eng, rec := setup(t, store.PolicyAuto, store.Info{OldVersion: "1.0.0", NewVersion: "1.1.0"})
	eng.outcome = updater.OutcomeRolledBack
	ctx := context.Background()
	s.RunOnce(ctx)
	s.RunOnce(ctx)
	if len(eng.updated) != 1 {
		t.Fatalf("a rolled-back digest must not be retried: %v", eng.updated)
	}
	if len(rec.events) != 1 || rec.events[0].Kind != KindRolledBack {
		t.Fatalf("events %+v", rec.events)
	}
	eng.avail[0].RemoteDigest = "sha256:fixed"
	s.RunOnce(ctx)
	if len(eng.updated) != 2 {
		t.Fatalf("a new digest must be tried: %v", eng.updated)
	}
}

func TestUpdateErrorIsReported(t *testing.T) {
	s, eng, rec := setup(t, store.PolicyAuto, store.Info{OldVersion: "1.0.0", NewVersion: "1.1.0"})
	eng.err = errors.New("docker unreachable")
	s.RunOnce(context.Background())
	if len(rec.events) != 1 || rec.events[0].Kind != KindFailed || rec.events[0].Detail != "docker unreachable" {
		t.Fatalf("events %+v", rec.events)
	}
}

func TestNeverPolicyIsSilent(t *testing.T) {
	s, eng, rec := setup(t, store.PolicyNever, store.Info{OldVersion: "1.0.0", NewVersion: "1.0.1"})
	s.RunOnce(context.Background())
	if len(eng.updated) != 0 || len(rec.events) != 0 {
		t.Fatalf("updated %v events %+v", eng.updated, rec.events)
	}
}

func TestRunStopsWhenContextEnds(t *testing.T) {
	s, eng, _ := setup(t, store.PolicyNotify, store.Info{})
	s.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if eng.cleaned < 2 {
		t.Fatalf("Run should cycle repeatedly, cycled %d times", eng.cleaned)
	}
}
```

- [ ] **Step 6: Run to verify it fails**

Run: `go test ./internal/scheduler/`
Expected: FAIL, `undefined: Scheduler`.

- [ ] **Step 7: Implement the scheduler**

`internal/scheduler/scheduler.go`:

```go
// Package scheduler runs the check, decide, update loop.
package scheduler

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/policy"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
)

const (
	KindAvailable  = "update_available"
	KindUpdated    = "update_ok"
	KindRolledBack = "update_rolled_back"
	KindFailed     = "update_failed"
)

// Event is what a notifier is told about.
type Event struct {
	Kind       string
	Container  string
	Image      string
	OldVersion string
	NewVersion string
	Breaking   bool
	Reasons    []string
	Detail     string
}

type Notifier interface {
	Notify(ctx context.Context, e Event)
}

// LogNotifier writes events to a log; real targets come later.
type LogNotifier struct{ Log *log.Logger }

func (n LogNotifier) Notify(ctx context.Context, e Event) {
	l := n.Log
	if l == nil {
		l = log.Default()
	}
	msg := e.Kind + ": " + e.Container + " (" + e.Image + ")"
	if e.OldVersion != "" || e.NewVersion != "" {
		msg += " " + e.OldVersion + " to " + e.NewVersion
	}
	if e.Breaking {
		msg += " [breaking: " + strings.Join(e.Reasons, " ") + "]"
	}
	if e.Detail != "" {
		msg += " - " + e.Detail
	}
	l.Print(msg)
}

type Engine interface {
	Check(ctx context.Context) ([]store.Available, error)
	Update(ctx context.Context, name string) (store.History, error)
	Cleanup(ctx context.Context) error
}

type Scheduler struct {
	Engine   Engine
	Store    *store.Store
	Notifier Notifier
	Interval time.Duration
	Log      *log.Logger
}

func (s *Scheduler) logf(format string, a ...any) {
	l := s.Log
	if l == nil {
		l = log.Default()
	}
	l.Printf(format, a...)
}

// RunOnce does one cycle: find updates, announce or apply each one
// according to its policy, and clean up old images.
func (s *Scheduler) RunOnce(ctx context.Context) error {
	avail, err := s.Engine.Check(ctx)
	if err != nil {
		return err
	}
	infos, err := s.Store.ListInfo()
	if err != nil {
		return err
	}
	byName := map[string]store.Info{}
	for _, i := range infos {
		byName[i.Container] = i
	}
	for _, a := range avail {
		if ctx.Err() != nil {
			return nil
		}
		info := byName[a.Container]
		settings, err := s.Store.GetSettings(a.Container)
		if err != nil {
			s.logf("scheduler: settings of %s: %v", a.Container, err)
			continue
		}
		action, why := policy.Decide(settings, info, a.Image)
		ev := Event{Container: a.Container, Image: a.Image, OldVersion: info.OldVersion, NewVersion: info.NewVersion, Breaking: info.Breaking, Reasons: info.Reasons}
		switch action {
		case policy.Notify:
			s.announce(ctx, a, ev, why)
		case policy.Update:
			s.apply(ctx, a, ev)
		}
	}
	if err := s.Engine.Cleanup(ctx); err != nil {
		s.logf("scheduler: cleanup: %v", err)
	}
	return nil
}

func (s *Scheduler) announce(ctx context.Context, a store.Available, ev Event, why string) {
	if seen, _ := s.Store.Seen(a.Container, "available"); seen == a.RemoteDigest {
		return
	}
	ev.Kind, ev.Detail = KindAvailable, why
	s.Notifier.Notify(ctx, ev)
	if err := s.Store.MarkSeen(a.Container, "available", a.RemoteDigest); err != nil {
		s.logf("scheduler: mark seen %s: %v", a.Container, err)
	}
}

func (s *Scheduler) apply(ctx context.Context, a store.Available, ev Event) {
	if seen, _ := s.Store.Seen(a.Container, "attempted"); seen == a.RemoteDigest {
		return // this digest already failed or was tried; wait for a new one
	}
	if err := s.Store.MarkSeen(a.Container, "attempted", a.RemoteDigest); err != nil {
		s.logf("scheduler: mark attempted %s: %v", a.Container, err)
	}
	h, err := s.Engine.Update(ctx, a.Container)
	switch {
	case err != nil:
		ev.Kind, ev.Detail = KindFailed, err.Error()
	case h.Outcome == updater.OutcomeOK:
		ev.Kind = KindUpdated
	case h.Outcome == updater.OutcomeRolledBack:
		ev.Kind, ev.Detail = KindRolledBack, h.Reason
	default:
		ev.Kind, ev.Detail = KindFailed, h.Reason
	}
	s.Notifier.Notify(ctx, ev)
}

// Run cycles until the context ends: once right away, then every Interval.
func (s *Scheduler) Run(ctx context.Context) error {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.RunOnce(ctx); err != nil {
			s.logf("scheduler: cycle failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
```

- [ ] **Step 8: Run to verify the scheduler passes**

Run: `gofmt -l internal; go vet ./... && go test -race -count=1 ./internal/scheduler/ ./internal/engine/ ./internal/store/`
Expected: `ok` for all three.

- [ ] **Step 9: Wire the CLI**

Replace `cmd/nextupdate/main.go` with:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/changelog"
	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/engine"
	"github.com/jordibrouwer/nextupdate/internal/registry"
	"github.com/jordibrouwer/nextupdate/internal/scheduler"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
	"github.com/jordibrouwer/nextupdate/internal/verify"
)

var _ changelog.Cache = (*store.ChangelogCache)(nil)

const usage = `usage: nextupdate <command>

commands:
  serve                       check on a schedule and apply the update policies
  check                       list containers with a newer image
  update <name>               update one container, roll back if it fails
  policy <name> <policy>      set notify, auto or never for a container
  reconcile                   repair updates interrupted by a crash

environment:
  DOCKER_HOST                 Docker endpoint (default unix:///var/run/docker.sock)
  NEXTUPDATE_DATA             data directory (default /data)
  NEXTUPDATE_INTERVAL         time between checks in serve mode (default 6h)
  NEXTUPDATE_VERIFY_WINDOW    how long a new container gets to become healthy (default 60s)
  NEXTUPDATE_KEEP_OLD_IMAGES  how long to keep the previous image (default 168h)
  NEXTUPDATE_GITHUB_TOKEN     optional token for a higher GitHub rate limit`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "nextupdate:", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key, def string) (time.Duration, error) {
	d, err := time.ParseDuration(envOr(key, def))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func run(ctx context.Context, cmd string, args []string) error {
	api, err := docker.New(os.Getenv("DOCKER_HOST"))
	if err != nil {
		return err
	}
	dataDir := envOr("NEXTUPDATE_DATA", "/data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dataDir, "nextupdate.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	window, err := envDuration("NEXTUPDATE_VERIFY_WINDOW", "60s")
	if err != nil {
		return err
	}
	keep, err := envDuration("NEXTUPDATE_KEEP_OLD_IMAGES", "168h")
	if err != nil {
		return err
	}
	interval, err := envDuration("NEXTUPDATE_INTERVAL", "6h")
	if err != nil {
		return err
	}
	verifier := engine.NewVerifier(api, st, verify.Check{Window: window, Interval: time.Second, MaxRestarts: 3})
	journal := st.Journal()
	runner := updater.ExecRunner{}
	self, _ := os.Hostname()
	eng := &engine.Engine{
		API: api, Registry: registry.NewRemote(), Store: st, Self: self, Retention: keep,
		Run:       &updater.Run{API: api, Journal: journal, Verify: verifier},
		Compose:   &updater.Compose{API: api, Runner: runner, Journal: journal, Verify: verifier},
		Changelog: &changelog.GitHub{Token: os.Getenv("NEXTUPDATE_GITHUB_TOKEN"), Cache: st.ChangelogCache()},
		Mappings:  changelog.DefaultMappings(),
	}

	switch cmd {
	case "serve":
		if err := reconcile(ctx, api, journal, runner); err != nil {
			return err
		}
		fmt.Printf("serving: checking every %s\n", interval)
		s := &scheduler.Scheduler{Engine: eng, Store: st, Notifier: scheduler.LogNotifier{}, Interval: interval}
		return s.Run(ctx)
	case "check":
		list, err := eng.Check(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("everything is up to date")
			return nil
		}
		infos, err := st.ListInfo()
		if err != nil {
			return err
		}
		byName := map[string]store.Info{}
		for _, i := range infos {
			byName[i.Container] = i
		}
		for _, a := range list {
			i := byName[a.Container]
			line := fmt.Sprintf("%-30s %s", a.Container, a.Image)
			if i.OldVersion != "" || i.NewVersion != "" {
				line += fmt.Sprintf("  %s to %s", orUnknown(i.OldVersion), orUnknown(i.NewVersion))
			}
			if i.Breaking {
				line += "  BREAKING"
			}
			fmt.Println(line)
			for _, r := range i.Reasons {
				fmt.Println("    " + r)
			}
		}
		return nil
	case "update":
		if len(args) != 1 {
			return errors.New("update needs exactly one container name")
		}
		if err := reconcile(ctx, api, journal, runner); err != nil {
			return err
		}
		h, err := eng.Update(ctx, args[0])
		if err != nil {
			return err
		}
		for _, l := range h.Log {
			fmt.Println("  " + l)
		}
		fmt.Printf("%s: %s %s\n", h.Container, h.Outcome, h.Reason)
		if h.Outcome != updater.OutcomeOK {
			return fmt.Errorf("update of %s ended as %s", h.Container, h.Outcome)
		}
		return nil
	case "policy":
		if len(args) != 2 {
			return errors.New("policy needs a container name and one of notify, auto, never")
		}
		s, err := st.GetSettings(args[0])
		if err != nil {
			return err
		}
		s.Policy = strings.ToLower(args[1])
		if err := st.SetSettings(s); err != nil {
			return err
		}
		fmt.Printf("%s: policy is now %s\n", args[0], s.Policy)
		return nil
	case "reconcile":
		return reconcile(ctx, api, journal, runner)
	default:
		return fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
}

func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

func reconcile(ctx context.Context, api docker.API, j *store.Journal, runner updater.Runner) error {
	actions, err := updater.Reconcile(ctx, api, j, runner)
	for _, a := range actions {
		fmt.Println("reconcile:", a)
	}
	return err
}
```

In `Dockerfile` change the last line to `CMD ["serve"]`.

- [ ] **Step 10: Build and smoke-test against local Docker**

Run: `gofmt -l . ; go vet ./... && go build -o /tmp/nextupdate ./cmd/nextupdate`
Expected: no output.

Run: `d=$(mktemp -d); NEXTUPDATE_DATA=$d /tmp/nextupdate check`
Expected: a list with versions (and `BREAKING` marks with reasons), or `everything is up to date`; no error lines.

Run: `NEXTUPDATE_DATA=$d /tmp/nextupdate policy some-container auto && NEXTUPDATE_DATA=$d /tmp/nextupdate policy some-container yolo`
Expected: first prints `some-container: policy is now auto`; second exits 1 with `invalid policy "yolo"`.

Run in the background and read the log after ~10 s: `NEXTUPDATE_DATA=$d NEXTUPDATE_INTERVAL=5s /tmp/nextupdate serve > /tmp/nu-serve.log 2>&1 &` then `kill %1` (or the PID) and `cat /tmp/nu-serve.log`.
Expected: `serving: checking every 5s`; every available update announced exactly once as `update_available: ...`, not once per cycle.

- [ ] **Step 11: Run the whole suite, including integration**

Run: `go test -race -count=1 ./...`
Expected: all `ok`.

Run in the background: `go test -tags integration ./test/integration/ -count=1 > /tmp/nu-it.log 2>&1; echo exit=$? >> /tmp/nu-it.log`, then read the log when done.
Expected: `ok` and `exit=0` (the Plan 1 update and rollback tests still pass with the new `Verifier` signature). Clean up afterwards: `docker image rm nu-it:v1 nu-it:v2 nu-it:bad nu-it:runcur nu-it:composecur 2>/dev/null; true`.

- [ ] **Step 12: Commit**

```bash
git add cmd internal Dockerfile
git commit -m "scheduler, serve command and policy command"
```

---

## Spec coverage (Plan 2)

| Spec item | Task |
|---|---|
| Changelog per image from GitHub releases, ETag cache, range between versions | 4, 6 |
| Repo lookup: OCI label, bundled mapping, manual entry | 4 |
| Breaking: semver major, keywords, community flag | 3, 5 |
| Policy `notify` / `auto` (patch and minor, never breaking) / `never` | 7 |
| Protected images (databases, socket proxy) default to notify | 7 |
| Per-container check config (HTTP URL, verify window) | 1 |
| Scheduler, one update at a time, no retry of a rolled-back digest | 9 |
| Announce an update once, not every cycle | 9 |
| Old images kept for N days, then removed | 8 |
| Stale-data rule (no update offered when the registry or GitHub is down) | Already true: a failed digest check skips the container (Plan 1); a failed GitHub call only leaves the notes empty (Task 6) |
| Registry 429 backoff | go-containerregistry retries 429 and 5xx by default; no extra code |
| Notifier targets: Web Push, ntfy, Gotify, Discord, Telegram, e-mail | Plan 3 (`scheduler.Notifier` is the seam) |
| Self-update through a helper container | Later (the engine still refuses to update itself) |
| UI, auth, PWA, widget, registry credentials in settings | Plan 3 |

**Known limits, accepted for now:** tags that are not plain versions (for example `1.2.3-ls123`) parse as pre-releases and are skipped when selecting release notes; `auto` never updates an image without a version label (notify instead); a private repository without credentials looks the same as a local build and is skipped silently.
