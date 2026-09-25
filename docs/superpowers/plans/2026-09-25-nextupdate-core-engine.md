# nextupdate Core Engine Implementation Plan (Plan 1 of 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A headless Go binary that finds Docker containers with a newer image, updates them (compose and plain `docker run` containers), verifies the new container and rolls back automatically on failure, driven from a CLI (`check`, `update <name>`, `reconcile`).

**Architecture:** A small hand-written Docker Engine API client (HTTP over the unix socket, API v1.44) sits behind a `docker.API` interface so every other package tests against an in-memory fake. `discovery` classifies containers, `registry` compares digests without pulling (go-containerregistry), `verify` judges a new container, `updater` holds two adapters (`Run`, `Compose`) that journal every step to SQLite so `Reconcile` can repair a crash mid-update. `engine` ties it together; `cmd/nextupdate` is the CLI.

**Tech Stack:** Go 1.26, `modernc.org/sqlite` (pure Go, no CGO), `github.com/google/go-containerregistry`, Docker Engine API v1.44, Docker Compose v2 CLI, Alpine runtime image.

**Spec:** `docs/superpowers/specs/2026-09-25-nextupdate-design.md`

**Follow-up plans (not in this plan):** Plan 2: changelog, classifier, policy and scheduler, per-container verify config, old-image cleanup. Plan 3: web UI, auth, PWA/push, notifiers, nextdash widget.

## Global Constraints

- Module path: `github.com/jordibrouwer/nextupdate`. Go 1.26. `CGO_ENABLED=0` must build.
- Docker Engine API version prefix: `/v1.44`.
- The compose file is never edited by nextupdate.
- Renamed old containers use the suffix `-nu-old` (`discovery.OldSuffix`).
- Compose project directories must be mounted inside the nextupdate container at the **same absolute path** as on the host, because compose labels carry host paths. (Refines the spec's `/stacks` mount; README must say so.)
- Rollback and cleanup steps run with `context.WithoutCancel(ctx)` so a SIGTERM never leaves a half-done update.
- Tests: unit tests with `go test ./...`; integration tests only with `-tags integration` and a running Docker. Keep Bash calls under 30 s; run integration tests in the background.
- Port 8099 for anything that listens during development; never 8080.
- Commits: only when Jordi asked in that turn; short, plain subject line; no `Co-Authored-By` trailer.

## File Structure

```
go.mod
cmd/nextupdate/main.go             CLI wiring: env config, subcommands
internal/store/schema.sql          SQLite schema
internal/store/store.go            Open, history, available
internal/store/journal.go          journal of in-flight update steps
internal/store/store_test.go
internal/docker/types.go           API interface + JSON types
internal/docker/client.go          HTTP client over unix socket / tcp
internal/docker/ref.go             SplitRef
internal/docker/client_test.go
internal/dockertest/fake.go        in-memory docker.API for tests
internal/discovery/discovery.go    container list → Container with source
internal/discovery/discovery_test.go
internal/registry/registry.go      RemoteDigest, LocalDigest
internal/registry/registry_test.go
internal/verify/verify.go          health / crashloop / HTTP verification
internal/verify/verify_test.go
internal/updater/updater.go        Result, Adapter, Journal, Verifier, Runner
internal/updater/recreate.go       BuildSpec: inspect → create spec
internal/updater/recreate_test.go
internal/updater/run.go            Run adapter
internal/updater/run_test.go
internal/updater/compose.go        Compose adapter + ExecRunner
internal/updater/compose_test.go
internal/updater/reconcile.go      crash recovery
internal/updater/reconcile_test.go
internal/engine/engine.go          Check, Update
internal/engine/engine_test.go
test/integration/update_test.go    real Docker, build tag integration
Dockerfile
.dockerignore
```

---

### Task 1: Module scaffold and SQLite store

**Files:**
- Create: `go.mod`, `internal/store/schema.sql`, `internal/store/store.go`, `internal/store/journal.go`
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces:
  - `store.Open(path string) (*store.Store, error)`, `(*Store).Close() error`
  - `store.History{ID int64; Container, Image, FromImage, ToImage string; StartedAt, FinishedAt time.Time; Outcome, Reason string; Log []string}`
  - `(*Store).AddHistory(h History) (int64, error)`, `(*Store).ListHistory(limit int) ([]History, error)` (newest first)
  - `store.Available{Container, Image, LocalDigest, RemoteDigest string; DetectedAt time.Time}`
  - `(*Store).ReplaceAvailable(list []Available) error`, `(*Store).ListAvailable() ([]Available, error)` (ordered by container), `(*Store).RemoveAvailable(container string) error`
  - `(*Store).Journal() *store.Journal`
  - `store.JournalEntry{ID int64; Container, Adapter, Step string; Data map[string]string}`
  - `(*Journal).Begin(container, adapter string, data map[string]string) (int64, error)` (step `"started"`)
  - `(*Journal).Step(id int64, step string, data map[string]string) error` (merges data into existing data)
  - `(*Journal).Close(id int64) error`, `(*Journal).Open() ([]JournalEntry, error)`

- [ ] **Step 1: Initialise the module**

```bash
cd /Users/jordibrw/dev/nextupdate
go mod init github.com/jordibrouwer/nextupdate
go get modernc.org/sqlite
```

- [ ] **Step 2: Write the failing tests**

`internal/store/store_test.go`:

```go
package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestHistoryRoundTrip(t *testing.T) {
	s := openTest(t)
	start := time.UnixMilli(1_700_000_000_000)
	for i, outcome := range []string{"ok", "rolled_back"} {
		_, err := s.AddHistory(History{
			Container: "app", Image: "app:latest", FromImage: "sha256:a", ToImage: "sha256:b",
			StartedAt: start.Add(time.Duration(i) * time.Minute), FinishedAt: start.Add(time.Duration(i)*time.Minute + time.Second),
			Outcome: outcome, Reason: "r", Log: []string{"pull app:latest", "stop app"},
		})
		if err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	got, err := s.ListHistory(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].Outcome != "rolled_back" {
		t.Fatalf("want newest first, got %+v", got)
	}
	if len(got[1].Log) != 2 || got[1].Log[1] != "stop app" || !got[1].StartedAt.Equal(start) {
		t.Fatalf("fields not preserved: %+v", got[1])
	}
}

func TestAvailableReplaceAndRemove(t *testing.T) {
	s := openTest(t)
	now := time.UnixMilli(1_700_000_000_000)
	if err := s.ReplaceAvailable([]Available{
		{Container: "b", Image: "b:1", LocalDigest: "sha256:1", RemoteDigest: "sha256:2", DetectedAt: now},
		{Container: "a", Image: "a:1", LocalDigest: "sha256:3", RemoteDigest: "sha256:4", DetectedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceAvailable([]Available{
		{Container: "a", Image: "a:1", LocalDigest: "sha256:3", RemoteDigest: "sha256:5", DetectedAt: now},
		{Container: "c", Image: "c:1", LocalDigest: "sha256:6", RemoteDigest: "sha256:7", DetectedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAvailable("c"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListAvailable()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Container != "a" || got[0].RemoteDigest != "sha256:5" {
		t.Fatalf("got %+v", got)
	}
}

func TestJournalMergesAndCloses(t *testing.T) {
	j := openTest(t).Journal()
	id, err := j.Begin("app", "run", map[string]string{"old_id": "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Step(id, "created_new", map[string]string{"new_id": "c2"}); err != nil {
		t.Fatal(err)
	}
	open, err := j.Open()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Step != "created_new" || open[0].Data["old_id"] != "c1" || open[0].Data["new_id"] != "c2" || open[0].Adapter != "run" {
		t.Fatalf("got %+v", open)
	}
	if err := j.Close(id); err != nil {
		t.Fatal(err)
	}
	if open, _ = j.Open(); len(open) != 0 {
		t.Fatalf("closed entry still open: %+v", open)
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/store/`
Expected: FAIL, build errors such as `undefined: Open`.

- [ ] **Step 4: Write the implementation**

`internal/store/schema.sql`:

```sql
CREATE TABLE IF NOT EXISTS history (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  container   TEXT    NOT NULL,
  image       TEXT    NOT NULL,
  from_image  TEXT    NOT NULL,
  to_image    TEXT    NOT NULL,
  started_at  INTEGER NOT NULL,
  finished_at INTEGER NOT NULL,
  outcome     TEXT    NOT NULL,
  reason      TEXT    NOT NULL DEFAULT '',
  log         TEXT    NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS journal (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  container TEXT    NOT NULL,
  adapter   TEXT    NOT NULL,
  step      TEXT    NOT NULL,
  data      TEXT    NOT NULL DEFAULT '{}',
  closed    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS available (
  container     TEXT PRIMARY KEY,
  image         TEXT    NOT NULL,
  local_digest  TEXT    NOT NULL,
  remote_digest TEXT    NOT NULL,
  detected_at   INTEGER NOT NULL
);
```

`internal/store/store.go`:

```go
// Package store keeps nextupdate's state in SQLite.
package store

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type History struct {
	ID         int64
	Container  string
	Image      string
	FromImage  string
	ToImage    string
	StartedAt  time.Time
	FinishedAt time.Time
	Outcome    string
	Reason     string
	Log        []string
}

func (s *Store) AddHistory(h History) (int64, error) {
	logJSON, err := json.Marshal(h.Log)
	if err != nil {
		return 0, err
	}
	r, err := s.db.Exec(`INSERT INTO history (container, image, from_image, to_image, started_at, finished_at, outcome, reason, log)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.Container, h.Image, h.FromImage, h.ToImage, h.StartedAt.UnixMilli(), h.FinishedAt.UnixMilli(), h.Outcome, h.Reason, string(logJSON))
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) ListHistory(limit int) ([]History, error) {
	rows, err := s.db.Query(`SELECT id, container, image, from_image, to_image, started_at, finished_at, outcome, reason, log
		FROM history ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []History
	for rows.Next() {
		var h History
		var started, finished int64
		var logJSON string
		if err := rows.Scan(&h.ID, &h.Container, &h.Image, &h.FromImage, &h.ToImage, &started, &finished, &h.Outcome, &h.Reason, &logJSON); err != nil {
			return nil, err
		}
		h.StartedAt, h.FinishedAt = time.UnixMilli(started), time.UnixMilli(finished)
		if err := json.Unmarshal([]byte(logJSON), &h.Log); err != nil {
			return nil, fmt.Errorf("history %d log: %w", h.ID, err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

type Available struct {
	Container    string
	Image        string
	LocalDigest  string
	RemoteDigest string
	DetectedAt   time.Time
}

func (s *Store) ReplaceAvailable(list []Available) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM available`); err != nil {
		return err
	}
	for _, a := range list {
		if _, err := tx.Exec(`INSERT INTO available (container, image, local_digest, remote_digest, detected_at) VALUES (?, ?, ?, ?, ?)`,
			a.Container, a.Image, a.LocalDigest, a.RemoteDigest, a.DetectedAt.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListAvailable() ([]Available, error) {
	rows, err := s.db.Query(`SELECT container, image, local_digest, remote_digest, detected_at FROM available ORDER BY container`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Available
	for rows.Next() {
		var a Available
		var detected int64
		if err := rows.Scan(&a.Container, &a.Image, &a.LocalDigest, &a.RemoteDigest, &detected); err != nil {
			return nil, err
		}
		a.DetectedAt = time.UnixMilli(detected)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) RemoveAvailable(container string) error {
	_, err := s.db.Exec(`DELETE FROM available WHERE container = ?`, container)
	return err
}
```

`internal/store/journal.go`:

```go
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// Journal records the steps of an update in flight, so a crash can be
// repaired on the next start.
type Journal struct{ db *sql.DB }

func (s *Store) Journal() *Journal { return &Journal{db: s.db} }

type JournalEntry struct {
	ID        int64
	Container string
	Adapter   string
	Step      string
	Data      map[string]string
}

func (j *Journal) Begin(container, adapter string, data map[string]string) (int64, error) {
	b, err := json.Marshal(nonNil(data))
	if err != nil {
		return 0, err
	}
	r, err := j.db.Exec(`INSERT INTO journal (container, adapter, step, data) VALUES (?, ?, 'started', ?)`, container, adapter, string(b))
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (j *Journal) Step(id int64, step string, data map[string]string) error {
	var raw string
	if err := j.db.QueryRow(`SELECT data FROM journal WHERE id = ?`, id).Scan(&raw); err != nil {
		return fmt.Errorf("journal %d: %w", id, err)
	}
	merged := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &merged); err != nil {
		return fmt.Errorf("journal %d data: %w", id, err)
	}
	for k, v := range data {
		merged[k] = v
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	_, err = j.db.Exec(`UPDATE journal SET step = ?, data = ? WHERE id = ?`, step, string(b), id)
	return err
}

func (j *Journal) Close(id int64) error {
	_, err := j.db.Exec(`UPDATE journal SET closed = 1 WHERE id = ?`, id)
	return err
}

func (j *Journal) Open() ([]JournalEntry, error) {
	rows, err := j.db.Query(`SELECT id, container, adapter, step, data FROM journal WHERE closed = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JournalEntry
	for rows.Next() {
		var e JournalEntry
		var raw string
		if err := rows.Scan(&e.ID, &e.Container, &e.Adapter, &e.Step, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &e.Data); err != nil {
			return nil, fmt.Errorf("journal %d data: %w", e.ID, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go mod tidy && go test ./internal/store/`
Expected: `ok  github.com/jordibrouwer/nextupdate/internal/store`

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/store
git commit -m "sqlite store with history, available and journal"
```

---

### Task 2: Docker Engine API client

**Files:**
- Create: `internal/docker/types.go`, `internal/docker/client.go`, `internal/docker/ref.go`
- Test: `internal/docker/client_test.go`

**Interfaces:**
- Produces:
  - `docker.ErrNotFound` (wrapped on HTTP 404)
  - Types `ContainerSummary`, `ContainerJSON`, `ContainerState`, `Health`, `EndpointSettings`, `NetworkSettings`, `ImageJSON`, `CreateSpec`, `NetworkingConfig` (fields below)
  - `docker.API` interface:
    ```go
    ListContainers(ctx context.Context) ([]ContainerSummary, error)
    InspectContainer(ctx context.Context, id string) (ContainerJSON, error)
    InspectImage(ctx context.Context, ref string) (ImageJSON, error)
    PullImage(ctx context.Context, ref string) error
    TagImage(ctx context.Context, id, repo, tag string) error
    CreateContainer(ctx context.Context, name string, spec CreateSpec) (string, error)
    StartContainer(ctx context.Context, id string) error
    StopContainer(ctx context.Context, id string) error
    RenameContainer(ctx context.Context, id, newName string) error
    RemoveContainer(ctx context.Context, id string) error
    ```
  - `docker.New(host string) (*Client, error)`; `""` means `unix:///var/run/docker.sock`; accepts `unix://`, `tcp://`, `http://`
  - `docker.SplitRef(ref string) (repo, tag string)`

- [ ] **Step 1: Write the failing tests**

`internal/docker/client_test.go`:

```go
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New("tcp://" + strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestInspectContainer(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.44/containers/app/json" {
			t.Errorf("path %s", r.URL.Path)
		}
		io.WriteString(w, `{"Id":"abc","Name":"/app","Image":"sha256:1","RestartCount":2,
			"State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}},
			"Config":{"Image":"app:latest","Env":["A=1"]},"HostConfig":{"Binds":["/x:/y"]},
			"NetworkSettings":{"Networks":{"bridge":{"Aliases":["app"]}}}}`)
	})
	got, err := c.InspectContainer(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "abc" || got.RestartCount != 2 || got.State.Health.Status != "healthy" || got.Config["Image"] != "app:latest" {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(string(got.HostConfig), "/x:/y") || got.NetworkSettings.Networks["bridge"].Aliases[0] != "app" {
		t.Fatalf("host/network config lost: %+v", got)
	}
}

func TestNotFound(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"No such container: x"}`)
	})
	_, err := c.InspectContainer(context.Background(), "x")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestStopNotModifiedIsOK(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	})
	if err := c.StopContainer(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
}

func TestPullReportsStreamError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fromImage") != "ghcr.io/team/app" || r.URL.Query().Get("tag") != "1.2" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"error":"manifest unknown"}`+"\n")
	})
	err := c.PullImage(context.Background(), "ghcr.io/team/app:1.2")
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("want stream error, got %v", err)
	}
}

func TestCreateContainerBody(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "app" {
			t.Errorf("name %q", r.URL.Query().Get("name"))
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["Image"] != "app:2" || body["HostConfig"] == nil || body["NetworkingConfig"] == nil {
			t.Errorf("body %v", body)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"Id":"new1"}`)
	})
	id, err := c.CreateContainer(context.Background(), "app", CreateSpec{
		Config:     map[string]any{"Image": "app:2"},
		HostConfig: json.RawMessage(`{"Binds":["/x:/y"]}`),
		NetworkingConfig: NetworkingConfig{EndpointsConfig: map[string]EndpointSettings{"bridge": {}}},
	})
	if err != nil || id != "new1" {
		t.Fatalf("id %q err %v", id, err)
	}
}

func TestSplitRef(t *testing.T) {
	cases := map[string][2]string{
		"nginx":                     {"nginx", "latest"},
		"nginx:1.27":                {"nginx", "1.27"},
		"localhost:5000/app":        {"localhost:5000/app", "latest"},
		"localhost:5000/app:2":      {"localhost:5000/app", "2"},
		"ghcr.io/a/b@sha256:abc":    {"ghcr.io/a/b", "sha256:abc"},
	}
	for in, want := range cases {
		repo, tag := SplitRef(in)
		if repo != want[0] || tag != want[1] {
			t.Errorf("SplitRef(%q) = %q, %q", in, repo, tag)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/docker/`
Expected: FAIL, `undefined: New`.

- [ ] **Step 3: Write the implementation**

`internal/docker/types.go`:

```go
// Package docker is a small client for the parts of the Docker Engine API
// nextupdate needs.
package docker

import (
	"context"
	"encoding/json"
)

type ContainerSummary struct {
	ID      string            `json:"Id"`
	Names   []string          `json:"Names"`
	Image   string            `json:"Image"`
	ImageID string            `json:"ImageID"`
	Labels  map[string]string `json:"Labels"`
	State   string            `json:"State"`
}

type Health struct {
	Status string `json:"Status"`
}

type ContainerState struct {
	Status  string  `json:"Status"`
	Running bool    `json:"Running"`
	Health  *Health `json:"Health,omitempty"`
}

type EndpointSettings struct {
	Aliases    []string          `json:"Aliases,omitempty"`
	IPAMConfig json.RawMessage   `json:"IPAMConfig,omitempty"`
	Links      []string          `json:"Links,omitempty"`
	DriverOpts map[string]string `json:"DriverOpts,omitempty"`
	MacAddress string            `json:"MacAddress,omitempty"`
}

type NetworkSettings struct {
	Networks map[string]EndpointSettings `json:"Networks"`
}

// ContainerJSON keeps Config as a generic map and HostConfig as raw JSON,
// so a recreate passes on every field, including ones this code never names.
type ContainerJSON struct {
	ID              string          `json:"Id"`
	Name            string          `json:"Name"`
	Image           string          `json:"Image"`
	RestartCount    int             `json:"RestartCount"`
	State           ContainerState  `json:"State"`
	Config          map[string]any  `json:"Config"`
	HostConfig      json.RawMessage `json:"HostConfig"`
	NetworkSettings NetworkSettings `json:"NetworkSettings"`
}

type ImageJSON struct {
	ID          string         `json:"Id"`
	RepoDigests []string       `json:"RepoDigests"`
	Config      map[string]any `json:"Config"`
}

type NetworkingConfig struct {
	EndpointsConfig map[string]EndpointSettings `json:"EndpointsConfig"`
}

type CreateSpec struct {
	Config           map[string]any
	HostConfig       json.RawMessage
	NetworkingConfig NetworkingConfig
}

type API interface {
	ListContainers(ctx context.Context) ([]ContainerSummary, error)
	InspectContainer(ctx context.Context, id string) (ContainerJSON, error)
	InspectImage(ctx context.Context, ref string) (ImageJSON, error)
	PullImage(ctx context.Context, ref string) error
	TagImage(ctx context.Context, id, repo, tag string) error
	CreateContainer(ctx context.Context, name string, spec CreateSpec) (string, error)
	StartContainer(ctx context.Context, id string) error
	StopContainer(ctx context.Context, id string) error
	RenameContainer(ctx context.Context, id, newName string) error
	RemoveContainer(ctx context.Context, id string) error
}
```

`internal/docker/ref.go`:

```go
package docker

import "strings"

// SplitRef splits an image reference into repository and tag (or digest).
// A reference without tag gets "latest".
func SplitRef(ref string) (repo, tag string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}
```

`internal/docker/client.go`:

```go
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
)

const apiVersion = "v1.44"

var ErrNotFound = errors.New("not found")

type Client struct {
	hc   *http.Client
	base string
}

var _ API = (*Client)(nil)

func New(host string) (*Client, error) {
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("parse DOCKER_HOST: %w", err)
	}
	switch u.Scheme {
	case "unix":
		sock := u.Path
		tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}}
		return &Client{hc: &http.Client{Transport: tr}, base: "http://docker"}, nil
	case "tcp", "http":
		return &Client{hc: &http.Client{}, base: "http://" + u.Host}, nil
	default:
		return nil, fmt.Errorf("unsupported DOCKER_HOST scheme %q", u.Scheme)
	}
}

// do sends a request. out is nil (discard body), a func(io.Reader) error
// (stream the body), or a pointer to decode JSON into.
func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	u := c.base + "/" + apiVersion + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("docker %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified:
		return nil
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("docker %s %s: %w", method, path, ErrNotFound)
	case resp.StatusCode >= 300:
		var e struct {
			Message string `json:"message"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		return fmt.Errorf("docker %s %s: %d %s", method, path, resp.StatusCode, e.Message)
	}
	switch o := out.(type) {
	case nil:
		_, err := io.Copy(io.Discard, resp.Body)
		return err
	case func(io.Reader) error:
		return o(resp.Body)
	default:
		return json.NewDecoder(resp.Body).Decode(o)
	}
}

func (c *Client) ListContainers(ctx context.Context) ([]ContainerSummary, error) {
	var out []ContainerSummary
	err := c.do(ctx, http.MethodGet, "/containers/json", url.Values{"all": {"1"}}, nil, &out)
	return out, err
}

func (c *Client) InspectContainer(ctx context.Context, id string) (ContainerJSON, error) {
	var out ContainerJSON
	err := c.do(ctx, http.MethodGet, "/containers/"+id+"/json", nil, nil, &out)
	return out, err
}

func (c *Client) InspectImage(ctx context.Context, ref string) (ImageJSON, error) {
	var out ImageJSON
	err := c.do(ctx, http.MethodGet, "/images/"+ref+"/json", nil, nil, &out)
	return out, err
}

func (c *Client) PullImage(ctx context.Context, ref string) error {
	repo, tag := SplitRef(ref)
	q := url.Values{"fromImage": {repo}, "tag": {tag}}
	return c.do(ctx, http.MethodPost, "/images/create", q, nil, func(r io.Reader) error {
		dec := json.NewDecoder(r)
		for {
			var m struct {
				Error string `json:"error"`
			}
			if err := dec.Decode(&m); err == io.EOF {
				return nil
			} else if err != nil {
				return fmt.Errorf("read pull stream: %w", err)
			}
			if m.Error != "" {
				return fmt.Errorf("pull %s: %s", ref, m.Error)
			}
		}
	})
}

func (c *Client) TagImage(ctx context.Context, id, repo, tag string) error {
	return c.do(ctx, http.MethodPost, "/images/"+id+"/tag", url.Values{"repo": {repo}, "tag": {tag}}, nil, nil)
}

func (c *Client) CreateContainer(ctx context.Context, name string, spec CreateSpec) (string, error) {
	body := make(map[string]any, len(spec.Config)+2)
	for k, v := range spec.Config {
		body[k] = v
	}
	if len(spec.HostConfig) > 0 {
		body["HostConfig"] = spec.HostConfig
	}
	body["NetworkingConfig"] = spec.NetworkingConfig
	var out struct {
		ID string `json:"Id"`
	}
	err := c.do(ctx, http.MethodPost, "/containers/create", url.Values{"name": {name}}, body, &out)
	return out.ID, err
}

func (c *Client) StartContainer(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil, nil)
}

func (c *Client) StopContainer(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/stop", url.Values{"t": {"10"}}, nil, nil)
}

func (c *Client) RenameContainer(ctx context.Context, id, newName string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/rename", url.Values{"name": {newName}}, nil, nil)
}

func (c *Client) RemoveContainer(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/containers/"+id, url.Values{"force": {"1"}}, nil, nil)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/docker/`
Expected: `ok`

- [ ] **Step 5: Vet**

Run: `go vet ./internal/docker/`
Expected: no output. The real socket is exercised in Tasks 11 and 12.

- [ ] **Step 6: Commit**

```bash
git add internal/docker
git commit -m "docker engine api client"
```

---

### Task 3: Fake Docker and discovery

**Files:**
- Create: `internal/dockertest/fake.go`, `internal/discovery/discovery.go`
- Test: `internal/discovery/discovery_test.go`

**Interfaces:**
- Consumes: `docker.API` and types (Task 2).
- Produces:
  - `dockertest.New() *Fake`; fields `Containers map[string]*docker.ContainerJSON`, `Images map[string]docker.ImageJSON`, `Calls []string`, `PullErr, CreateErr, StartErr error`, `OnPull func(ref string)`
  - `(*Fake).AddImage(ref string, img docker.ImageJSON)` (stores under ref and under img.ID)
  - `(*Fake).AddContainer(id, name, ref string, labels map[string]string, running bool) *docker.ContainerJSON`
  - Constants `discovery.LabelProject`, `LabelService`, `LabelWorkdir`, `LabelFiles`, `OldSuffix = "-nu-old"`
  - `discovery.Source` (`SourceCompose`, `SourceRun`)
  - `discovery.Container{ID, Name, Image, ImageID string; Source Source; ComposeProject, ComposeService, ComposeWorkdir string; ComposeFiles []string}`
  - `discovery.Discover(ctx context.Context, api docker.API) ([]Container, error)`

- [ ] **Step 1: Write the fake**

`internal/dockertest/fake.go`:

```go
// Package dockertest provides an in-memory docker.API for tests.
package dockertest

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jordibrouwer/nextupdate/internal/docker"
)

type Fake struct {
	Containers map[string]*docker.ContainerJSON
	Images     map[string]docker.ImageJSON
	Calls      []string
	PullErr    error
	CreateErr  error
	StartErr   error
	OnPull     func(ref string)
	next       int
}

var _ docker.API = (*Fake)(nil)

func New() *Fake {
	return &Fake{Containers: map[string]*docker.ContainerJSON{}, Images: map[string]docker.ImageJSON{}}
}

func (f *Fake) AddImage(ref string, img docker.ImageJSON) {
	f.Images[ref] = img
	f.Images[img.ID] = img
}

func (f *Fake) AddContainer(id, name, ref string, labels map[string]string, running bool) *docker.ContainerJSON {
	lbl := map[string]any{}
	for k, v := range labels {
		lbl[k] = v
	}
	c := &docker.ContainerJSON{ID: id, Name: "/" + name, Image: f.Images[ref].ID,
		Config: map[string]any{"Image": ref, "Labels": lbl}}
	c.State.Running = running
	c.State.Status = "exited"
	if running {
		c.State.Status = "running"
	}
	f.Containers[id] = c
	return c
}

func (f *Fake) record(format string, a ...any) { f.Calls = append(f.Calls, fmt.Sprintf(format, a...)) }

func (f *Fake) find(idOrName string) (*docker.ContainerJSON, error) {
	if c, ok := f.Containers[idOrName]; ok {
		return c, nil
	}
	for _, c := range f.Containers {
		if c.Name == "/"+idOrName {
			return c, nil
		}
	}
	return nil, fmt.Errorf("container %s: %w", idOrName, docker.ErrNotFound)
}

func (f *Fake) ListContainers(ctx context.Context) ([]docker.ContainerSummary, error) {
	ids := make([]string, 0, len(f.Containers))
	for id := range f.Containers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []docker.ContainerSummary
	for _, id := range ids {
		c := f.Containers[id]
		labels := map[string]string{}
		if m, ok := c.Config["Labels"].(map[string]any); ok {
			for k, v := range m {
				labels[k], _ = v.(string)
			}
		}
		ref, _ := c.Config["Image"].(string)
		out = append(out, docker.ContainerSummary{ID: c.ID, Names: []string{c.Name}, Image: ref,
			ImageID: c.Image, Labels: labels, State: c.State.Status})
	}
	return out, nil
}

func (f *Fake) InspectContainer(ctx context.Context, id string) (docker.ContainerJSON, error) {
	c, err := f.find(id)
	if err != nil {
		return docker.ContainerJSON{}, err
	}
	return *c, nil
}

func (f *Fake) InspectImage(ctx context.Context, ref string) (docker.ImageJSON, error) {
	img, ok := f.Images[ref]
	if !ok {
		return docker.ImageJSON{}, fmt.Errorf("image %s: %w", ref, docker.ErrNotFound)
	}
	return img, nil
}

func (f *Fake) PullImage(ctx context.Context, ref string) error {
	f.record("pull %s", ref)
	if f.PullErr != nil {
		return f.PullErr
	}
	if f.OnPull != nil {
		f.OnPull(ref)
	}
	return nil
}

func (f *Fake) TagImage(ctx context.Context, id, repo, tag string) error {
	f.record("tag %s %s:%s", id, repo, tag)
	img, ok := f.Images[id]
	if !ok {
		return fmt.Errorf("image %s: %w", id, docker.ErrNotFound)
	}
	f.Images[repo+":"+tag] = img
	return nil
}

func (f *Fake) CreateContainer(ctx context.Context, name string, spec docker.CreateSpec) (string, error) {
	f.record("create %s", name)
	if f.CreateErr != nil {
		return "", f.CreateErr
	}
	if _, err := f.find(name); err == nil {
		return "", fmt.Errorf("name %s already in use", name)
	}
	ref, _ := spec.Config["Image"].(string)
	img, ok := f.Images[ref]
	if !ok {
		return "", fmt.Errorf("image %s: %w", ref, docker.ErrNotFound)
	}
	f.next++
	id := fmt.Sprintf("new%d", f.next)
	c := &docker.ContainerJSON{ID: id, Name: "/" + name, Image: img.ID, Config: spec.Config, HostConfig: spec.HostConfig}
	c.State.Status = "created"
	f.Containers[id] = c
	return id, nil
}

func (f *Fake) StartContainer(ctx context.Context, id string) error {
	f.record("start %s", id)
	if f.StartErr != nil {
		return f.StartErr
	}
	c, err := f.find(id)
	if err != nil {
		return err
	}
	c.State.Running, c.State.Status = true, "running"
	return nil
}

func (f *Fake) StopContainer(ctx context.Context, id string) error {
	f.record("stop %s", id)
	c, err := f.find(id)
	if err != nil {
		return err
	}
	c.State.Running, c.State.Status = false, "exited"
	return nil
}

func (f *Fake) RenameContainer(ctx context.Context, id, newName string) error {
	f.record("rename %s %s", id, newName)
	c, err := f.find(id)
	if err != nil {
		return err
	}
	if other, err := f.find(newName); err == nil && other.ID != c.ID {
		return fmt.Errorf("name %s already in use", newName)
	}
	c.Name = "/" + strings.TrimPrefix(newName, "/")
	return nil
}

func (f *Fake) RemoveContainer(ctx context.Context, id string) error {
	f.record("remove %s", id)
	c, err := f.find(id)
	if err != nil {
		return err
	}
	delete(f.Containers, c.ID)
	return nil
}
```

- [ ] **Step 2: Write the failing discovery test**

`internal/discovery/discovery_test.go`:

```go
package discovery

import (
	"context"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/dockertest"
)

func TestDiscover(t *testing.T) {
	f := dockertest.New()
	f.AddImage("app:latest", docker.ImageJSON{ID: "sha256:app"})
	f.AddImage("web:1", docker.ImageJSON{ID: "sha256:web"})
	f.AddContainer("c1", "app", "app:latest", nil, true)
	f.AddContainer("c2", "stack-web-1", "web:1", map[string]string{
		LabelProject: "stack", LabelService: "web",
		LabelWorkdir: "/srv/stack", LabelFiles: "/srv/stack/compose.yml,/srv/stack/override.yml",
	}, true)
	f.AddContainer("c3", "app"+OldSuffix, "app:latest", nil, false)
	// Tag moved away from the running image: list shows the ID, inspect keeps the ref.
	f.AddContainer("c4", "moved", "app:latest", nil, true)

	got, err := Discover(context.Background(), &movedTagAPI{Fake: f, id: "c4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 containers (old copy skipped), got %+v", got)
	}
	byName := map[string]Container{}
	for _, c := range got {
		byName[c.Name] = c
	}
	if c := byName["app"]; c.Source != SourceRun || c.Image != "app:latest" || c.ImageID != "sha256:app" {
		t.Fatalf("app: %+v", c)
	}
	web := byName["stack-web-1"]
	if web.Source != SourceCompose || web.ComposeProject != "stack" || web.ComposeService != "web" ||
		web.ComposeWorkdir != "/srv/stack" || len(web.ComposeFiles) != 2 {
		t.Fatalf("web: %+v", web)
	}
	if c := byName["moved"]; c.Image != "app:latest" {
		t.Fatalf("moved tag not resolved from inspect: %+v", c)
	}
}

// movedTagAPI reports an image ID in the list for one container, as Docker
// does when the tag now points at a newer image.
type movedTagAPI struct {
	*dockertest.Fake
	id string
}

func (m *movedTagAPI) ListContainers(ctx context.Context) ([]docker.ContainerSummary, error) {
	list, err := m.Fake.ListContainers(ctx)
	for i := range list {
		if list[i].ID == m.id {
			list[i].Image = list[i].ImageID
		}
	}
	return list, err
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/discovery/`
Expected: FAIL, `undefined: Discover`.

- [ ] **Step 4: Write discovery**

`internal/discovery/discovery.go`:

```go
// Package discovery finds containers and works out how they were created.
package discovery

import (
	"context"
	"fmt"
	"strings"

	"github.com/jordibrouwer/nextupdate/internal/docker"
)

const (
	LabelProject = "com.docker.compose.project"
	LabelService = "com.docker.compose.service"
	LabelWorkdir = "com.docker.compose.project.working_dir"
	LabelFiles   = "com.docker.compose.project.config_files"

	// OldSuffix marks the previous container kept during an update.
	OldSuffix = "-nu-old"
)

type Source string

const (
	SourceCompose Source = "compose"
	SourceRun     Source = "run"
)

type Container struct {
	ID             string
	Name           string
	Image          string // reference the container was created from, e.g. "nginx:latest"
	ImageID        string // image the container runs now
	Source         Source
	ComposeProject string
	ComposeService string
	ComposeWorkdir string
	ComposeFiles   []string
}

func Discover(ctx context.Context, api docker.API) ([]Container, error) {
	list, err := api.ListContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	var out []Container
	for _, s := range list {
		if len(s.Names) == 0 {
			continue
		}
		name := strings.TrimPrefix(s.Names[0], "/")
		if strings.HasSuffix(name, OldSuffix) {
			continue
		}
		ref := s.Image
		if isImageID(ref) {
			full, err := api.InspectContainer(ctx, s.ID)
			if err != nil {
				return nil, fmt.Errorf("inspect %s: %w", name, err)
			}
			ref, _ = full.Config["Image"].(string)
			if ref == "" || isImageID(ref) {
				continue // created from a bare image ID; nothing to update from
			}
		}
		c := Container{ID: s.ID, Name: name, Image: ref, ImageID: s.ImageID, Source: SourceRun}
		if p, wd := s.Labels[LabelProject], s.Labels[LabelWorkdir]; p != "" && wd != "" {
			c.Source = SourceCompose
			c.ComposeProject = p
			c.ComposeService = s.Labels[LabelService]
			c.ComposeWorkdir = wd
			if f := s.Labels[LabelFiles]; f != "" {
				c.ComposeFiles = strings.Split(f, ",")
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func isImageID(s string) bool {
	if strings.HasPrefix(s, "sha256:") {
		return true
	}
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/discovery/ ./internal/dockertest/`
Expected: `ok` for discovery, `no test files` for dockertest.

- [ ] **Step 6: Commit**

```bash
git add internal/dockertest internal/discovery
git commit -m "discover containers and their source"
```

---

### Task 4: Registry digests

**Files:**
- Create: `internal/registry/registry.go`
- Test: `internal/registry/registry_test.go`

**Interfaces:**
- Consumes: `docker.ImageJSON`.
- Produces:
  - `registry.Checker` interface: `RemoteDigest(ctx context.Context, ref string) (string, error)`
  - `registry.NewRemote(opts ...remote.Option) *Remote` (implements `Checker`)
  - `registry.LocalDigest(img docker.ImageJSON, ref string) (string, bool)`

- [ ] **Step 1: Add the dependency**

```bash
go get github.com/google/go-containerregistry
```

- [ ] **Step 2: Write the failing tests**

`internal/registry/registry_test.go`:

```go
package registry

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	ggcrregistry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/jordibrouwer/nextupdate/internal/docker"
)

func TestRemoteDigest(t *testing.T) {
	srv := httptest.NewServer(ggcrregistry.New())
	defer srv.Close()
	ref := strings.TrimPrefix(srv.URL, "http://") + "/team/app:1.0"

	img, err := random.Image(256, 1)
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
	want, _ := img.Digest()

	got, err := NewRemote().RemoteDigest(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if got != want.String() {
		t.Fatalf("got %s want %s", got, want)
	}
}

func TestLocalDigest(t *testing.T) {
	img := docker.ImageJSON{RepoDigests: []string{
		"ghcr.io/team/other@sha256:1111111111111111111111111111111111111111111111111111111111111111",
		"nginx@sha256:2222222222222222222222222222222222222222222222222222222222222222",
	}}
	got, ok := LocalDigest(img, "nginx:latest")
	if !ok || got != "sha256:2222222222222222222222222222222222222222222222222222222222222222" {
		t.Fatalf("got %q %v", got, ok)
	}
	got, ok = LocalDigest(img, "docker.io/library/nginx:1.27")
	if !ok || !strings.HasSuffix(got, "2222") {
		t.Fatalf("normalised name not matched: %q %v", got, ok)
	}
	if _, ok := LocalDigest(img, "ghcr.io/team/app:1"); ok {
		t.Fatal("unrelated repo matched")
	}
	if _, ok := LocalDigest(docker.ImageJSON{}, "nginx"); ok {
		t.Fatal("locally built image must not have a digest")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./internal/registry/`
Expected: FAIL, `undefined: NewRemote`.

- [ ] **Step 4: Write the implementation**

`internal/registry/registry.go`:

```go
// Package registry compares the digest a container runs with the digest a
// registry serves, without pulling.
package registry

import (
	"context"
	"fmt"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/jordibrouwer/nextupdate/internal/docker"
)

type Checker interface {
	RemoteDigest(ctx context.Context, ref string) (string, error)
}

type Remote struct{ opts []remote.Option }

func NewRemote(opts ...remote.Option) *Remote {
	return &Remote{opts: append([]remote.Option{remote.WithAuthFromKeychain(authn.DefaultKeychain)}, opts...)}
}

// RemoteDigest does a manifest HEAD request. On Docker Hub a HEAD does not
// count toward the pull rate limit.
func (r *Remote) RemoteDigest(ctx context.Context, ref string) (string, error) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", ref, err)
	}
	desc, err := remote.Head(parsed, append([]remote.Option{remote.WithContext(ctx)}, r.opts...)...)
	if err != nil {
		return "", fmt.Errorf("head %s: %w", ref, err)
	}
	return desc.Digest.String(), nil
}

// LocalDigest returns the registry digest of a local image for the
// repository of ref. A locally built image has none.
func LocalDigest(img docker.ImageJSON, ref string) (string, bool) {
	parsed, err := name.ParseReference(ref)
	if err != nil {
		return "", false
	}
	repo := parsed.Context().Name()
	for _, rd := range img.RepoDigests {
		d, err := name.NewDigest(rd)
		if err != nil {
			continue
		}
		if d.Context().Name() == repo {
			return d.DigestStr(), true
		}
	}
	return "", false
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go mod tidy && go test ./internal/registry/`
Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/registry
git commit -m "compare local and remote image digests"
```

---

### Task 5: Verification

**Files:**
- Create: `internal/verify/verify.go`
- Test: `internal/verify/verify_test.go`

**Interfaces:**
- Consumes: `docker.API`, `dockertest.Fake`.
- Produces:
  - `verify.Check{Window, Interval time.Duration; HTTPURL string; MaxRestarts int}` (MaxRestarts 0 means 3)
  - `verify.Result{OK bool; Reason string}`
  - `verify.Verify(ctx context.Context, api docker.API, id string, c Check) Result`

Rules, in order, on each poll:
1. Inspect error → fail.
2. `RestartCount >= MaxRestarts` → fail `crashloop`.
3. Not running and status not `restarting`/`created` → fail `container exited`.
4. Health `healthy` → stop polling, success so far. Health `unhealthy` → fail.
5. Window passed: health present but not healthy → fail; no health → success so far.
Then, if `HTTPURL` set: GET must return 2xx.

- [ ] **Step 1: Write the failing tests**

`internal/verify/verify_test.go`:

```go
package verify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/dockertest"
)

func setup(t *testing.T, mutate func(c *docker.ContainerJSON)) *dockertest.Fake {
	t.Helper()
	f := dockertest.New()
	f.AddImage("app:1", docker.ImageJSON{ID: "sha256:1"})
	c := f.AddContainer("c1", "app", "app:1", nil, true)
	mutate(c)
	return f
}

var fast = Check{Window: 60 * time.Millisecond, Interval: 5 * time.Millisecond}

func TestVerify(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *docker.ContainerJSON)
		ok     bool
		reason string
	}{
		{"healthy", func(c *docker.ContainerJSON) { c.State.Health = &docker.Health{Status: "healthy"} }, true, ""},
		{"unhealthy", func(c *docker.ContainerJSON) { c.State.Health = &docker.Health{Status: "unhealthy"} }, false, "unhealthy"},
		{"never healthy", func(c *docker.ContainerJSON) { c.State.Health = &docker.Health{Status: "starting"} }, false, "not healthy"},
		{"no healthcheck, stays up", func(c *docker.ContainerJSON) {}, true, ""},
		{"exited", func(c *docker.ContainerJSON) { c.State.Running, c.State.Status = false, "exited" }, false, "exited"},
		{"crashloop", func(c *docker.ContainerJSON) { c.RestartCount = 3 }, false, "crashloop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Verify(context.Background(), setup(t, tc.mutate), "c1", fast)
			if got.OK != tc.ok || !strings.Contains(got.Reason, tc.reason) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestVerifyHTTP(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()
	healthy := func(c *docker.ContainerJSON) { c.State.Health = &docker.Health{Status: "healthy"} }
	chk := fast
	chk.HTTPURL = srv.URL

	if got := Verify(context.Background(), setup(t, healthy), "c1", chk); !got.OK {
		t.Fatalf("200 should pass: %+v", got)
	}
	status = http.StatusBadGateway
	if got := Verify(context.Background(), setup(t, healthy), "c1", chk); got.OK || !strings.Contains(got.Reason, "502") {
		t.Fatalf("502 should fail: %+v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/verify/`
Expected: FAIL, `undefined: Check`.

- [ ] **Step 3: Write the implementation**

`internal/verify/verify.go`:

```go
// Package verify decides whether a freshly started container works.
package verify

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/docker"
)

type Check struct {
	Window      time.Duration
	Interval    time.Duration
	HTTPURL     string
	MaxRestarts int
}

type Result struct {
	OK     bool
	Reason string
}

func fail(format string, a ...any) Result { return Result{Reason: fmt.Sprintf(format, a...)} }

func Verify(ctx context.Context, api docker.API, id string, c Check) Result {
	if c.MaxRestarts <= 0 {
		c.MaxRestarts = 3
	}
	if c.Interval <= 0 {
		c.Interval = time.Second
	}
	deadline := time.Now().Add(c.Window)
	for {
		st, err := api.InspectContainer(ctx, id)
		if err != nil {
			return fail("inspect: %v", err)
		}
		if st.RestartCount >= c.MaxRestarts {
			return fail("crashloop: %d restarts", st.RestartCount)
		}
		if !st.State.Running && st.State.Status != "restarting" && st.State.Status != "created" {
			return fail("container exited (status %s)", st.State.Status)
		}
		if h := st.State.Health; h != nil {
			if h.Status == "healthy" {
				break
			}
			if h.Status == "unhealthy" {
				return fail("healthcheck unhealthy")
			}
		}
		if time.Now().After(deadline) {
			if st.State.Health != nil {
				return fail("healthcheck not healthy within %s", c.Window)
			}
			break
		}
		select {
		case <-ctx.Done():
			return fail("cancelled: %v", ctx.Err())
		case <-time.After(c.Interval):
		}
	}
	if c.HTTPURL != "" {
		return checkHTTP(ctx, c.HTTPURL)
	}
	return Result{OK: true}
}

func checkHTTP(ctx context.Context, url string) Result {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fail("http check: %v", err)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return fail("http check: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fail("http check: status %d", resp.StatusCode)
	}
	return Result{OK: true}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/verify/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/verify
git commit -m "verify new containers: health, crashloop, http"
```

---

### Task 6: Recreate spec from inspect

**Files:**
- Create: `internal/updater/updater.go`, `internal/updater/recreate.go`
- Test: `internal/updater/recreate_test.go`

**Interfaces:**
- Consumes: `docker` types, `discovery.Container`, `verify.Result`, `store.JournalEntry`.
- Produces (in `updater.go`):
  ```go
  const (OutcomeOK = "ok"; OutcomeRolledBack = "rolled_back"; OutcomeFailed = "failed")
  type Result struct { Outcome, Reason, FromImage, ToImage string; Log []string }
  type Adapter interface { Update(ctx context.Context, c discovery.Container) Result }
  type Verifier func(ctx context.Context, id string) verify.Result
  type Journal interface {
      Begin(container, adapter string, data map[string]string) (int64, error)
      Step(id int64, step string, data map[string]string) error
      Close(id int64) error
      Open() ([]store.JournalEntry, error)
  }
  type Runner interface { Run(ctx context.Context, dir string, args ...string) (string, error) }
  ```
- Produces (in `recreate.go`): `updater.BuildSpec(old docker.ContainerJSON, oldImg docker.ImageJSON, ref string) docker.CreateSpec`

`BuildSpec` copies the old container's config, points it at `ref`, and removes everything that came from the **old image** rather than from the user, so the new image's defaults apply (Watchtower's approach):
- `Env` entries and `Labels` identical to the old image's are dropped.
- `Cmd`, `Entrypoint`, `WorkingDir`, `User`, `ExposedPorts`, `Volumes`, `Healthcheck`, `StopSignal` are dropped when equal to the old image's value.
- `Hostname` equal to the old short ID (Docker's default) is dropped; so is that short ID in network aliases.
- `HostConfig` passes through unchanged.

- [ ] **Step 1: Write the shared types**

`internal/updater/updater.go`:

```go
// Package updater replaces a container with one running a newer image and
// rolls back when the new one fails verification.
package updater

import (
	"context"

	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/verify"
)

const (
	OutcomeOK         = "ok"
	OutcomeRolledBack = "rolled_back"
	OutcomeFailed     = "failed"
)

// Result of one update. FromImage and ToImage are image IDs.
type Result struct {
	Outcome   string
	Reason    string
	FromImage string
	ToImage   string
	Log       []string
}

// Adapter performs plan, apply, verify and rollback for one kind of container.
type Adapter interface {
	Update(ctx context.Context, c discovery.Container) Result
}

type Verifier func(ctx context.Context, id string) verify.Result

type Journal interface {
	Begin(container, adapter string, data map[string]string) (int64, error)
	Step(id int64, step string, data map[string]string) error
	Close(id int64) error
	Open() ([]store.JournalEntry, error)
}

type Runner interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

var _ Journal = (*store.Journal)(nil)
```

- [ ] **Step 2: Write the failing test**

`internal/updater/recreate_test.go`:

```go
package updater

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/docker"
)

func TestBuildSpec(t *testing.T) {
	old := docker.ContainerJSON{
		ID: "0123456789abcdef0123",
		Config: map[string]any{
			"Image":    "app:latest",
			"Hostname": "0123456789ab",
			"Env":      []any{"PATH=/usr/bin", "APP_VERSION=1.0", "TZ=Europe/Amsterdam"},
			"Labels":   map[string]any{"org.opencontainers.image.version": "1.0", "mine": "yes"},
			"Cmd":      []any{"serve"},
			"User":     "1000",
		},
		HostConfig: json.RawMessage(`{"Binds":["/data:/data"]}`),
		NetworkSettings: docker.NetworkSettings{Networks: map[string]docker.EndpointSettings{
			"proxy": {Aliases: []string{"app", "0123456789ab"}},
		}},
	}
	oldImg := docker.ImageJSON{Config: map[string]any{
		"Env":    []any{"PATH=/usr/bin", "APP_VERSION=1.0"},
		"Labels": map[string]any{"org.opencontainers.image.version": "1.0"},
		"Cmd":    []any{"serve"},
		"User":   "",
	}}

	spec := BuildSpec(old, oldImg, "app:latest")

	if spec.Config["Image"] != "app:latest" {
		t.Errorf("image %v", spec.Config["Image"])
	}
	if !reflect.DeepEqual(spec.Config["Env"], []any{"TZ=Europe/Amsterdam"}) {
		t.Errorf("env %v", spec.Config["Env"])
	}
	if !reflect.DeepEqual(spec.Config["Labels"], map[string]any{"mine": "yes"}) {
		t.Errorf("labels %v", spec.Config["Labels"])
	}
	if _, ok := spec.Config["Cmd"]; ok {
		t.Error("Cmd equal to image default must be dropped")
	}
	if spec.Config["User"] != "1000" {
		t.Error("user-set User must stay")
	}
	if _, ok := spec.Config["Hostname"]; ok {
		t.Error("default hostname must be dropped")
	}
	if got := spec.NetworkingConfig.EndpointsConfig["proxy"].Aliases; !reflect.DeepEqual(got, []string{"app"}) {
		t.Errorf("aliases %v", got)
	}
	if string(spec.HostConfig) != `{"Binds":["/data:/data"]}` {
		t.Errorf("host config %s", spec.HostConfig)
	}
	if old.Config["Hostname"] != "0123456789ab" {
		t.Error("BuildSpec must not mutate the old config")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/updater/`
Expected: FAIL, `undefined: BuildSpec`.

- [ ] **Step 4: Write the implementation**

`internal/updater/recreate.go`:

```go
package updater

import (
	"reflect"

	"github.com/jordibrouwer/nextupdate/internal/docker"
)

var imageDefaultKeys = []string{"Cmd", "Entrypoint", "WorkingDir", "User", "ExposedPorts", "Volumes", "Healthcheck", "StopSignal"}

// BuildSpec turns an inspected container into a create request for ref,
// keeping what the user set and dropping what the old image supplied.
func BuildSpec(old docker.ContainerJSON, oldImg docker.ImageJSON, ref string) docker.CreateSpec {
	cfg := make(map[string]any, len(old.Config))
	for k, v := range old.Config {
		cfg[k] = v
	}
	cfg["Image"] = ref
	imgCfg := oldImg.Config
	if imgCfg == nil {
		imgCfg = map[string]any{}
	}
	if v, ok := cfg["Env"]; ok {
		cfg["Env"] = subtractList(v, imgCfg["Env"])
	}
	if v, ok := cfg["Labels"]; ok {
		cfg["Labels"] = subtractMap(v, imgCfg["Labels"])
	}
	for _, k := range imageDefaultKeys {
		if v, ok := cfg[k]; ok && reflect.DeepEqual(v, imgCfg[k]) {
			delete(cfg, k)
		}
	}
	short := shortID(old.ID)
	if h, _ := cfg["Hostname"].(string); h == short {
		delete(cfg, "Hostname")
	}
	eps := make(map[string]docker.EndpointSettings, len(old.NetworkSettings.Networks))
	for name, ep := range old.NetworkSettings.Networks {
		ep.Aliases = without(ep.Aliases, short)
		eps[name] = ep
	}
	return docker.CreateSpec{Config: cfg, HostConfig: old.HostConfig, NetworkingConfig: docker.NetworkingConfig{EndpointsConfig: eps}}
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func subtractList(a, b any) any {
	al, ok := a.([]any)
	if !ok {
		return a
	}
	drop := map[any]bool{}
	if bl, ok := b.([]any); ok {
		for _, v := range bl {
			drop[v] = true
		}
	}
	out := []any{}
	for _, v := range al {
		if !drop[v] {
			out = append(out, v)
		}
	}
	return out
}

func subtractMap(a, b any) any {
	am, ok := a.(map[string]any)
	if !ok {
		return a
	}
	bm, _ := b.(map[string]any)
	out := map[string]any{}
	for k, v := range am {
		if bv, ok := bm[k]; ok && reflect.DeepEqual(bv, v) {
			continue
		}
		out[k] = v
	}
	return out
}

func without(list []string, drop string) []string {
	var out []string
	for _, s := range list {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/updater/`
Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add internal/updater
git commit -m "build a create spec from an inspected container"
```

---

### Task 7: Run adapter

**Files:**
- Create: `internal/updater/run.go`
- Test: `internal/updater/run_test.go`

**Interfaces:**
- Consumes: `BuildSpec`, `Result`, `Journal`, `Verifier` (Task 6); `discovery.OldSuffix`; `docker.API`.
- Produces: `updater.Run{API docker.API; Journal Journal; Verify Verifier}` implementing `Adapter`.

Journal steps written (Reconcile in Task 9 relies on these exact names and keys):
- `Begin(name, "run", {"old_id", "was_running": "true"|"false"})`
- `Step("renamed_old", nil)`
- `Step("created_new", {"new_id"})`
- `Step("verified", nil)`
- `Close` on every finished path, **except** when a rollback itself fails (entry stays open for Reconcile).

- [ ] **Step 1: Write the failing tests**

`internal/updater/run_test.go`:

```go
package updater

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/dockertest"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/verify"
)

func testJournal(t *testing.T) *store.Journal {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s.Journal()
}

// runFixture: container "app" (c1) runs app:latest = sha256:old; a pull
// moves app:latest to sha256:new.
func runFixture(t *testing.T) (*dockertest.Fake, discovery.Container) {
	f := dockertest.New()
	f.AddImage("app:latest", docker.ImageJSON{ID: "sha256:old"})
	f.AddContainer("c1", "app", "app:latest", nil, true)
	f.OnPull = func(ref string) { f.AddImage(ref, docker.ImageJSON{ID: "sha256:new"}) }
	return f, discovery.Container{ID: "c1", Name: "app", Image: "app:latest", ImageID: "sha256:old", Source: discovery.SourceRun}
}

func verifier(ok bool) Verifier {
	return func(ctx context.Context, id string) verify.Result {
		if ok {
			return verify.Result{OK: true}
		}
		return verify.Result{Reason: "healthcheck unhealthy"}
	}
}

func TestRunUpdateOK(t *testing.T) {
	f, c := runFixture(t)
	j := testJournal(t)
	res := (&Run{API: f, Journal: j, Verify: verifier(true)}).Update(context.Background(), c)

	if res.Outcome != OutcomeOK || res.FromImage != "sha256:old" || res.ToImage != "sha256:new" {
		t.Fatalf("res %+v", res)
	}
	cur, err := f.InspectContainer(context.Background(), "app")
	if err != nil || cur.Image != "sha256:new" || !cur.State.Running {
		t.Fatalf("app after update: %+v %v", cur, err)
	}
	if _, err := f.InspectContainer(context.Background(), "c1"); !errors.Is(err, docker.ErrNotFound) {
		t.Fatal("old container should be removed")
	}
	if open, _ := j.Open(); len(open) != 0 {
		t.Fatalf("journal left open: %+v", open)
	}
}

func TestRunUpdateRollsBack(t *testing.T) {
	f, c := runFixture(t)
	j := testJournal(t)
	res := (&Run{API: f, Journal: j, Verify: verifier(false)}).Update(context.Background(), c)

	if res.Outcome != OutcomeRolledBack || !strings.Contains(res.Reason, "unhealthy") {
		t.Fatalf("res %+v", res)
	}
	cur, err := f.InspectContainer(context.Background(), "app")
	if err != nil || cur.ID != "c1" || !cur.State.Running {
		t.Fatalf("old container not restored: %+v %v", cur, err)
	}
	if len(f.Containers) != 1 {
		t.Fatalf("new container not removed: %d containers", len(f.Containers))
	}
	if open, _ := j.Open(); len(open) != 0 {
		t.Fatalf("journal left open: %+v", open)
	}
}

func TestRunPullFailureChangesNothing(t *testing.T) {
	f, c := runFixture(t)
	f.PullErr = errors.New("manifest unknown")
	res := (&Run{API: f, Journal: testJournal(t), Verify: verifier(true)}).Update(context.Background(), c)
	if res.Outcome != OutcomeFailed || !strings.Contains(res.Reason, "manifest unknown") {
		t.Fatalf("res %+v", res)
	}
	if cur, _ := f.InspectContainer(context.Background(), "c1"); !cur.State.Running || cur.Name != "/app" {
		t.Fatalf("container touched: %+v", cur)
	}
}

func TestRunStoppedContainerStaysStopped(t *testing.T) {
	f, c := runFixture(t)
	f.Containers["c1"].State.Running, f.Containers["c1"].State.Status = false, "exited"
	res := (&Run{API: f, Journal: testJournal(t), Verify: verifier(false)}).Update(context.Background(), c)
	if res.Outcome != OutcomeOK {
		t.Fatalf("res %+v", res)
	}
	cur, _ := f.InspectContainer(context.Background(), "app")
	if cur.State.Running || cur.Image != "sha256:new" {
		t.Fatalf("want new, not started: %+v", cur)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/updater/ -run TestRun`
Expected: FAIL, `undefined: Run`.

- [ ] **Step 3: Write the implementation**

`internal/updater/run.go`:

```go
package updater

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/docker"
)

// Run updates a container created with plain `docker run`: keep the old
// container under a new name until the new one passes verification.
type Run struct {
	API     docker.API
	Journal Journal
	Verify  Verifier
}

func (r *Run) Update(ctx context.Context, c discovery.Container) Result {
	var res Result
	logf := func(format string, a ...any) { res.Log = append(res.Log, fmt.Sprintf(format, a...)) }
	fail := func(reason string) Result {
		res.Outcome, res.Reason = OutcomeFailed, reason
		logf("failed: %s", reason)
		return res
	}

	old, err := r.API.InspectContainer(ctx, c.ID)
	if err != nil {
		return fail("inspect container: " + err.Error())
	}
	res.FromImage = old.Image
	oldImg, err := r.API.InspectImage(ctx, old.Image)
	if err != nil {
		return fail("inspect current image: " + err.Error())
	}
	wasRunning := old.State.Running
	jid, err := r.Journal.Begin(c.Name, "run", map[string]string{"old_id": old.ID, "was_running": strconv.FormatBool(wasRunning)})
	if err != nil {
		return fail("journal: " + err.Error())
	}

	logf("pull %s", c.Image)
	if err := r.API.PullImage(ctx, c.Image); err != nil {
		r.Journal.Close(jid)
		return fail("pull: " + err.Error())
	}
	newImg, err := r.API.InspectImage(ctx, c.Image)
	if err != nil {
		r.Journal.Close(jid)
		return fail("inspect new image: " + err.Error())
	}
	res.ToImage = newImg.ID
	if newImg.ID == old.Image {
		r.Journal.Close(jid)
		logf("image unchanged")
		res.Outcome, res.Reason = OutcomeOK, "image unchanged"
		return res
	}

	rctx := context.WithoutCancel(ctx)
	rollback := func(newID, reason string) Result {
		logf("rollback: %s", reason)
		if newID != "" {
			if err := r.API.RemoveContainer(rctx, newID); err != nil && !errors.Is(err, docker.ErrNotFound) {
				return fail(reason + "; rollback remove new: " + err.Error())
			}
		}
		if err := r.API.RenameContainer(rctx, old.ID, c.Name); err != nil {
			return fail(reason + "; rollback rename: " + err.Error())
		}
		if wasRunning {
			if err := r.API.StartContainer(rctx, old.ID); err != nil {
				return fail(reason + "; rollback start: " + err.Error())
			}
		}
		r.Journal.Close(jid)
		res.Outcome, res.Reason = OutcomeRolledBack, reason
		return res
	}

	if wasRunning {
		logf("stop %s", c.Name)
		if err := r.API.StopContainer(ctx, old.ID); err != nil {
			r.Journal.Close(jid)
			return fail("stop: " + err.Error())
		}
	}
	if err := r.API.RenameContainer(rctx, old.ID, c.Name+discovery.OldSuffix); err != nil {
		if wasRunning {
			_ = r.API.StartContainer(rctx, old.ID)
		}
		r.Journal.Close(jid)
		return fail("rename old: " + err.Error())
	}
	_ = r.Journal.Step(jid, "renamed_old", nil)

	logf("create %s from %s", c.Name, c.Image)
	newID, err := r.API.CreateContainer(rctx, c.Name, BuildSpec(old, oldImg, c.Image))
	if err != nil {
		return rollback("", "create: "+err.Error())
	}
	_ = r.Journal.Step(jid, "created_new", map[string]string{"new_id": newID})

	if wasRunning {
		if err := r.API.StartContainer(rctx, newID); err != nil {
			return rollback(newID, "start: "+err.Error())
		}
		logf("verify %s", c.Name)
		if v := r.Verify(ctx, newID); !v.OK {
			return rollback(newID, "verify: "+v.Reason)
		}
	}
	_ = r.Journal.Step(jid, "verified", nil)

	if err := r.API.RemoveContainer(rctx, old.ID); err != nil {
		logf("remove old container: %v", err)
	}
	r.Journal.Close(jid)
	res.Outcome = OutcomeOK
	return res
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/updater/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/updater/run.go internal/updater/run_test.go
git commit -m "run adapter: update with rename rollback"
```

---

### Task 8: Compose adapter

**Files:**
- Create: `internal/updater/compose.go`
- Test: `internal/updater/compose_test.go`

**Interfaces:**
- Consumes: `Result`, `Journal`, `Verifier`, `Runner` (Task 6); `discovery` labels; `docker.SplitRef`.
- Produces:
  - `updater.Compose{API docker.API; Runner Runner; Journal Journal; Verify Verifier}` implementing `Adapter`
  - `updater.ExecRunner{}` implementing `Runner` by running `docker <args>` in `dir`
  - `updater.ComposeArgs(project, workdir string, files []string, extra ...string) []string`
  - `updater.FindService(ctx context.Context, api docker.API, project, service string) (string, error)`

Journal: `Begin(name, "compose", {"old_image", "ref", "project", "service", "workdir", "files" (comma-joined)})`, `Step("verified", nil)`, `Close`.

- [ ] **Step 1: Write the failing tests**

`internal/updater/compose_test.go`:

```go
package updater

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/dockertest"
)

var composeLabels = map[string]string{
	discovery.LabelProject: "stack", discovery.LabelService: "web",
	discovery.LabelWorkdir: "/srv/stack", discovery.LabelFiles: "/srv/stack/compose.yml",
}

// fakeCompose mimics `docker compose pull` and `up` against a Fake.
type fakeCompose struct {
	f     *dockertest.Fake
	calls [][]string
	upErr error
}

func (r *fakeCompose) Run(ctx context.Context, dir string, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	switch {
	case slices.Contains(args, "pull"):
		return "", r.f.PullImage(ctx, "web:latest")
	case slices.Contains(args, "up"):
		if r.upErr != nil && !slices.Contains(args, "never") {
			return "boom", r.upErr
		}
		for id, c := range r.f.Containers {
			if lbl, _ := c.Config["Labels"].(map[string]any); lbl[discovery.LabelService] == "web" {
				r.f.RemoveContainer(ctx, id)
			}
		}
		r.f.AddContainer("web-"+r.f.Images["web:latest"].ID, "stack-web-1", "web:latest", composeLabels, true)
	}
	return "", nil
}

func composeFixture(t *testing.T) (*dockertest.Fake, *fakeCompose, discovery.Container) {
	f := dockertest.New()
	f.AddImage("web:latest", docker.ImageJSON{ID: "sha256:old"})
	f.AddContainer("c1", "stack-web-1", "web:latest", composeLabels, true)
	f.OnPull = func(ref string) { f.AddImage(ref, docker.ImageJSON{ID: "sha256:new"}) }
	c := discovery.Container{ID: "c1", Name: "stack-web-1", Image: "web:latest", ImageID: "sha256:old",
		Source: discovery.SourceCompose, ComposeProject: "stack", ComposeService: "web",
		ComposeWorkdir: "/srv/stack", ComposeFiles: []string{"/srv/stack/compose.yml"}}
	return f, &fakeCompose{f: f}, c
}

func serviceImage(t *testing.T, f *dockertest.Fake) string {
	t.Helper()
	id, err := FindService(context.Background(), f, "stack", "web")
	if err != nil {
		t.Fatal(err)
	}
	return f.Containers[id].Image
}

func TestComposeUpdateOK(t *testing.T) {
	f, runner, c := composeFixture(t)
	j := testJournal(t)
	res := (&Compose{API: f, Runner: runner, Journal: j, Verify: verifier(true)}).Update(context.Background(), c)
	if res.Outcome != OutcomeOK || res.ToImage != "sha256:new" {
		t.Fatalf("res %+v", res)
	}
	if got := serviceImage(t, f); got != "sha256:new" {
		t.Fatalf("service runs %s", got)
	}
	want := []string{"compose", "-p", "stack", "--project-directory", "/srv/stack", "-f", "/srv/stack/compose.yml", "up", "-d", "--no-deps", "web"}
	if !slices.Equal(runner.calls[1], want) {
		t.Fatalf("up args %v", runner.calls[1])
	}
	if open, _ := j.Open(); len(open) != 0 {
		t.Fatalf("journal open: %+v", open)
	}
}

func TestComposeRollsBackOnVerify(t *testing.T) {
	f, runner, c := composeFixture(t)
	res := (&Compose{API: f, Runner: runner, Journal: testJournal(t), Verify: verifier(false)}).Update(context.Background(), c)
	if res.Outcome != OutcomeRolledBack {
		t.Fatalf("res %+v", res)
	}
	if got := serviceImage(t, f); got != "sha256:old" {
		t.Fatalf("service runs %s after rollback", got)
	}
	last := runner.calls[len(runner.calls)-1]
	if !strings.Contains(strings.Join(last, " "), "up -d --no-deps --pull never web") {
		t.Fatalf("rollback args %v", last)
	}
}

func TestComposeRollsBackOnUpError(t *testing.T) {
	f, runner, c := composeFixture(t)
	runner.upErr = errors.New("exit status 1")
	res := (&Compose{API: f, Runner: runner, Journal: testJournal(t), Verify: verifier(true)}).Update(context.Background(), c)
	if res.Outcome != OutcomeRolledBack || !strings.Contains(res.Reason, "boom") {
		t.Fatalf("res %+v", res)
	}
	if got := serviceImage(t, f); got != "sha256:old" {
		t.Fatalf("service runs %s", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/updater/ -run TestCompose`
Expected: FAIL, `undefined: Compose`.

- [ ] **Step 3: Write the implementation**

`internal/updater/compose.go`:

```go
package updater

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/docker"
)

// ExecRunner runs the docker CLI.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func ComposeArgs(project, workdir string, files []string, extra ...string) []string {
	args := []string{"compose", "-p", project, "--project-directory", workdir}
	for _, f := range files {
		args = append(args, "-f", f)
	}
	return append(args, extra...)
}

func FindService(ctx context.Context, api docker.API, project, service string) (string, error) {
	list, err := api.ListContainers(ctx)
	if err != nil {
		return "", err
	}
	for _, s := range list {
		if s.Labels[discovery.LabelProject] != project || s.Labels[discovery.LabelService] != service {
			continue
		}
		if len(s.Names) > 0 && strings.HasSuffix(s.Names[0], discovery.OldSuffix) {
			continue
		}
		return s.ID, nil
	}
	return "", fmt.Errorf("no container for service %s/%s", project, service)
}

// Compose updates a compose service through the compose CLI, so the compose
// file stays the source of truth. Rollback re-tags the old image and brings
// the service up without pulling.
type Compose struct {
	API     docker.API
	Runner  Runner
	Journal Journal
	Verify  Verifier
}

func (cp *Compose) Update(ctx context.Context, c discovery.Container) Result {
	var res Result
	logf := func(format string, a ...any) { res.Log = append(res.Log, fmt.Sprintf(format, a...)) }
	fail := func(reason string) Result {
		res.Outcome, res.Reason = OutcomeFailed, reason
		logf("failed: %s", reason)
		return res
	}
	args := func(extra ...string) []string {
		return ComposeArgs(c.ComposeProject, c.ComposeWorkdir, c.ComposeFiles, extra...)
	}

	old, err := cp.API.InspectContainer(ctx, c.ID)
	if err != nil {
		return fail("inspect container: " + err.Error())
	}
	res.FromImage = old.Image
	jid, err := cp.Journal.Begin(c.Name, "compose", map[string]string{
		"old_image": old.Image, "ref": c.Image, "project": c.ComposeProject, "service": c.ComposeService,
		"workdir": c.ComposeWorkdir, "files": strings.Join(c.ComposeFiles, ","),
	})
	if err != nil {
		return fail("journal: " + err.Error())
	}

	logf("compose pull %s", c.ComposeService)
	if out, err := cp.Runner.Run(ctx, c.ComposeWorkdir, args("pull", c.ComposeService)...); err != nil {
		cp.Journal.Close(jid)
		return fail(fmt.Sprintf("compose pull: %v: %s", err, out))
	}
	newImg, err := cp.API.InspectImage(ctx, c.Image)
	if err != nil {
		cp.Journal.Close(jid)
		return fail("inspect new image: " + err.Error())
	}
	res.ToImage = newImg.ID
	if newImg.ID == old.Image {
		cp.Journal.Close(jid)
		logf("image unchanged")
		res.Outcome, res.Reason = OutcomeOK, "image unchanged"
		return res
	}

	rctx := context.WithoutCancel(ctx)
	rollback := func(reason string) Result {
		logf("rollback: %s", reason)
		repo, tag := docker.SplitRef(c.Image)
		if err := cp.API.TagImage(rctx, old.Image, repo, tag); err != nil {
			return fail(reason + "; rollback tag: " + err.Error())
		}
		if out, err := cp.Runner.Run(rctx, c.ComposeWorkdir, args("up", "-d", "--no-deps", "--pull", "never", c.ComposeService)...); err != nil {
			return fail(fmt.Sprintf("%s; rollback up: %v: %s", reason, err, out))
		}
		cp.Journal.Close(jid)
		res.Outcome, res.Reason = OutcomeRolledBack, reason
		return res
	}

	logf("compose up %s", c.ComposeService)
	if out, err := cp.Runner.Run(rctx, c.ComposeWorkdir, args("up", "-d", "--no-deps", c.ComposeService)...); err != nil {
		return rollback(fmt.Sprintf("compose up: %v: %s", err, out))
	}
	if !old.State.Running {
		_ = cp.Journal.Step(jid, "verified", nil)
		cp.Journal.Close(jid)
		res.Outcome = OutcomeOK
		return res
	}
	newID, err := FindService(ctx, cp.API, c.ComposeProject, c.ComposeService)
	if err != nil {
		return rollback(err.Error())
	}
	logf("verify %s", c.Name)
	if v := cp.Verify(ctx, newID); !v.OK {
		return rollback("verify: " + v.Reason)
	}
	_ = cp.Journal.Step(jid, "verified", nil)
	cp.Journal.Close(jid)
	res.Outcome = OutcomeOK
	return res
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/updater/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/updater/compose.go internal/updater/compose_test.go
git commit -m "compose adapter: pull, up, retag rollback"
```

---

### Task 9: Reconcile after a crash

**Files:**
- Create: `internal/updater/reconcile.go`
- Test: `internal/updater/reconcile_test.go`

**Interfaces:**
- Consumes: journal steps from Tasks 7 and 8, `Journal`, `Runner`, `ComposeArgs`.
- Produces: `updater.Reconcile(ctx context.Context, api docker.API, j Journal, runner Runner) ([]string, error)` returning human-readable actions.

Rules per open entry:
- `run`, step `verified`: remove `old_id` (ignore not found), close.
- `run`, other steps: remove `new_id` if set (ignore not found); if `old_id` still exists, rename it back to the container name when needed and start it when `was_running == "true"` and it is not running; close.
- `compose`, step `verified`: close.
- `compose`, other steps: tag `old_image` as `ref`, run compose `up -d --no-deps --pull never <service>`, close.

- [ ] **Step 1: Write the failing tests**

`internal/updater/reconcile_test.go`:

```go
package updater

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/dockertest"
)

func TestReconcileRunMidUpdate(t *testing.T) {
	f := dockertest.New()
	f.AddImage("app:latest", docker.ImageJSON{ID: "sha256:new"})
	f.AddImage("sha256:old", docker.ImageJSON{ID: "sha256:old"})
	f.AddContainer("c1", "app"+discovery.OldSuffix, "sha256:old", nil, false)
	f.AddContainer("c2", "app", "app:latest", nil, true)
	j := testJournal(t)
	id, _ := j.Begin("app", "run", map[string]string{"old_id": "c1", "was_running": "true"})
	j.Step(id, "created_new", map[string]string{"new_id": "c2"})

	actions, err := Reconcile(context.Background(), f, j, nil)
	if err != nil {
		t.Fatal(err)
	}
	cur, err := f.InspectContainer(context.Background(), "app")
	if err != nil || cur.ID != "c1" || !cur.State.Running {
		t.Fatalf("old not restored: %+v %v", cur, err)
	}
	if _, ok := f.Containers["c2"]; ok {
		t.Fatal("half-made container not removed")
	}
	if len(actions) == 0 {
		t.Fatal("no actions reported")
	}
	if open, _ := j.Open(); len(open) != 0 {
		t.Fatal("journal not closed")
	}
}

func TestReconcileRunVerifiedFinishes(t *testing.T) {
	f := dockertest.New()
	f.AddImage("app:latest", docker.ImageJSON{ID: "sha256:new"})
	f.AddContainer("c1", "app"+discovery.OldSuffix, "app:latest", nil, false)
	f.AddContainer("c2", "app", "app:latest", nil, true)
	j := testJournal(t)
	id, _ := j.Begin("app", "run", map[string]string{"old_id": "c1", "was_running": "true"})
	j.Step(id, "verified", map[string]string{"new_id": "c2"})

	if _, err := Reconcile(context.Background(), f, j, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Containers["c1"]; ok {
		t.Fatal("old container should be removed")
	}
	if cur, _ := f.InspectContainer(context.Background(), "app"); cur.ID != "c2" {
		t.Fatalf("new container touched: %+v", cur)
	}
}

type recordRunner struct{ calls [][]string }

func (r *recordRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	r.calls = append(r.calls, args)
	return "", nil
}

func TestReconcileComposeRetagsAndUps(t *testing.T) {
	f := dockertest.New()
	f.AddImage("sha256:old", docker.ImageJSON{ID: "sha256:old"})
	j := testJournal(t)
	j.Begin("stack-web-1", "compose", map[string]string{"old_image": "sha256:old", "ref": "web:latest",
		"project": "stack", "service": "web", "workdir": "/srv/stack", "files": "/srv/stack/compose.yml"})
	rr := &recordRunner{}

	if _, err := Reconcile(context.Background(), f, j, rr); err != nil {
		t.Fatal(err)
	}
	if f.Images["web:latest"].ID != "sha256:old" {
		t.Fatal("old image not re-tagged")
	}
	if len(rr.calls) != 1 || !slices.Contains(rr.calls[0], "never") || !strings.Contains(strings.Join(rr.calls[0], " "), "-f /srv/stack/compose.yml") {
		t.Fatalf("calls %v", rr.calls)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/updater/ -run TestReconcile`
Expected: FAIL, `undefined: Reconcile`.

- [ ] **Step 3: Write the implementation**

`internal/updater/reconcile.go`:

```go
package updater

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

// Reconcile repairs updates that were interrupted, using the journal.
// Anything not verified is rolled back; verified updates are finished.
func Reconcile(ctx context.Context, api docker.API, j Journal, runner Runner) ([]string, error) {
	entries, err := j.Open()
	if err != nil {
		return nil, err
	}
	var actions []string
	for _, e := range entries {
		var err error
		switch e.Adapter {
		case "run":
			err = reconcileRun(ctx, api, e, &actions)
		case "compose":
			err = reconcileCompose(ctx, api, runner, e, &actions)
		default:
			err = fmt.Errorf("unknown adapter %q", e.Adapter)
		}
		if err != nil {
			return actions, fmt.Errorf("reconcile %s: %w", e.Container, err)
		}
		if err := j.Close(e.ID); err != nil {
			return actions, err
		}
	}
	return actions, nil
}

func reconcileRun(ctx context.Context, api docker.API, e store.JournalEntry, actions *[]string) error {
	oldID := e.Data["old_id"]
	if e.Step == "verified" {
		if err := api.RemoveContainer(ctx, oldID); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return err
		}
		*actions = append(*actions, fmt.Sprintf("%s: finished verified update, removed old container", e.Container))
		return nil
	}
	if newID := e.Data["new_id"]; newID != "" {
		if err := api.RemoveContainer(ctx, newID); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return err
		}
	}
	old, err := api.InspectContainer(ctx, oldID)
	if errors.Is(err, docker.ErrNotFound) {
		*actions = append(*actions, fmt.Sprintf("%s: old container gone, nothing to restore", e.Container))
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimPrefix(old.Name, "/") != e.Container {
		if err := api.RenameContainer(ctx, oldID, e.Container); err != nil {
			return err
		}
	}
	if e.Data["was_running"] == "true" && !old.State.Running {
		if err := api.StartContainer(ctx, oldID); err != nil {
			return err
		}
	}
	*actions = append(*actions, fmt.Sprintf("%s: rolled back interrupted update", e.Container))
	return nil
}

func reconcileCompose(ctx context.Context, api docker.API, runner Runner, e store.JournalEntry, actions *[]string) error {
	if e.Step == "verified" {
		*actions = append(*actions, fmt.Sprintf("%s: finished verified update", e.Container))
		return nil
	}
	repo, tag := docker.SplitRef(e.Data["ref"])
	if err := api.TagImage(ctx, e.Data["old_image"], repo, tag); err != nil {
		return err
	}
	var files []string
	if f := e.Data["files"]; f != "" {
		files = strings.Split(f, ",")
	}
	args := ComposeArgs(e.Data["project"], e.Data["workdir"], files, "up", "-d", "--no-deps", "--pull", "never", e.Data["service"])
	if out, err := runner.Run(ctx, e.Data["workdir"], args...); err != nil {
		return fmt.Errorf("compose up: %v: %s", err, out)
	}
	*actions = append(*actions, fmt.Sprintf("%s: rolled back interrupted update", e.Container))
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/updater/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/updater/reconcile.go internal/updater/reconcile_test.go
git commit -m "reconcile interrupted updates from the journal"
```

---

### Task 10: Engine

**Files:**
- Create: `internal/engine/engine.go`
- Test: `internal/engine/engine_test.go`

**Interfaces:**
- Consumes: `discovery.Discover`, `registry.Checker`, `registry.LocalDigest`, `store.Store`, `updater.Adapter`, `updater.Result`.
- Produces:
  - `engine.Engine{API docker.API; Registry registry.Checker; Store *store.Store; Run, Compose updater.Adapter; Self string; Log *log.Logger; Now func() time.Time}`
  - `(*Engine).Check(ctx context.Context) ([]store.Available, error)`
  - `(*Engine).Update(ctx context.Context, name string) (store.History, error)`
  - `engine.ErrSelfUpdate`

Behaviour:
- `Check`: discover; for each container inspect its running image (`ImageID`); skip when there is no registry digest (locally built); on a registry error log and skip; record every container whose remote digest differs; `ReplaceAvailable` with the result.
- `Update`: discover, find by name (error if missing); refuse when `Self != ""` and the container ID starts with `Self` (a container's hostname is its short ID); pick adapter by source; write history; on `ok` remove the name from `available`.

- [ ] **Step 1: Write the failing tests**

`internal/engine/engine_test.go`:

```go
package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/dockertest"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
)

type fakeRegistry map[string]string

func (r fakeRegistry) RemoteDigest(ctx context.Context, ref string) (string, error) {
	if d, ok := r[ref]; ok {
		return d, nil
	}
	return "", fmt.Errorf("unknown %s", ref)
}

type fakeAdapter struct {
	res  updater.Result
	seen []string
}

func (a *fakeAdapter) Update(ctx context.Context, c discovery.Container) updater.Result {
	a.seen = append(a.seen, c.Name)
	return a.res
}

const (
	dOld = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	dNew = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
)

func newEngine(t *testing.T) (*Engine, *fakeAdapter, *fakeAdapter) {
	t.Helper()
	f := dockertest.New()
	f.AddImage("app:latest", docker.ImageJSON{ID: "sha256:a", RepoDigests: []string{"app@" + dOld}})
	f.AddImage("same:1", docker.ImageJSON{ID: "sha256:s", RepoDigests: []string{"same@" + dOld}})
	f.AddImage("local:dev", docker.ImageJSON{ID: "sha256:l"})
	f.AddImage("down:1", docker.ImageJSON{ID: "sha256:d", RepoDigests: []string{"down@" + dOld}})
	f.AddContainer("aaaaaaaaaaaa1", "app", "app:latest", nil, true)
	f.AddContainer("s1", "same", "same:1", nil, true)
	f.AddContainer("l1", "local", "local:dev", nil, true)
	f.AddContainer("d1", "down", "down:1", nil, true)
	f.AddContainer("w1", "web", "app:latest", map[string]string{
		discovery.LabelProject: "p", discovery.LabelService: "web", discovery.LabelWorkdir: "/srv/p",
	}, true)

	st, err := store.Open(filepath.Join(t.TempDir(), "e.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	run := &fakeAdapter{res: updater.Result{Outcome: updater.OutcomeOK, FromImage: "sha256:a", ToImage: "sha256:b"}}
	comp := &fakeAdapter{res: updater.Result{Outcome: updater.OutcomeRolledBack, Reason: "verify: unhealthy"}}
	e := &Engine{
		API: f, Store: st, Run: run, Compose: comp,
		Registry: fakeRegistry{"app:latest": dNew, "same:1": dOld},
		Now:      func() time.Time { return time.UnixMilli(1_700_000_000_000) },
	}
	return e, run, comp
}

func TestCheck(t *testing.T) {
	e, _, _ := newEngine(t)
	got, err := e.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Container != "app" || got[1].Container != "web" || got[0].RemoteDigest != dNew {
		t.Fatalf("want app and web (same up to date, local built, down unreachable), got %+v", got)
	}
	stored, _ := e.Store.ListAvailable()
	if len(stored) != 2 {
		t.Fatalf("stored %+v", stored)
	}
}

func TestUpdatePicksAdapterAndRecordsHistory(t *testing.T) {
	e, run, comp := newEngine(t)
	ctx := context.Background()
	if _, err := e.Check(ctx); err != nil {
		t.Fatal(err)
	}
	h, err := e.Update(ctx, "app")
	if err != nil || h.Outcome != updater.OutcomeOK || len(run.seen) != 1 {
		t.Fatalf("app: %+v %v %v", h, err, run.seen)
	}
	h, err = e.Update(ctx, "web")
	if err != nil || h.Outcome != updater.OutcomeRolledBack || len(comp.seen) != 1 {
		t.Fatalf("web: %+v %v", h, err)
	}
	hist, _ := e.Store.ListHistory(10)
	if len(hist) != 2 {
		t.Fatalf("history %+v", hist)
	}
	avail, _ := e.Store.ListAvailable()
	if len(avail) != 1 || avail[0].Container != "web" {
		t.Fatalf("only the rolled-back update stays available: %+v", avail)
	}
}

func TestUpdateRefusesSelfAndUnknown(t *testing.T) {
	e, _, _ := newEngine(t)
	e.Self = "aaaaaaaaaaaa"
	if _, err := e.Update(context.Background(), "app"); !errors.Is(err, ErrSelfUpdate) {
		t.Fatalf("want ErrSelfUpdate, got %v", err)
	}
	if _, err := e.Update(context.Background(), "nope"); err == nil {
		t.Fatal("unknown container must error")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/engine/`
Expected: FAIL, `undefined: Engine`.

- [ ] **Step 3: Write the implementation**

`internal/engine/engine.go`:

```go
// Package engine finds available updates and applies them.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/registry"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
)

var ErrSelfUpdate = errors.New("nextupdate does not update its own container yet")

type Engine struct {
	API      docker.API
	Registry registry.Checker
	Store    *store.Store
	Run      updater.Adapter
	Compose  updater.Adapter
	Self     string // own hostname = own short container ID
	Log      *log.Logger
	Now      func() time.Time
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) logf(format string, a ...any) {
	if e.Log != nil {
		e.Log.Printf(format, a...)
	} else {
		log.Printf(format, a...)
	}
}

func (e *Engine) Check(ctx context.Context) ([]store.Available, error) {
	containers, err := discovery.Discover(ctx, e.API)
	if err != nil {
		return nil, err
	}
	var out []store.Available
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
		if err != nil {
			e.logf("check %s: %v", c.Name, err)
			continue
		}
		if remote != local {
			out = append(out, store.Available{Container: c.Name, Image: c.Image, LocalDigest: local, RemoteDigest: remote, DetectedAt: e.now()})
		}
	}
	if err := e.Store.ReplaceAvailable(out); err != nil {
		return nil, err
	}
	return e.Store.ListAvailable()
}

func (e *Engine) Update(ctx context.Context, name string) (store.History, error) {
	containers, err := discovery.Discover(ctx, e.API)
	if err != nil {
		return store.History{}, err
	}
	var target *discovery.Container
	for i := range containers {
		if containers[i].Name == name {
			target = &containers[i]
		}
	}
	if target == nil {
		return store.History{}, fmt.Errorf("no container named %q", name)
	}
	if e.Self != "" && strings.HasPrefix(target.ID, e.Self) {
		return store.History{}, ErrSelfUpdate
	}
	adapter := e.Run
	if target.Source == discovery.SourceCompose {
		adapter = e.Compose
	}
	started := e.now()
	res := adapter.Update(ctx, *target)
	h := store.History{
		Container: name, Image: target.Image, FromImage: res.FromImage, ToImage: res.ToImage,
		StartedAt: started, FinishedAt: e.now(), Outcome: res.Outcome, Reason: res.Reason, Log: res.Log,
	}
	if h.ID, err = e.Store.AddHistory(h); err != nil {
		return h, err
	}
	if res.Outcome == updater.OutcomeOK {
		if err := e.Store.RemoveAvailable(name); err != nil {
			return h, err
		}
	}
	return h, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/engine/`
Expected: `ok`

- [ ] **Step 5: Run the whole unit suite once**

Run: `go vet ./... && go test ./...`
Expected: every package `ok` (dockertest: `no test files`).

- [ ] **Step 6: Commit**

```bash
git add internal/engine
git commit -m "engine: check and update"
```

---

### Task 11: CLI and container image

**Files:**
- Create: `cmd/nextupdate/main.go`, `Dockerfile`, `.dockerignore`

**Interfaces:**
- Consumes: everything above.
- Produces: binary `nextupdate` with subcommands `check`, `update <name>`, `reconcile`. Environment: `DOCKER_HOST` (default unix socket), `NEXTUPDATE_DATA` (default `/data`), `NEXTUPDATE_VERIFY_WINDOW` (default `60s`).

- [ ] **Step 1: Write the CLI**

`cmd/nextupdate/main.go`:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/engine"
	"github.com/jordibrouwer/nextupdate/internal/registry"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
	"github.com/jordibrouwer/nextupdate/internal/verify"
)

const usage = `usage: nextupdate <command>

commands:
  check            list containers with a newer image
  update <name>    update one container, roll back if it fails
  reconcile        repair updates interrupted by a crash`

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
	window, err := time.ParseDuration(envOr("NEXTUPDATE_VERIFY_WINDOW", "60s"))
	if err != nil {
		return fmt.Errorf("NEXTUPDATE_VERIFY_WINDOW: %w", err)
	}
	chk := verify.Check{Window: window, Interval: time.Second, MaxRestarts: 3}
	verifier := func(ctx context.Context, id string) verify.Result { return verify.Verify(ctx, api, id, chk) }
	journal := st.Journal()
	runner := updater.ExecRunner{}
	self, _ := os.Hostname()
	eng := &engine.Engine{
		API: api, Registry: registry.NewRemote(), Store: st, Self: self,
		Run:     &updater.Run{API: api, Journal: journal, Verify: verifier},
		Compose: &updater.Compose{API: api, Runner: runner, Journal: journal, Verify: verifier},
	}

	switch cmd {
	case "check":
		list, err := eng.Check(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("everything is up to date")
		}
		for _, a := range list {
			fmt.Printf("%-30s %s\n", a.Container, a.Image)
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
	case "reconcile":
		return reconcile(ctx, api, journal, runner)
	default:
		return fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
}

func reconcile(ctx context.Context, api docker.API, j *store.Journal, runner updater.Runner) error {
	actions, err := updater.Reconcile(ctx, api, j, runner)
	for _, a := range actions {
		fmt.Println("reconcile:", a)
	}
	return err
}
```

- [ ] **Step 2: Build and run against local Docker**

Run: `go build -o /tmp/nextupdate ./cmd/nextupdate && NEXTUPDATE_DATA=$(mktemp -d) /tmp/nextupdate check`
Expected: a list of containers with newer images, or `everything is up to date`; no error. (On macOS Docker Desktop the socket is `unix://$HOME/.docker/run/docker.sock`; set `DOCKER_HOST` to that if the default fails.)

- [ ] **Step 3: Write the image files**

`.dockerignore`:

```
.git
docs
test
```

`Dockerfile`:

```dockerfile
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/nextupdate ./cmd/nextupdate

FROM alpine:3.22
RUN apk add --no-cache docker-cli docker-cli-compose ca-certificates tzdata
COPY --from=build /out/nextupdate /usr/local/bin/nextupdate
ENV NEXTUPDATE_DATA=/data
VOLUME /data
ENTRYPOINT ["nextupdate"]
CMD ["check"]
```

- [ ] **Step 4: Build the image (background, it takes longer than 30 s)**

Run in background: `docker build -t nextupdate:dev . > /tmp/nextupdate-build.log 2>&1; echo exit=$? >> /tmp/nextupdate-build.log`
Then: `tail -3 /tmp/nextupdate-build.log`
Expected: `exit=0`.

- [ ] **Step 5: Run the image once**

Run: `docker run --rm -v /var/run/docker.sock:/var/run/docker.sock nextupdate:dev check`
Expected: same output as Step 2.

- [ ] **Step 6: Commit**

```bash
git add cmd Dockerfile .dockerignore
git commit -m "cli and container image"
```

---

### Task 12: Integration tests against real Docker

**Files:**
- Create: `test/integration/update_test.go`

**Interfaces:**
- Consumes: `docker.New`, `store.Open`, `discovery.Discover`, `updater.Run`, `updater.Compose`, `updater.ExecRunner`, `verify.Verify`.

The tests build three local busybox images (`v1`, `v2` healthy; `bad` unhealthy), so no registry is involved: the pull step is replaced by a no-op (`noPull`, `noPullRunner`), and moving the tag stands in for "a newer image was published".

- [ ] **Step 1: Write the tests**

`test/integration/update_test.go`:

```go
//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
	"github.com/jordibrouwer/nextupdate/internal/verify"
)

func dockerCLI(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	cmd := exec.Command("docker", args...)
	cmd.Dir = dir
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func buildImage(t *testing.T, tag, health string) string {
	df := fmt.Sprintf("FROM busybox:1.37\nLABEL nu.tag=%s\nHEALTHCHECK --interval=1s --timeout=1s --retries=1 CMD %s\nCMD [\"sleep\",\"3600\"]\n", tag, health)
	dockerCLI(t, "", df, "build", "-q", "-t", tag, "-")
	return dockerCLI(t, "", "", "image", "inspect", "-f", "{{.Id}}", tag)
}

type noPull struct{ docker.API }

func (noPull) PullImage(context.Context, string) error { return nil }

type noPullRunner struct{}

func (noPullRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	if slices.Contains(args, "pull") {
		return "", nil
	}
	return updater.ExecRunner{}.Run(ctx, dir, args...)
}

type env struct {
	api     *docker.Client
	journal *store.Journal
	verify  updater.Verifier
	v2, bad string
}

func setup(t *testing.T) env {
	t.Helper()
	api, err := docker.New(os.Getenv("DOCKER_HOST"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "it.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	buildImage(t, "nu-it:v1", "true")
	e := env{api: api, journal: st.Journal(), v2: buildImage(t, "nu-it:v2", "true"), bad: buildImage(t, "nu-it:bad", "false")}
	chk := verify.Check{Window: 20 * time.Second, Interval: 500 * time.Millisecond, MaxRestarts: 3}
	e.verify = func(ctx context.Context, id string) verify.Result { return verify.Verify(ctx, api, id, chk) }
	return e
}

func find(t *testing.T, api docker.API, name string) discovery.Container {
	t.Helper()
	cs, err := discovery.Discover(context.Background(), api)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("container %s not discovered", name)
	return discovery.Container{}
}

func TestRunAdapter(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	name := "nu-it-run"
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name, name+discovery.OldSuffix).Run() })
	dockerCLI(t, "", "", "tag", "nu-it:v1", "nu-it:runcur")
	dockerCLI(t, "", "", "run", "-d", "--name", name, "-e", "KEEP=me", "nu-it:runcur")
	run := &updater.Run{API: noPull{e.api}, Journal: e.journal, Verify: e.verify}

	dockerCLI(t, "", "", "tag", "nu-it:v2", "nu-it:runcur")
	if res := run.Update(ctx, find(t, e.api, name)); res.Outcome != updater.OutcomeOK {
		t.Fatalf("good update: %+v", res)
	}
	if got := dockerCLI(t, "", "", "inspect", "-f", "{{.Image}}", name); got != e.v2 {
		t.Fatalf("runs %s, want v2 %s", got, e.v2)
	}
	if got := dockerCLI(t, "", "", "inspect", "-f", "{{json .Config.Env}}", name); !strings.Contains(got, "KEEP=me") {
		t.Fatalf("user env lost: %s", got)
	}

	dockerCLI(t, "", "", "tag", "nu-it:bad", "nu-it:runcur")
	if res := run.Update(ctx, find(t, e.api, name)); res.Outcome != updater.OutcomeRolledBack {
		t.Fatalf("bad update should roll back: %+v", res)
	}
	if got := dockerCLI(t, "", "", "inspect", "-f", "{{.Image}} {{.State.Running}}", name); got != e.v2+" true" {
		t.Fatalf("after rollback: %s", got)
	}
}

func TestComposeAdapter(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	dir := t.TempDir()
	compose := "services:\n  app:\n    image: nu-it:composecur\n    pull_policy: never\n"
	if err := os.WriteFile(filepath.Join(dir, "compose.yml"), []byte(compose), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c := exec.Command("docker", "compose", "-p", "nuit", "down")
		c.Dir = dir
		c.Run()
	})
	dockerCLI(t, "", "", "tag", "nu-it:v1", "nu-it:composecur")
	dockerCLI(t, dir, "", "compose", "-p", "nuit", "up", "-d")
	comp := &updater.Compose{API: e.api, Runner: noPullRunner{}, Journal: e.journal, Verify: e.verify}
	name := "nuit-app-1"

	dockerCLI(t, "", "", "tag", "nu-it:v2", "nu-it:composecur")
	if res := comp.Update(ctx, find(t, e.api, name)); res.Outcome != updater.OutcomeOK {
		t.Fatalf("good update: %+v", res)
	}
	if got := dockerCLI(t, "", "", "inspect", "-f", "{{.Image}}", name); got != e.v2 {
		t.Fatalf("runs %s, want v2", got)
	}

	dockerCLI(t, "", "", "tag", "nu-it:bad", "nu-it:composecur")
	if res := comp.Update(ctx, find(t, e.api, name)); res.Outcome != updater.OutcomeRolledBack {
		t.Fatalf("bad update should roll back: %+v", res)
	}
	if got := dockerCLI(t, "", "", "inspect", "-f", "{{.Image}} {{.State.Running}}", name); got != e.v2+" true" {
		t.Fatalf("after rollback: %s", got)
	}
	if got := dockerCLI(t, "", "", "image", "inspect", "-f", "{{.Id}}", "nu-it:composecur"); got != e.v2 {
		t.Fatalf("tag not restored to v2: %s", got)
	}
}
```

- [ ] **Step 2: Run the integration tests (background)**

Run in background: `go test -tags integration ./test/integration/ -v -count=1 > /tmp/nextupdate-it.log 2>&1; echo exit=$? >> /tmp/nextupdate-it.log`
Then: `grep -E '^(--- |ok|FAIL|exit=)' /tmp/nextupdate-it.log`
Expected: `--- PASS: TestRunAdapter`, `--- PASS: TestComposeAdapter`, `exit=0`.

- [ ] **Step 3: Falsify the rollback test**

Temporarily change `rollback` in `internal/updater/run.go` to `return fail(reason)` as its first line, rerun Step 2, and confirm `TestRunAdapter` FAILS on "bad update should roll back". Revert the change and rerun to green.

- [ ] **Step 4: Clean up test images**

Run: `docker image rm nu-it:v1 nu-it:v2 nu-it:bad nu-it:runcur nu-it:composecur 2>/dev/null; true`

- [ ] **Step 5: Commit**

```bash
git add test
git commit -m "integration tests: update and rollback on real docker"
```

---

## Spec coverage (Plan 1)

| Spec item | Task |
|---|---|
| Discovery, compose vs run | 3 |
| Digest compare via manifest HEAD, `:latest` via digest | 4, 10 |
| Hybrid engine, run adapter with rename rollback | 6, 7 |
| Compose adapter, compose file untouched, retag rollback | 8 |
| Verifier: healthcheck, HTTP, crashloop | 5 |
| Journal + reconcile on startup | 1, 9, 11 |
| One update per container, sequential | 11 (CLI runs one update per call; queue arrives with the scheduler in Plan 2) |
| Registry 429 backoff, optional credentials | 4 (keychain credentials via `~/.docker/config.json`); explicit backoff in Plan 2 with the scheduler |
| Self-update refused (helper container in a later plan) | 10 |
| History, available tables | 1, 10 |
| Integration tests with healthy/unhealthy images | 12 |
| Changelog, classifier, policy, scheduler, old-image retention | Plan 2 |
| UI, auth, PWA, notifiers, widget | Plan 3 |
