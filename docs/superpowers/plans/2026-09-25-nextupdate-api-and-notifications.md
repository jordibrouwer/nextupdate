# nextupdate API, Login and Notifications Implementation Plan (Plan 3a of 3b)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give `nextupdate serve` an HTTP API on port 8099 with login, everything the future web UI needs (updates, changelog, apply, rollback, settings, history), notification targets (Web Push, ntfy, Gotify, Discord, Telegram, webhook, e-mail) and the nextdash widget endpoint. No web UI yet; that is Plan 3b.

**Architecture:** A new `api` package serves JSON over `net/http` (Go 1.22 method patterns). Auth is one admin user with bcrypt passwords and server-side sessions in SQLite. Long-running work (check, update, rollback) runs as background jobs, the API answers 202 and the UI polls. A `notify.Dispatcher` implements the existing `scheduler.Notifier` and fans an event out to every enabled notifier and every Web Push subscription. The engine gets a manual `Rollback`.

**Tech Stack:** Go 1.26, `net/http`, `golang.org/x/crypto/bcrypt`, `github.com/SherClockHolmes/webpush-go`, `net/smtp`, SQLite.

**Spec:** `docs/superpowers/specs/2026-09-25-nextupdate-design.md`

**Builds on:** Plans 1 and 2, implemented on branch `dev` (last commit `2dfb323`). Follow-up: Plan 3b (web UI as an embedded static app, PWA manifest and service worker). The layout is decided: list on the left, detail panel on the right (changelog, breaking reasons, actions).

## Global Constraints

- Module path `github.com/jordibrouwer/nextupdate`, Go 1.26, `CGO_ENABLED=0` must build.
- HTTP listens on `NEXTUPDATE_LISTEN`, default `:8099`. Never use 8080.
- Every non-GET API request must send the header `X-NextUpdate: 1` (CSRF defence, together with a `SameSite=Strict` cookie). Missing header: `403`.
- Session cookie `nu_session`: `HttpOnly`, `SameSite=Strict`, `Secure` when the request is HTTPS or `X-Forwarded-Proto: https`, 30 days.
- Passwords: at least 10 characters, bcrypt. Five failed logins per client address in 15 minutes lock that address for the rest of the window.
- Session tokens are 32 random bytes, base64url; only their SHA-256 hex is stored.
- API errors are JSON `{"error": "<plain English sentence>"}`. Success bodies are JSON.
- Secret notifier fields (tokens, passwords) are returned as `"********"` and a PUT that sends `"********"` keeps the stored value. Secrets are stored in the SQLite file in plain text; document that `/data` must be protected.
- Never log secrets, tokens or passwords.
- Every task ends with a commit. Short plain subject line, no `Co-Authored-By` trailer.
- Keep Bash calls under 30 s; run slow things in the background.

## File Structure

```
internal/store/schema.sql             (modify: users, sessions, settings, notifiers, push_subscriptions)
internal/store/auth.go                users, sessions, settings key/value, ErrNotFound
internal/store/notifiers.go           notifiers and push subscriptions
internal/store/auth_test.go
internal/store/notifiers_test.go
internal/auth/auth.go                 Service (setup, login, sessions) and Limiter
internal/auth/auth_test.go
internal/notify/notify.go             Message, Sender, Build, Types, SendTest
internal/notify/senders.go            ntfy, gotify, discord, telegram, webhook
internal/notify/email.go              SMTP sender
internal/notify/dispatcher.go         Dispatcher, Format
internal/notify/notify_test.go
internal/notify/email_test.go
internal/notify/dispatcher_test.go
internal/push/push.go                 VAPID keys, Sender, ErrGone
internal/push/push_test.go
internal/updater/run.go, compose.go   (modify: SkipPull)
internal/engine/engine.go             (modify: Containers, Rollback)
internal/api/api.go                   Server, Deps, middleware, helpers, session endpoints
internal/api/jobs.go                  background jobs
internal/api/updates.go               read endpoints and actions
internal/api/notifiers.go             notifier, push and widget endpoints
internal/api/api_test.go              shared test harness and session tests
internal/api/updates_test.go
internal/api/notifiers_test.go
internal/scheduler/scheduler.go       (modify: record last check time)
cmd/nextupdate/main.go                (modify: HTTP server, dispatcher)
README.md                             how to run it
Dockerfile                            (modify: EXPOSE 8099)
```

---

### Task 1: Users, sessions, settings and notifier storage

**Files:**
- Modify: `internal/store/schema.sql`
- Create: `internal/store/auth.go`, `internal/store/notifiers.go`
- Test: `internal/store/auth_test.go`, `internal/store/notifiers_test.go`

**Interfaces:**
- Produces:
  - `store.ErrNotFound`
  - `store.User{ID int64; Name, PasswordHash string}`; `(*Store).CountUsers() (int, error)`, `CreateUser(name, hash string) (User, error)`, `GetUserByName(name string) (User, error)` (`ErrNotFound` when missing; names are matched case-insensitively)
  - `(*Store).CreateSession(tokenHash string, userID int64, expires time.Time) error`, `SessionUser(tokenHash string, now time.Time) (int64, error)` (`ErrNotFound` when missing or expired), `DeleteSession(tokenHash string) error`, `DeleteExpiredSessions(now time.Time) error`
  - `(*Store).GetSetting(key string) (string, error)` (`""` when missing), `SetSetting(key, value string) error`
  - `store.Notifier{ID int64; Name, Type string; Config map[string]string; Enabled bool}`; `AddNotifier(n Notifier) (int64, error)`, `ListNotifiers() ([]Notifier, error)` (by ID), `GetNotifier(id int64) (Notifier, error)`, `UpdateNotifier(n Notifier) error`, `DeleteNotifier(id int64) error`
  - `store.PushSub{Endpoint, P256dh, Auth string; UserID int64}`; `AddPushSub(s PushSub) error` (upsert by endpoint), `ListPushSubs() ([]PushSub, error)`, `DeletePushSub(endpoint string) error`

- [ ] **Step 1: Write the failing tests**

`internal/store/auth_test.go`:

```go
package store

import (
	"errors"
	"testing"
	"time"
)

func TestUsers(t *testing.T) {
	s := openTest(t)
	if n, err := s.CountUsers(); err != nil || n != 0 {
		t.Fatalf("count %d %v", n, err)
	}
	u, err := s.CreateUser("Jordi", "hash1")
	if err != nil || u.ID == 0 {
		t.Fatalf("create %+v %v", u, err)
	}
	if _, err := s.CreateUser("jordi", "hash2"); err == nil {
		t.Fatal("duplicate name (case-insensitive) accepted")
	}
	got, err := s.GetUserByName("JORDI")
	if err != nil || got.ID != u.ID || got.PasswordHash != "hash1" || got.Name != "Jordi" {
		t.Fatalf("get %+v %v", got, err)
	}
	if _, err := s.GetUserByName("nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSessions(t *testing.T) {
	s := openTest(t)
	now := time.UnixMilli(1_700_000_000_000)
	if err := s.CreateSession("h1", 7, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("h2", 7, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if uid, err := s.SessionUser("h1", now); err != nil || uid != 7 {
		t.Fatalf("valid session: %d %v", uid, err)
	}
	if _, err := s.SessionUser("h2", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session: %v", err)
	}
	if _, err := s.SessionUser("nope", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
	if err := s.DeleteExpiredSessions(now); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession("h1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser("h1", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session still valid: %v", err)
	}
}

func TestSettingsKV(t *testing.T) {
	s := openTest(t)
	if v, err := s.GetSetting("k"); err != nil || v != "" {
		t.Fatalf("missing: %q %v", v, err)
	}
	s.SetSetting("k", "1")
	s.SetSetting("k", "2")
	if v, _ := s.GetSetting("k"); v != "2" {
		t.Fatalf("got %q", v)
	}
}
```

`internal/store/notifiers_test.go`:

```go
package store

import (
	"errors"
	"testing"
)

func TestNotifiersCRUD(t *testing.T) {
	s := openTest(t)
	id, err := s.AddNotifier(Notifier{Name: "phone", Type: "ntfy", Config: map[string]string{"url": "https://ntfy.sh", "topic": "t"}, Enabled: true})
	if err != nil || id == 0 {
		t.Fatal(id, err)
	}
	s.AddNotifier(Notifier{Name: "chat", Type: "discord", Config: map[string]string{"webhook_url": "https://x"}, Enabled: false})
	got, err := s.GetNotifier(id)
	if err != nil || got.Name != "phone" || got.Config["topic"] != "t" || !got.Enabled {
		t.Fatalf("get %+v %v", got, err)
	}
	got.Name, got.Enabled = "phone 2", false
	if err := s.UpdateNotifier(got); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListNotifiers()
	if len(list) != 2 || list[0].Name != "phone 2" || list[0].Enabled || list[1].Type != "discord" {
		t.Fatalf("list %+v", list)
	}
	if err := s.DeleteNotifier(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNotifier(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestPushSubs(t *testing.T) {
	s := openTest(t)
	s.AddPushSub(PushSub{Endpoint: "https://push/1", P256dh: "k1", Auth: "a1", UserID: 1})
	s.AddPushSub(PushSub{Endpoint: "https://push/1", P256dh: "k2", Auth: "a2", UserID: 1}) // same endpoint: replaced
	s.AddPushSub(PushSub{Endpoint: "https://push/2", P256dh: "k3", Auth: "a3", UserID: 1})
	list, err := s.ListPushSubs()
	if err != nil || len(list) != 2 || list[0].P256dh != "k2" {
		t.Fatalf("list %+v %v", list, err)
	}
	s.DeletePushSub("https://push/1")
	if list, _ = s.ListPushSubs(); len(list) != 1 || list[0].Endpoint != "https://push/2" {
		t.Fatalf("after delete %+v", list)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/store/ -run 'TestUsers|TestSessions|TestSettingsKV|TestNotifiers|TestPushSubs'`
Expected: FAIL, `undefined: ErrNotFound`.

- [ ] **Step 3: Implement**

Append to `internal/store/schema.sql`:

```sql

CREATE TABLE IF NOT EXISTS users (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT    NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT    NOT NULL,
  created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id    INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS notifiers (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  name    TEXT    NOT NULL,
  type    TEXT    NOT NULL,
  config  TEXT    NOT NULL DEFAULT '{}',
  enabled INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS push_subscriptions (
  endpoint TEXT PRIMARY KEY,
  p256dh   TEXT    NOT NULL,
  auth     TEXT    NOT NULL,
  user_id  INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL DEFAULT 0
);
```

`internal/store/auth.go`:

```go
package store

import (
	"database/sql"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

type User struct {
	ID           int64
	Name         string
	PasswordHash string
}

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(name, hash string) (User, error) {
	r, err := s.db.Exec(`INSERT INTO users (name, password_hash, created_at) VALUES (?, ?, ?)`, name, hash, time.Now().UnixMilli())
	if err != nil {
		return User{}, err
	}
	id, err := r.LastInsertId()
	return User{ID: id, Name: name, PasswordHash: hash}, err
}

func (s *Store) GetUserByName(name string) (User, error) {
	var u User
	err := s.db.QueryRow(`SELECT id, name, password_hash FROM users WHERE name = ?`, name).Scan(&u.ID, &u.Name, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) CreateSession(tokenHash string, userID int64, expires time.Time) error {
	_, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`, tokenHash, userID, expires.UnixMilli())
	return err
}

func (s *Store) SessionUser(tokenHash string, now time.Time) (int64, error) {
	var uid int64
	err := s.db.QueryRow(`SELECT user_id FROM sessions WHERE token_hash = ? AND expires_at > ?`, tokenHash, now.UnixMilli()).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return uid, err
}

func (s *Store) DeleteSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) DeleteExpiredSessions(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, now.UnixMilli())
	return err
}

// GetSetting returns "" for a key that was never set.
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
```

`internal/store/notifiers.go`:

```go
package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Notifier is one configured notification target. Config holds the
// type-specific fields (URL, token, ...); secrets are stored in plain text.
type Notifier struct {
	ID      int64
	Name    string
	Type    string
	Config  map[string]string
	Enabled bool
}

func (s *Store) AddNotifier(n Notifier) (int64, error) {
	cfg, err := json.Marshal(nonNilMap(n.Config))
	if err != nil {
		return 0, err
	}
	r, err := s.db.Exec(`INSERT INTO notifiers (name, type, config, enabled) VALUES (?, ?, ?, ?)`, n.Name, n.Type, string(cfg), n.Enabled)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func scanNotifier(sc interface{ Scan(...any) error }) (Notifier, error) {
	var n Notifier
	var cfg string
	if err := sc.Scan(&n.ID, &n.Name, &n.Type, &cfg, &n.Enabled); err != nil {
		return Notifier{}, err
	}
	if err := json.Unmarshal([]byte(cfg), &n.Config); err != nil {
		return Notifier{}, fmt.Errorf("notifier %d config: %w", n.ID, err)
	}
	return n, nil
}

func (s *Store) ListNotifiers() ([]Notifier, error) {
	rows, err := s.db.Query(`SELECT id, name, type, config, enabled FROM notifiers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notifier
	for rows.Next() {
		n, err := scanNotifier(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) GetNotifier(id int64) (Notifier, error) {
	n, err := scanNotifier(s.db.QueryRow(`SELECT id, name, type, config, enabled FROM notifiers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Notifier{}, ErrNotFound
	}
	return n, err
}

func (s *Store) UpdateNotifier(n Notifier) error {
	cfg, err := json.Marshal(nonNilMap(n.Config))
	if err != nil {
		return err
	}
	r, err := s.db.Exec(`UPDATE notifiers SET name = ?, type = ?, config = ?, enabled = ? WHERE id = ?`, n.Name, n.Type, string(cfg), n.Enabled, n.ID)
	if err != nil {
		return err
	}
	if c, _ := r.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteNotifier(id int64) error {
	_, err := s.db.Exec(`DELETE FROM notifiers WHERE id = ?`, id)
	return err
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// PushSub is one browser's Web Push subscription.
type PushSub struct {
	Endpoint string
	P256dh   string
	Auth     string
	UserID   int64
}

func (s *Store) AddPushSub(p PushSub) error {
	_, err := s.db.Exec(`INSERT INTO push_subscriptions (endpoint, p256dh, auth, user_id, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth, user_id = excluded.user_id`,
		p.Endpoint, p.P256dh, p.Auth, p.UserID, time.Now().UnixMilli())
	return err
}

func (s *Store) ListPushSubs() ([]PushSub, error) {
	rows, err := s.db.Query(`SELECT endpoint, p256dh, auth, user_id FROM push_subscriptions ORDER BY created_at, endpoint`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSub
	for rows.Next() {
		var p PushSub
		if err := rows.Scan(&p.Endpoint, &p.P256dh, &p.Auth, &p.UserID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeletePushSub(endpoint string) error {
	_, err := s.db.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	return err
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `gofmt -l internal/store; go vet ./internal/store/ && go test -count=1 ./internal/store/`
Expected: `ok`. (`TestPushSubs` orders by `created_at, endpoint`; both rows can share a millisecond, so endpoint decides and keeps the order stable.)

- [ ] **Step 5: Commit**

```bash
git add internal/store
git commit -m "store users, sessions, settings and notifiers"
```

---

### Task 2: Auth service

**Files:**
- Create: `internal/auth/auth.go`
- Test: `internal/auth/auth_test.go`

**Interfaces:**
- Consumes: Task 1 store methods.
- Produces:
  - `auth.Service{Store *store.Store; Now func() time.Time; SessionTTL time.Duration; Cost int; Limiter *Limiter}`
  - Errors `auth.ErrSetupDone`, `ErrWeakPassword`, `ErrBadLogin`, `ErrLocked`, `ErrNoSession`
  - `(*Service).NeedsSetup() (bool, error)`, `Setup(name, password string) error`, `Login(name, password, remote string) (token string, err error)`, `Authenticate(token string) (userID int64, err error)`, `Logout(token string) error`
  - `auth.NewLimiter(max int, window time.Duration, now func() time.Time) *Limiter`; `Allow(key string) bool`, `Fail(key string)`, `Reset(key string)`

Rules: `Setup` works only while no user exists; a password shorter than 10 characters is `ErrWeakPassword`; `Login` for an unknown name still runs a bcrypt compare (no timing hint) and returns `ErrBadLogin`; after `max` failures inside `window` the key is locked until the window has passed since the first failure; a successful login resets the key.

- [ ] **Step 1: Write the failing tests**

`internal/auth/auth_test.go`:

```go
package auth

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

func newService(t *testing.T) (*Service, *store.Store, *time.Time) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.UnixMilli(1_700_000_000_000)
	clock := func() time.Time { return now }
	return &Service{Store: st, Now: clock, SessionTTL: time.Hour, Cost: bcrypt.MinCost, Limiter: NewLimiter(5, 15*time.Minute, clock)}, st, &now
}

func TestSetupOnlyOnce(t *testing.T) {
	s, _, _ := newService(t)
	if need, err := s.NeedsSetup(); err != nil || !need {
		t.Fatalf("need %v %v", need, err)
	}
	if err := s.Setup("jordi", "short"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("want ErrWeakPassword, got %v", err)
	}
	if err := s.Setup("jordi", "long enough password"); err != nil {
		t.Fatal(err)
	}
	if need, _ := s.NeedsSetup(); need {
		t.Fatal("setup still needed")
	}
	if err := s.Setup("other", "another long password"); !errors.Is(err, ErrSetupDone) {
		t.Fatalf("want ErrSetupDone, got %v", err)
	}
}

func TestLoginAuthenticateLogout(t *testing.T) {
	s, st, _ := newService(t)
	s.Setup("jordi", "long enough password")

	if _, err := s.Login("jordi", "wrong password!!", "1.2.3.4"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("bad password: %v", err)
	}
	if _, err := s.Login("nobody", "long enough password", "1.2.3.4"); !errors.Is(err, ErrBadLogin) {
		t.Fatalf("unknown user: %v", err)
	}
	token, err := s.Login("JORDI", "long enough password", "1.2.3.4")
	if err != nil || len(token) < 40 {
		t.Fatalf("login: %q %v", token, err)
	}
	if _, err := st.SessionUser(token, s.Now()); err == nil {
		t.Fatal("the raw token must not be stored; only its hash")
	}
	uid, err := s.Authenticate(token)
	if err != nil || uid == 0 {
		t.Fatalf("authenticate: %d %v", uid, err)
	}
	if err := s.Logout(token); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Authenticate(token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("after logout: %v", err)
	}
	if _, err := s.Authenticate(""); !errors.Is(err, ErrNoSession) {
		t.Fatalf("empty token: %v", err)
	}
}

func TestSessionExpires(t *testing.T) {
	s, _, now := newService(t)
	s.Setup("jordi", "long enough password")
	token, _ := s.Login("jordi", "long enough password", "ip")
	*now = now.Add(2 * time.Hour)
	if _, err := s.Authenticate(token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired session accepted: %v", err)
	}
}

func TestLoginLocksAfterFiveFailures(t *testing.T) {
	s, _, now := newService(t)
	s.Setup("jordi", "long enough password")
	for i := 0; i < 5; i++ {
		if _, err := s.Login("jordi", "wrong password!!", "9.9.9.9"); !errors.Is(err, ErrBadLogin) {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	if _, err := s.Login("jordi", "long enough password", "9.9.9.9"); !errors.Is(err, ErrLocked) {
		t.Fatalf("even the right password must be refused while locked: %v", err)
	}
	if _, err := s.Login("jordi", "long enough password", "8.8.8.8"); err != nil {
		t.Fatalf("another address is not locked: %v", err)
	}
	*now = now.Add(16 * time.Minute)
	if _, err := s.Login("jordi", "long enough password", "9.9.9.9"); err != nil {
		t.Fatalf("lock must end after the window: %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go get golang.org/x/crypto/bcrypt && go test ./internal/auth/`
Expected: FAIL, `undefined: Service`.

- [ ] **Step 3: Implement**

`internal/auth/auth.go`:

```go
// Package auth handles the single admin login and its sessions.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

var (
	ErrSetupDone    = errors.New("an admin account already exists")
	ErrWeakPassword = errors.New("the password must be at least 10 characters")
	ErrBadLogin     = errors.New("wrong name or password")
	ErrLocked       = errors.New("too many failed logins, try again later")
	ErrNoSession    = errors.New("not signed in")
)

const minPassword = 10

type Service struct {
	Store      *store.Store
	Now        func() time.Time
	SessionTTL time.Duration
	Cost       int // bcrypt cost; 0 = default
	Limiter    *Limiter
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) NeedsSetup() (bool, error) {
	n, err := s.Store.CountUsers()
	return n == 0, err
}

func (s *Service) Setup(name, password string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("enter a name")
	}
	if len(password) < minPassword {
		return ErrWeakPassword
	}
	need, err := s.NeedsSetup()
	if err != nil {
		return err
	}
	if !need {
		return ErrSetupDone
	}
	cost := s.Cost
	if cost == 0 {
		cost = bcrypt.DefaultCost
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), cost)
	if err != nil {
		return err
	}
	_, err = s.Store.CreateUser(name, string(hash))
	return err
}

// dummyHash makes a login for an unknown name cost as much as a real one.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("dummy password"), bcrypt.MinCost)

func (s *Service) Login(name, password, remote string) (string, error) {
	if s.Limiter != nil && !s.Limiter.Allow(remote) {
		return "", ErrLocked
	}
	u, err := s.Store.GetUserByName(strings.TrimSpace(name))
	hash := dummyHash
	if err == nil {
		hash = []byte(u.PasswordHash)
	} else if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(password)) != nil || err != nil {
		if s.Limiter != nil {
			s.Limiter.Fail(remote)
		}
		return "", ErrBadLogin
	}
	if s.Limiter != nil {
		s.Limiter.Reset(remote)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	ttl := s.SessionTTL
	if ttl == 0 {
		ttl = 30 * 24 * time.Hour
	}
	if err := s.Store.CreateSession(hashToken(token), u.ID, s.now().Add(ttl)); err != nil {
		return "", err
	}
	_ = s.Store.DeleteExpiredSessions(s.now())
	return token, nil
}

func (s *Service) Authenticate(token string) (int64, error) {
	if token == "" {
		return 0, ErrNoSession
	}
	uid, err := s.Store.SessionUser(hashToken(token), s.now())
	if errors.Is(err, store.ErrNotFound) {
		return 0, ErrNoSession
	}
	return uid, err
}

func (s *Service) Logout(token string) error {
	if token == "" {
		return nil
	}
	return s.Store.DeleteSession(hashToken(token))
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Limiter counts failures per key inside a window.
type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time
	mu     sync.Mutex
	fails  map[string]*failure
}

type failure struct {
	count int
	first time.Time
}

func NewLimiter(max int, window time.Duration, now func() time.Time) *Limiter {
	return &Limiter{max: max, window: window, now: now, fails: map[string]*failure{}}
}

// Allow reports whether the key may try again.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[key]
	if !ok {
		return true
	}
	if l.now().Sub(f.first) >= l.window {
		delete(l.fails, key)
		return true
	}
	return f.count < l.max
}

func (l *Limiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f, ok := l.fails[key]
	if !ok || l.now().Sub(f.first) >= l.window {
		l.fails[key] = &failure{count: 1, first: l.now()}
		return
	}
	f.count++
}

func (l *Limiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go mod tidy && gofmt -l internal/auth; go vet ./internal/auth/ && go test -race -count=1 ./internal/auth/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/auth
git commit -m "admin login with sessions and lockout"
```

---

### Task 3: Notification senders

**Files:**
- Create: `internal/notify/notify.go`, `internal/notify/senders.go`, `internal/notify/email.go`
- Test: `internal/notify/notify_test.go`, `internal/notify/email_test.go`

**Interfaces:**
- Produces:
  - `notify.Message{Title, Body, URL string; Breaking bool}`
  - `notify.Sender` interface: `Send(ctx context.Context, m Message) error`
  - `notify.Field{Key, Label string; Secret, Required bool}` and `notify.TypeInfo{Type, Label string; Fields []Field}`
  - `notify.Types() []TypeInfo` (order: `ntfy`, `gotify`, `discord`, `telegram`, `webhook`, `email`)
  - `notify.Build(typ string, cfg map[string]string, client *http.Client) (Sender, error)`: error `missing <field label>` for a missing required field, `unknown notifier type "x"` otherwise
  - `notify.SecretKeys(typ string) map[string]bool`
  - `notify.SendTest(ctx context.Context, client *http.Client, n store.Notifier) error`: builds the sender and sends `Message{Title: "nextupdate test", Body: "This is a test message from nextupdate."}`

Field keys per type:
- `ntfy`: `url` (required), `topic` (required), `token` (secret)
- `gotify`: `url` (required), `token` (required, secret)
- `discord`: `webhook_url` (required, secret)
- `telegram`: `bot_token` (required, secret), `chat_id` (required), `api_base` (optional, default `https://api.telegram.org`)
- `webhook`: `url` (required)
- `email`: `host` (required), `port` (default `587`), `username`, `password` (secret), `from` (required), `to` (required)

Wire formats:
- ntfy: `POST {url}/{topic}`, body = `Body`, headers `Title`, `Click` (when `URL` set), `Priority: 4` when `Breaking`, `Authorization: Bearer <token>` when a token is set
- gotify: `POST {url}/message` with header `X-Gotify-Key: <token>`, JSON `{"title","message","priority"}`; priority 8 when breaking, else 5
- discord: `POST {webhook_url}` JSON `{"content": "<Title>\n<Body>\n<URL>"}` (empty parts left out)
- telegram: `POST {api_base}/bot{bot_token}/sendMessage` JSON `{"chat_id","text": "<Title>\n<Body>\n<URL>"}`
- webhook: `POST {url}` JSON `{"title","body","url","breaking"}`
- any 2xx is success; otherwise an error `"<type>: status <code>"` (never include the URL, it may hold a secret)

- [ ] **Step 1: Write the failing tests**

`internal/notify/notify_test.go`:

```go
package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

type capture struct {
	method, path, query string
	header              http.Header
	body                string
}

func server(t *testing.T, status int) (*httptest.Server, *capture) {
	t.Helper()
	c := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.method, c.path, c.query, c.header, c.body = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(b)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

var msg = Message{Title: "Update available: app", Body: "1.0.0 to 2.0.0", URL: "https://nu.example", Breaking: true}

func send(t *testing.T, typ string, cfg map[string]string) error {
	t.Helper()
	s, err := Build(typ, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s.Send(context.Background(), msg)
}

func TestNtfy(t *testing.T) {
	srv, c := server(t, 200)
	if err := send(t, "ntfy", map[string]string{"url": srv.URL + "/", "topic": "updates", "token": "tk"}); err != nil {
		t.Fatal(err)
	}
	if c.method != "POST" || c.path != "/updates" || c.body != "1.0.0 to 2.0.0" ||
		c.header.Get("Title") != "Update available: app" || c.header.Get("Click") != "https://nu.example" ||
		c.header.Get("Priority") != "4" || c.header.Get("Authorization") != "Bearer tk" {
		t.Fatalf("ntfy request: %+v", c)
	}
}

func TestGotify(t *testing.T) {
	srv, c := server(t, 200)
	if err := send(t, "gotify", map[string]string{"url": srv.URL, "token": "gt"}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal([]byte(c.body), &got)
	if c.path != "/message" || c.header.Get("X-Gotify-Key") != "gt" || got["title"] != "Update available: app" || got["priority"] != float64(8) {
		t.Fatalf("gotify request: %+v %v", c, got)
	}
}

func TestDiscordAndTelegramAndWebhook(t *testing.T) {
	srv, c := server(t, 204)
	if err := send(t, "discord", map[string]string{"webhook_url": srv.URL + "/hook"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.body, "Update available: app") || !strings.Contains(c.body, "https://nu.example") {
		t.Fatalf("discord body %s", c.body)
	}

	if err := send(t, "telegram", map[string]string{"bot_token": "123:abc", "chat_id": "42", "api_base": srv.URL}); err != nil {
		t.Fatal(err)
	}
	if c.path != "/bot123:abc/sendMessage" || !strings.Contains(c.body, `"chat_id":"42"`) {
		t.Fatalf("telegram: %s %s", c.path, c.body)
	}

	if err := send(t, "webhook", map[string]string{"url": srv.URL + "/wh"}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal([]byte(c.body), &got)
	if c.path != "/wh" || got["breaking"] != true || got["url"] != "https://nu.example" {
		t.Fatalf("webhook: %s %v", c.path, got)
	}
}

func TestSendErrorHidesTheURL(t *testing.T) {
	srv, _ := server(t, 500)
	err := send(t, "discord", map[string]string{"webhook_url": srv.URL + "/secret-token"})
	if err == nil || !strings.Contains(err.Error(), "status 500") || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error %v", err)
	}
}

func TestBuildValidates(t *testing.T) {
	if _, err := Build("ntfy", map[string]string{"url": "https://x"}, nil); err == nil || !strings.Contains(err.Error(), "Topic") {
		t.Fatalf("missing topic: %v", err)
	}
	if _, err := Build("carrier-pigeon", nil, nil); err == nil || !strings.Contains(err.Error(), "unknown notifier type") {
		t.Fatalf("unknown type: %v", err)
	}
}

func TestTypesAndSecrets(t *testing.T) {
	types := Types()
	if len(types) != 6 || types[0].Type != "ntfy" || types[5].Type != "email" {
		t.Fatalf("types %+v", types)
	}
	if !SecretKeys("ntfy")["token"] || SecretKeys("ntfy")["url"] || !SecretKeys("email")["password"] {
		t.Fatal("secret keys wrong")
	}
}

func TestSendTest(t *testing.T) {
	srv, c := server(t, 200)
	err := SendTest(context.Background(), nil, store.Notifier{Type: "ntfy", Config: map[string]string{"url": srv.URL, "topic": "t"}})
	if err != nil || c.header.Get("Title") != "nextupdate test" {
		t.Fatalf("%v %+v", err, c)
	}
}
```

`internal/notify/email_test.go`:

```go
package notify

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
)

// fakeSMTP accepts one message and returns what it received.
func fakeSMTP(t *testing.T) (addr string, got chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got = make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		w := func(s string) { conn.Write([]byte(s + "\r\n")) }
		w("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					w("250 queued")
					continue
				}
				data.WriteString(line + "\n")
				continue
			}
			switch cmd := strings.ToUpper(line); {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				w("250 fake")
			case strings.HasPrefix(cmd, "MAIL"), strings.HasPrefix(cmd, "RCPT"):
				w("250 ok")
			case cmd == "DATA":
				inData = true
				w("354 go ahead")
			case cmd == "QUIT":
				w("221 bye")
				got <- data.String()
				return
			default:
				w("250 ok")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestEmail(t *testing.T) {
	addr, got := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	s, err := Build("email", map[string]string{"host": host, "port": port, "from": "nextupdate@home.lan", "to": "jordi@example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), Message{Title: "Update available: app", Body: "1.0.0 to 2.0.0", URL: "https://nu.example"}); err != nil {
		t.Fatal(err)
	}
	mail := <-got
	for _, want := range []string{"From: nextupdate@home.lan", "To: jordi@example.com", "Subject: Update available: app", "1.0.0 to 2.0.0", "https://nu.example"} {
		if !strings.Contains(mail, want) {
			t.Errorf("mail lacks %q:\n%s", want, mail)
		}
	}
}

func TestEmailRejectsHeaderInjection(t *testing.T) {
	s, _ := Build("email", map[string]string{"host": "127.0.0.1", "port": "1", "from": "a@b.c", "to": "d@e.f"}, nil)
	err := s.Send(context.Background(), Message{Title: "hi\r\nBcc: evil@x.y", Body: "b"})
	if err == nil || !strings.Contains(err.Error(), "line break") {
		t.Fatalf("want a line-break error, got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/notify/`
Expected: FAIL, `undefined: Message`.

- [ ] **Step 3: Implement**

`internal/notify/notify.go`:

```go
// Package notify sends update messages to ntfy, Gotify, Discord, Telegram,
// webhooks and e-mail.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

type Message struct {
	Title    string
	Body     string
	URL      string
	Breaking bool
}

type Sender interface {
	Send(ctx context.Context, m Message) error
}

type Field struct {
	Key      string
	Label    string
	Secret   bool
	Required bool
}

type TypeInfo struct {
	Type   string
	Label  string
	Fields []Field
}

var types = []TypeInfo{
	{"ntfy", "ntfy", []Field{{"url", "Server URL", false, true}, {"topic", "Topic", false, true}, {"token", "Access token", true, false}}},
	{"gotify", "Gotify", []Field{{"url", "Server URL", false, true}, {"token", "App token", true, true}}},
	{"discord", "Discord", []Field{{"webhook_url", "Webhook URL", true, true}}},
	{"telegram", "Telegram", []Field{{"bot_token", "Bot token", true, true}, {"chat_id", "Chat ID", false, true}, {"api_base", "API base URL", false, false}}},
	{"webhook", "Webhook", []Field{{"url", "URL", false, true}}},
	{"email", "E-mail", []Field{{"host", "SMTP host", false, true}, {"port", "SMTP port", false, false}, {"username", "Username", false, false}, {"password", "Password", true, false}, {"from", "From address", false, true}, {"to", "To address", false, true}}},
}

func Types() []TypeInfo { return append([]TypeInfo(nil), types...) }

func typeInfo(typ string) (TypeInfo, bool) {
	for _, t := range types {
		if t.Type == typ {
			return t, true
		}
	}
	return TypeInfo{}, false
}

func SecretKeys(typ string) map[string]bool {
	out := map[string]bool{}
	if t, ok := typeInfo(typ); ok {
		for _, f := range t.Fields {
			if f.Secret {
				out[f.Key] = true
			}
		}
	}
	return out
}

func Build(typ string, cfg map[string]string, client *http.Client) (Sender, error) {
	info, ok := typeInfo(typ)
	if !ok {
		return nil, fmt.Errorf("unknown notifier type %q", typ)
	}
	for _, f := range info.Fields {
		if f.Required && strings.TrimSpace(cfg[f.Key]) == "" {
			return nil, fmt.Errorf("missing %s", f.Label)
		}
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	base := httpSender{typ: typ, cfg: cfg, client: client}
	if typ == "email" {
		return emailSender{cfg: cfg}, nil
	}
	return base, nil
}

func SendTest(ctx context.Context, client *http.Client, n store.Notifier) error {
	s, err := Build(n.Type, n.Config, client)
	if err != nil {
		return err
	}
	return s.Send(ctx, Message{Title: "nextupdate test", Body: "This is a test message from nextupdate."})
}

// text joins the non-empty parts of a message, one per line.
func (m Message) text() string {
	var parts []string
	for _, p := range []string{m.Title, m.Body, m.URL} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n")
}

func postJSON(ctx context.Context, client *http.Client, typ, url string, header map[string]string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if header == nil {
		header = map[string]string{}
	}
	header["Content-Type"] = "application/json"
	return do(ctx, client, typ, url, header, bytes.NewReader(b))
}

// do sends a POST. Errors never contain the URL, because it can hold a secret.
func do(ctx context.Context, client *http.Client, typ, url string, header map[string]string, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return fmt.Errorf("%s: invalid URL", typ)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: request failed", typ)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s: status %d", typ, resp.StatusCode)
	}
	return nil
}
```

`internal/notify/senders.go`:

```go
package notify

import (
	"context"
	"net/http"
	"strings"
	"strings"
)

type httpSender struct {
	typ    string
	cfg    map[string]string
	client *http.Client
}

func (s httpSender) Send(ctx context.Context, m Message) error {
	c := s.cfg
	switch s.typ {
	case "ntfy":
		h := map[string]string{"Title": m.Title}
		if m.URL != "" {
			h["Click"] = m.URL
		}
		if m.Breaking {
			h["Priority"] = "4"
		}
		if c["token"] != "" {
			h["Authorization"] = "Bearer " + c["token"]
		}
		return do(ctx, s.client, s.typ, strings.TrimRight(c["url"], "/")+"/"+strings.Trim(c["topic"], "/"), h, strings.NewReader(m.Body))
	case "gotify":
		prio := 5
		if m.Breaking {
			prio = 8
		}
		return postJSON(ctx, s.client, s.typ, strings.TrimRight(c["url"], "/")+"/message", map[string]string{"X-Gotify-Key": c["token"]},
			map[string]any{"title": m.Title, "message": m.Body, "priority": prio})
	case "discord":
		return postJSON(ctx, s.client, s.typ, c["webhook_url"], nil, map[string]string{"content": m.text()})
	case "telegram":
		base := strings.TrimRight(c["api_base"], "/")
		if base == "" {
			base = "https://api.telegram.org"
		}
		return postJSON(ctx, s.client, s.typ, base+"/bot"+c["bot_token"]+"/sendMessage", nil,
			map[string]string{"chat_id": c["chat_id"], "text": m.text()})
	default: // webhook
		return postJSON(ctx, s.client, s.typ, c["url"], nil,
			map[string]any{"title": m.Title, "body": m.Body, "url": m.URL, "breaking": m.Breaking})
	}
}
```

(Remove the duplicated `"strings"` import line: it must appear once.)

`internal/notify/email.go`:

```go
package notify

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

type emailSender struct{ cfg map[string]string }

func (s emailSender) Send(ctx context.Context, m Message) error {
	c := s.cfg
	for _, v := range []string{m.Title, c["from"], c["to"]} {
		if strings.ContainsAny(v, "\r\n") {
			return errors.New("email: a header contains a line break")
		}
	}
	port := c["port"]
	if port == "" {
		port = "587"
	}
	addr := net.JoinHostPort(c["host"], port)
	var auth smtp.Auth
	if c["username"] != "" {
		auth = smtp.PlainAuth("", c["username"], c["password"], c["host"])
	}
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nDate: %s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n",
		c["from"], c["to"], m.Title, time.Now().Format(time.RFC1123Z), strings.ReplaceAll(strings.TrimSpace(m.Body+"\n\n"+m.URL), "\n", "\r\n"))
	done := make(chan error, 1)
	go func() { done <- smtp.SendMail(addr, auth, c["from"], []string{c["to"]}, []byte(msg)) }()
	select {
	case err := <-done:
		if err != nil {
			return errors.New("email: could not send the message")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(30 * time.Second):
		return errors.New("email: timed out")
	}
}
```

- [ ] **Step 4: Run to verify they pass**

Run: `gofmt -l internal/notify; go vet ./internal/notify/ && go test -race -count=1 ./internal/notify/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/notify
git commit -m "notification senders: ntfy, gotify, discord, telegram, webhook, email"
```

---

### Task 4: Web Push

**Files:**
- Create: `internal/push/push.go`
- Test: `internal/push/push_test.go`

**Interfaces:**
- Consumes: `store.PushSub`, `store.Store` settings.
- Produces:
  - `push.Keys{Public, Private string}`; `push.EnsureKeys(st *store.Store) (Keys, error)` (reads settings `vapid_public` and `vapid_private`, generates and stores them on first use)
  - `push.Sender{Keys Keys; Subject string; Client *http.Client}`; `(*Sender).Send(ctx context.Context, sub store.PushSub, payload []byte) error`
  - `push.ErrGone` returned for HTTP 404 and 410 (the subscription no longer exists)

- [ ] **Step 1: Write the failing tests**

`internal/push/push_test.go`:

```go
package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

func newSub(t *testing.T, endpoint string) store.PushSub {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	rand.Read(authSecret)
	return store.PushSub{
		Endpoint: endpoint,
		P256dh:   base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(authSecret),
	}
}

func TestEnsureKeysCreatesOnceAndReuses(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := EnsureKeys(st)
	if err != nil || a.Public == "" || a.Private == "" {
		t.Fatalf("first: %+v %v", a, err)
	}
	b, err := EnsureKeys(st)
	if err != nil || a != b {
		t.Fatalf("second call must return the same keys: %+v vs %+v (%v)", a, b, err)
	}
}

func TestSendPostsAnEncryptedMessage(t *testing.T) {
	var gotAuth, gotEnc, gotTTL string
	var gotLen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotEnc, gotTTL = r.Header.Get("Authorization"), r.Header.Get("Content-Encoding"), r.Header.Get("TTL")
		buf := make([]byte, 8192)
		gotLen, _ = r.Body.Read(buf)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	st, _ := store.Open(filepath.Join(t.TempDir(), "p.db"))
	defer st.Close()
	keys, _ := EnsureKeys(st)
	s := &Sender{Keys: keys, Subject: "mailto:admin@example.com"}

	if err := s.Send(context.Background(), newSub(t, srv.URL+"/push/1"), []byte(`{"title":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	if gotAuth == "" || gotEnc != "aes128gcm" || gotTTL == "" || gotLen < 20 {
		t.Fatalf("auth %q enc %q ttl %q len %d", gotAuth, gotEnc, gotTTL, gotLen)
	}
}

func TestSendReportsGoneSubscriptions(t *testing.T) {
	for _, status := range []int{http.StatusGone, http.StatusNotFound} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		st, _ := store.Open(filepath.Join(t.TempDir(), "p.db"))
		keys, _ := EnsureKeys(st)
		err := (&Sender{Keys: keys, Subject: "mailto:a@b.c"}).Send(context.Background(), newSub(t, srv.URL), []byte("x"))
		srv.Close()
		st.Close()
		if !errors.Is(err, ErrGone) {
			t.Errorf("status %d: want ErrGone, got %v", status, err)
		}
	}
}

func TestSendFailsOnServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	st, _ := store.Open(filepath.Join(t.TempDir(), "p.db"))
	defer st.Close()
	keys, _ := EnsureKeys(st)
	err := (&Sender{Keys: keys, Subject: "mailto:a@b.c"}).Send(context.Background(), newSub(t, srv.URL), []byte("x"))
	if err == nil || errors.Is(err, ErrGone) {
		t.Fatalf("want a plain failure, got %v", err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go get github.com/SherClockHolmes/webpush-go && go test ./internal/push/`
Expected: FAIL, `undefined: EnsureKeys`.

- [ ] **Step 3: Implement**

`internal/push/push.go`:

```go
// Package push sends Web Push messages to subscribed browsers.
package push

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

// ErrGone means the browser removed the subscription; delete it.
var ErrGone = errors.New("push subscription is gone")

type Keys struct {
	Public  string
	Private string
}

// EnsureKeys returns the VAPID key pair, creating and storing it on first use.
func EnsureKeys(st *store.Store) (Keys, error) {
	pub, err := st.GetSetting("vapid_public")
	if err != nil {
		return Keys{}, err
	}
	priv, err := st.GetSetting("vapid_private")
	if err != nil {
		return Keys{}, err
	}
	if pub != "" && priv != "" {
		return Keys{Public: pub, Private: priv}, nil
	}
	priv, pub, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return Keys{}, fmt.Errorf("generate VAPID keys: %w", err)
	}
	if err := st.SetSetting("vapid_public", pub); err != nil {
		return Keys{}, err
	}
	if err := st.SetSetting("vapid_private", priv); err != nil {
		return Keys{}, err
	}
	return Keys{Public: pub, Private: priv}, nil
}

type Sender struct {
	Keys    Keys
	Subject string // "mailto:..." or an https URL that identifies this server
	Client  *http.Client
}

func (s *Sender) Send(ctx context.Context, sub store.PushSub, payload []byte) error {
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &webpush.Options{
		Subscriber:      s.Subject,
		VAPIDPublicKey:  s.Keys.Public,
		VAPIDPrivateKey: s.Keys.Private,
		TTL:             24 * 60 * 60,
		HTTPClient:      s.Client,
	})
	if err != nil {
		return errors.New("push: request failed")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound:
		return ErrGone
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("push: status %d", resp.StatusCode)
	}
	return nil
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go mod tidy && gofmt -l internal/push; go vet ./internal/push/ && go test -race -count=1 ./internal/push/`
Expected: `ok`. If `HTTPClient: nil` is not accepted by the library version, set `s.Client` to `http.DefaultClient` when nil before the call.

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum internal/push
git commit -m "web push with vapid keys"
```

---

### Task 5: Dispatcher

**Files:**
- Create: `internal/notify/dispatcher.go`
- Test: `internal/notify/dispatcher_test.go`

**Interfaces:**
- Consumes: `scheduler.Event`, `store`, `push.Sender`, `notify.Build`.
- Produces:
  - `notify.Format(ev scheduler.Event, baseURL string) Message`
  - `notify.Dispatcher{Store *store.Store; Push *push.Sender; Client *http.Client; Log *log.Logger; BaseURL string}` implementing `scheduler.Notifier`: sends to every enabled notifier and every Web Push subscription; one failure never stops the rest; a `push.ErrGone` deletes that subscription

`Format` rules (English, plain):
- `update_available`: title `Update available: <container>` plus ` (<old> to <new>)` when a version is known, prefixed `Breaking update available` instead of `Update available` when `Breaking`; body = `Detail` and the reasons, one per line
- `update_ok`: `Updated <container>` plus ` to <new>` when known; body empty
- `update_rolled_back`: `Update of <container> was rolled back`; body = `Detail`
- `update_failed`: `Update of <container> failed`; body = `Detail`
- `URL` = `baseURL`; `Breaking` is copied

- [ ] **Step 1: Write the failing tests**

`internal/notify/dispatcher_test.go`:

```go
package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/push"
	"github.com/jordibrouwer/nextupdate/internal/scheduler"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

func TestFormat(t *testing.T) {
	cases := []struct {
		ev        scheduler.Event
		title     string
		bodyHas   string
		breaking  bool
	}{
		{scheduler.Event{Kind: scheduler.KindAvailable, Container: "app", OldVersion: "1.0.0", NewVersion: "1.1.0"}, "Update available: app (1.0.0 to 1.1.0)", "", false},
		{scheduler.Event{Kind: scheduler.KindAvailable, Container: "app", OldVersion: "1.0.0", NewVersion: "2.0.0", Breaking: true, Reasons: []string{"Major version change from 1.0.0 to 2.0.0."}, Detail: "The policy for this container is notify."}, "Breaking update available: app (1.0.0 to 2.0.0)", "Major version change", true},
		{scheduler.Event{Kind: scheduler.KindAvailable, Container: "app"}, "Update available: app", "", false},
		{scheduler.Event{Kind: scheduler.KindUpdated, Container: "app", NewVersion: "1.1.0"}, "Updated app to 1.1.0", "", false},
		{scheduler.Event{Kind: scheduler.KindRolledBack, Container: "app", Detail: "verify: healthcheck unhealthy"}, "Update of app was rolled back", "healthcheck unhealthy", false},
		{scheduler.Event{Kind: scheduler.KindFailed, Container: "app", Detail: "docker unreachable"}, "Update of app failed", "docker unreachable", false},
	}
	for _, c := range cases {
		m := Format(c.ev, "https://nu.example")
		if m.Title != c.title || !strings.Contains(m.Body, c.bodyHas) || m.URL != "https://nu.example" || m.Breaking != c.breaking {
			t.Errorf("%s: got %+v", c.ev.Kind, m)
		}
	}
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestDispatcherFansOutAndSurvivesFailures(t *testing.T) {
	st := newStore(t)
	var mu sync.Mutex
	var hits []string
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits = append(hits, r.URL.Path+" "+string(b))
		mu.Unlock()
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()

	st.AddNotifier(store.Notifier{Name: "broken", Type: "webhook", Config: map[string]string{"url": bad.URL}, Enabled: true})
	st.AddNotifier(store.Notifier{Name: "off", Type: "webhook", Config: map[string]string{"url": good.URL + "/off"}, Enabled: false})
	st.AddNotifier(store.Notifier{Name: "ok", Type: "webhook", Config: map[string]string{"url": good.URL + "/ok"}, Enabled: true})

	var logs strings.Builder
	d := &Dispatcher{Store: st, BaseURL: "https://nu.example", Log: newLogger(&logs)}
	d.Notify(context.Background(), scheduler.Event{Kind: scheduler.KindUpdated, Container: "app", NewVersion: "1.1.0"})

	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 1 || !strings.HasPrefix(hits[0], "/ok ") {
		t.Fatalf("only the enabled, working notifier should receive it: %v", hits)
	}
	var body map[string]any
	json.Unmarshal([]byte(strings.TrimPrefix(hits[0], "/ok ")), &body)
	if body["title"] != "Updated app to 1.1.0" {
		t.Fatalf("body %v", body)
	}
	if !strings.Contains(logs.String(), "broken") || strings.Contains(logs.String(), bad.URL) {
		t.Fatalf("the failure must be logged by name, without the URL: %q", logs.String())
	}
}

func TestDispatcherPushesAndDropsGoneSubscriptions(t *testing.T) {
	st := newStore(t)
	keys, _ := push.EnsureKeys(st)
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) }))
	defer live.Close()
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(410) }))
	defer gone.Close()

	sub := func(endpoint string) store.PushSub {
		s := newPushSub(t, endpoint)
		st.AddPushSub(s)
		return s
	}
	sub(live.URL + "/a")
	sub(gone.URL + "/b")

	d := &Dispatcher{Store: st, Push: &push.Sender{Keys: keys, Subject: "mailto:a@b.c"}, BaseURL: "https://nu.example", Log: newLogger(&strings.Builder{})}
	d.Notify(context.Background(), scheduler.Event{Kind: scheduler.KindAvailable, Container: "app"})

	left, _ := st.ListPushSubs()
	if len(left) != 1 || !strings.HasPrefix(left[0].Endpoint, live.URL) {
		t.Fatalf("the gone subscription must be deleted, the live one kept: %+v", left)
	}
}
```

Add a test helper file `internal/notify/helpers_test.go`:

```go
package notify

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"log"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

func newLogger(w io.Writer) *log.Logger { return log.New(w, "", 0) }

func newPushSub(t *testing.T, endpoint string) store.PushSub {
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/notify/`
Expected: FAIL, `undefined: Format`, `undefined: Dispatcher`.

- [ ] **Step 3: Implement**

`internal/notify/dispatcher.go`:

```go
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/push"
	"github.com/jordibrouwer/nextupdate/internal/scheduler"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

// Format turns a scheduler event into a message.
func Format(ev scheduler.Event, baseURL string) Message {
	versions := ""
	if ev.OldVersion != "" && ev.NewVersion != "" {
		versions = " (" + ev.OldVersion + " to " + ev.NewVersion + ")"
	}
	m := Message{URL: baseURL, Breaking: ev.Breaking}
	var lines []string
	switch ev.Kind {
	case scheduler.KindAvailable:
		head := "Update available"
		if ev.Breaking {
			head = "Breaking update available"
		}
		m.Title = head + ": " + ev.Container + versions
		lines = append(lines, ev.Detail)
		lines = append(lines, ev.Reasons...)
	case scheduler.KindUpdated:
		m.Title = "Updated " + ev.Container
		if ev.NewVersion != "" {
			m.Title += " to " + ev.NewVersion
		}
	case scheduler.KindRolledBack:
		m.Title = "Update of " + ev.Container + " was rolled back"
		lines = append(lines, ev.Detail)
	default:
		m.Title = "Update of " + ev.Container + " failed"
		lines = append(lines, ev.Detail)
	}
	var kept []string
	for _, l := range lines {
		if l != "" {
			kept = append(kept, l)
		}
	}
	m.Body = strings.Join(kept, "\n")
	return m
}

// Dispatcher sends events to every enabled notifier and every push
// subscription. It implements scheduler.Notifier.
type Dispatcher struct {
	Store   *store.Store
	Push    *push.Sender // nil disables Web Push
	Client  *http.Client
	Log     *log.Logger
	BaseURL string
}

var _ scheduler.Notifier = (*Dispatcher)(nil)

func (d *Dispatcher) logf(format string, a ...any) {
	l := d.Log
	if l == nil {
		l = log.Default()
	}
	l.Printf(format, a...)
}

func (d *Dispatcher) Notify(ctx context.Context, ev scheduler.Event) {
	msg := Format(ev, d.BaseURL)

	notifiers, err := d.Store.ListNotifiers()
	if err != nil {
		d.logf("notify: list notifiers: %v", err)
	}
	for _, n := range notifiers {
		if !n.Enabled {
			continue
		}
		s, err := Build(n.Type, n.Config, d.Client)
		if err != nil {
			d.logf("notify: notifier %q: %v", n.Name, err)
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := s.Send(cctx, msg); err != nil {
			d.logf("notify: notifier %q: %v", n.Name, err)
		}
		cancel()
	}

	if d.Push == nil {
		return
	}
	subs, err := d.Store.ListPushSubs()
	if err != nil {
		d.logf("notify: list push subscriptions: %v", err)
		return
	}
	payload, _ := json.Marshal(map[string]any{"title": msg.Title, "body": msg.Body, "url": msg.URL, "container": ev.Container, "kind": ev.Kind})
	for _, sub := range subs {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := d.Push.Send(cctx, sub, payload)
		cancel()
		switch {
		case errors.Is(err, push.ErrGone):
			if err := d.Store.DeletePushSub(sub.Endpoint); err != nil {
				d.logf("notify: delete push subscription: %v", err)
			}
		case err != nil:
			d.logf("notify: push: %v", err)
		}
	}
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `gofmt -l internal/notify; go vet ./... && go test -race -count=1 ./internal/notify/`
Expected: `ok`. `notify` imports `scheduler` and `push`; neither imports `notify`, so there is no cycle.

- [ ] **Step 5: Commit**

```bash
git add internal/notify
git commit -m "dispatcher: fan events out to notifiers and web push"
```

---

### Task 6: Manual rollback and engine helpers

**Files:**
- Modify: `internal/updater/run.go`, `internal/updater/compose.go`, `internal/updater/run_test.go`, `internal/updater/compose_test.go`, `internal/engine/engine.go`, `internal/engine/engine_test.go`

**Interfaces:**
- Produces:
  - `updater.Run.SkipPull bool` and `updater.Compose.SkipPull bool`: when set, the adapter does not pull (the image to run is already local)
  - `engine.ErrNoRollback` (wrapped with a plain sentence)
  - `engine.Engine.RollbackRun` and `RollbackCompose updater.Adapter` (adapters built with `SkipPull: true`)
  - `(*Engine).Containers(ctx context.Context) ([]discovery.Container, error)`
  - `(*Engine).Rollback(ctx context.Context, name string) (store.History, error)`

Behaviour of `Rollback`: holds the update lock; refuses the engine's own container (`ErrSelfUpdate`); finds the newest history entry of that container with outcome `ok`, a `FromImage` and `FromImage != ToImage` (none: `ErrNoRollback`); the previous image must still exist locally (else `ErrNoRollback` with "the previous image was removed"); tags `FromImage` as the container's image reference; runs the matching rollback adapter; records a history entry whose `Reason` starts with `Manual rollback.`; on success and with `Retention > 0` it registers the image it rolled away from as an old image.

- [ ] **Step 1: Write the failing tests**

Append to `internal/updater/run_test.go`:

```go
func TestRunSkipPull(t *testing.T) {
	f, c := runFixture(t)
	f.Images["app:latest"] = docker.ImageJSON{ID: "sha256:target"} // the tag already points at the target
	f.Images["sha256:target"] = docker.ImageJSON{ID: "sha256:target"}
	res := (&Run{API: f, Journal: testJournal(t), Verify: verifier(true), SkipPull: true}).Update(context.Background(), c)
	if res.Outcome != OutcomeOK || res.ToImage != "sha256:target" {
		t.Fatalf("res %+v", res)
	}
	for _, call := range f.Calls {
		if strings.HasPrefix(call, "pull ") {
			t.Fatalf("SkipPull must not pull: %v", f.Calls)
		}
	}
}
```

Append to `internal/updater/compose_test.go`:

```go
func TestComposeSkipPull(t *testing.T) {
	f, runner, c := composeFixture(t)
	f.Images["web:latest"] = docker.ImageJSON{ID: "sha256:target"}
	f.Images["sha256:target"] = docker.ImageJSON{ID: "sha256:target"}
	res := (&Compose{API: f, Runner: runner, Journal: testJournal(t), Verify: verifier(true), SkipPull: true}).Update(context.Background(), c)
	if res.Outcome != OutcomeOK {
		t.Fatalf("res %+v", res)
	}
	for _, call := range runner.calls {
		if slices.Contains(call, "pull") {
			t.Fatalf("SkipPull must not run compose pull: %v", runner.calls)
		}
	}
}
```

Append to `internal/engine/engine_test.go`:

```go
func TestRollback(t *testing.T) {
	e, _, _ := newEngine(t)
	f := e.API.(*dockertest.Fake)
	rbRun := &fakeAdapter{res: updater.Result{Outcome: updater.OutcomeOK, FromImage: "sha256:b", ToImage: "sha256:a"}}
	e.RollbackRun = rbRun
	e.Retention = time.Hour
	ctx := context.Background()

	if _, err := e.Rollback(ctx, "app"); !errors.Is(err, ErrNoRollback) {
		t.Fatalf("no history yet: %v", err)
	}
	if _, err := e.Update(ctx, "app"); err != nil { // fakeAdapter: ok, sha256:a to sha256:b
		t.Fatal(err)
	}
	f.Images["app:latest"] = docker.ImageJSON{ID: "sha256:b"} // the update moved the tag

	h, err := e.Rollback(ctx, "app")
	if err != nil || h.Outcome != updater.OutcomeOK || len(rbRun.seen) != 1 {
		t.Fatalf("rollback: %+v %v", h, err)
	}
	if f.Images["app:latest"].ID != "sha256:a" {
		t.Fatalf("the previous image must be tagged again, tag points to %s", f.Images["app:latest"].ID)
	}
	if !strings.HasPrefix(h.Reason, "Manual rollback.") {
		t.Fatalf("reason %q", h.Reason)
	}
	due, _ := e.Store.DueOldImages(time.Now().Add(2 * time.Hour))
	found := false
	for _, o := range due {
		found = found || o.ImageID == "sha256:b"
	}
	if !found {
		t.Fatalf("the image rolled away from must be tracked for cleanup: %+v", due)
	}
}

func TestRollbackNeedsThePreviousImage(t *testing.T) {
	e, _, _ := newEngine(t)
	f := e.API.(*dockertest.Fake)
	e.RollbackRun = &fakeAdapter{}
	if _, err := e.Update(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
	// sha256:a is only referenced by containers; remove it as cleanup would
	for k, img := range f.Images {
		if img.ID == "sha256:a" {
			delete(f.Images, k)
		}
	}
	_, err := e.Rollback(context.Background(), "app")
	if !errors.Is(err, ErrNoRollback) || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("want ErrNoRollback about a removed image, got %v", err)
	}
}

func TestRollbackRefusesSelf(t *testing.T) {
	e, _, _ := newEngine(t)
	e.Self = "aaaaaaaaaaaa"
	e.Update(context.Background(), "web") // records history for another container so the lookup order does not matter
	if _, err := e.Rollback(context.Background(), "app"); !errors.Is(err, ErrSelfUpdate) {
		t.Fatalf("want ErrSelfUpdate, got %v", err)
	}
}

func TestContainers(t *testing.T) {
	e, _, _ := newEngine(t)
	list, err := e.Containers(context.Background())
	if err != nil || len(list) != 5 {
		t.Fatalf("got %d containers, %v", len(list), err)
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/updater/ ./internal/engine/`
Expected: FAIL, `unknown field SkipPull`, `e.Rollback undefined`.

- [ ] **Step 3: Implement**

`internal/updater/run.go`: add a field to `Run` and guard the pull:

```go
type Run struct {
	API      docker.API
	Journal  Journal
	Verify   Verifier
	SkipPull bool // the image is already local (manual rollback)
}
```

Replace the pull block in `Update`:

```go
	if !r.SkipPull {
		logf("pull %s", c.Image)
		if err := r.API.PullImage(ctx, c.Image); err != nil {
			r.Journal.Close(jid)
			return fail("pull: " + err.Error())
		}
	}
```

`internal/updater/compose.go`: add `SkipPull bool` to `Compose` and guard the pull command:

```go
	if !cp.SkipPull {
		logf("compose pull %s", c.ComposeService)
		if out, err := cp.Runner.Run(ctx, c.ComposeWorkdir, args("pull", c.ComposeService)...); err != nil {
			cp.Journal.Close(jid)
			return fail(fmt.Sprintf("compose pull: %v: %s", err, out))
		}
	}
```

In `Compose` with `SkipPull`, the `up` command must not pull either; change the `up` args to include `"--pull", "never"` when `SkipPull` is set:

```go
	upArgs := []string{"up", "-d", "--no-deps"}
	if cp.SkipPull {
		upArgs = append(upArgs, "--pull", "never")
	}
	upArgs = append(upArgs, c.ComposeService)
	logf("compose up %s", c.ComposeService)
	if out, err := cp.Runner.Run(rctx, c.ComposeWorkdir, args(upArgs...)...); err != nil {
```

(replacing the existing `args("up", "-d", "--no-deps", c.ComposeService)` call.)

`internal/engine/engine.go`: add the fields, error and methods:

```go
var ErrNoRollback = errors.New("there is nothing to roll back to")
```

fields on `Engine`:

```go
	RollbackRun     updater.Adapter // Run adapter with SkipPull
	RollbackCompose updater.Adapter // Compose adapter with SkipPull
```

methods (add `"github.com/jordibrouwer/nextupdate/internal/docker"` is already imported):

```go
func (e *Engine) Containers(ctx context.Context) ([]discovery.Container, error) {
	return discovery.Discover(ctx, e.API)
}

// Rollback puts the previous image back. It keeps the previous image on
// disk for Retention, so this works until Cleanup has removed it.
func (e *Engine) Rollback(ctx context.Context, name string) (store.History, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

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
	hist, err := e.Store.ListHistory(200)
	if err != nil {
		return store.History{}, err
	}
	var prev *store.History
	for i := range hist {
		h := hist[i]
		if h.Container == name && h.Outcome == updater.OutcomeOK && h.FromImage != "" && h.FromImage != h.ToImage {
			prev = &hist[i]
			break
		}
	}
	if prev == nil {
		return store.History{}, fmt.Errorf("%w: no earlier update of %s is recorded", ErrNoRollback, name)
	}
	if _, err := e.API.InspectImage(ctx, prev.FromImage); errors.Is(err, docker.ErrNotFound) {
		return store.History{}, fmt.Errorf("%w: the previous image was removed", ErrNoRollback)
	} else if err != nil {
		return store.History{}, err
	}
	repo, tag := docker.SplitRef(target.Image)
	if err := e.API.TagImage(ctx, prev.FromImage, repo, tag); err != nil {
		return store.History{}, fmt.Errorf("tag previous image: %w", err)
	}
	adapter := e.RollbackRun
	if target.Source == discovery.SourceCompose {
		adapter = e.RollbackCompose
	}
	started := e.now()
	res := adapter.Update(ctx, *target)
	reason := "Manual rollback."
	if res.Reason != "" {
		reason += " " + res.Reason
	}
	h := store.History{
		Container: name, Image: target.Image, FromImage: res.FromImage, ToImage: res.ToImage,
		StartedAt: started, FinishedAt: e.now(), Outcome: res.Outcome, Reason: reason, Log: res.Log,
	}
	if h.ID, err = e.Store.AddHistory(h); err != nil {
		return h, err
	}
	if res.Outcome == updater.OutcomeOK && e.Retention > 0 && res.FromImage != "" && res.FromImage != res.ToImage {
		if err := e.Store.AddOldImage(store.OldImage{ImageID: res.FromImage, Container: name, RemoveAfter: e.now().Add(e.Retention)}); err != nil {
			return h, err
		}
	}
	return h, nil
}
```

`cmd/nextupdate/main.go`: build the rollback adapters next to the normal ones:

```go
		RollbackRun:     &updater.Run{API: api, Journal: journal, Verify: verifier, SkipPull: true},
		RollbackCompose: &updater.Compose{API: api, Runner: runner, Journal: journal, Verify: verifier, SkipPull: true},
```

- [ ] **Step 4: Run to verify it passes**

Run: `gofmt -l . ; go vet ./... && go vet -tags integration ./test/... && go test -race -count=1 ./...`
Expected: all `ok`.

- [ ] **Step 5: Commit**

```bash
git add internal cmd
git commit -m "manual rollback to the previous image"
```

---

### Task 7: API server, middleware and login endpoints

**Files:**
- Modify: `internal/store/auth.go`, `internal/store/auth_test.go`
- Create: `internal/api/api.go`, `internal/api/jobs.go`, `internal/api/updates.go` (stub), `internal/api/notifiers.go` (stub)
- Test: `internal/api/api_test.go`

**Interfaces:**
- Consumes: `auth.Service`, `store`, `engine` methods (through an interface), `changelog`, `push`, `notify`.
- Produces:
  - `api.Engine` interface: `Check(ctx) ([]store.Available, error)`, `Update(ctx, name string) (store.History, error)`, `Rollback(ctx, name string) (store.History, error)`, `Containers(ctx) ([]discovery.Container, error)`
  - `api.Deps{Store *store.Store; Auth *auth.Service; Engine Engine; Changelog changelog.Source; Mappings *changelog.Mappings; Push *push.Sender; BaseURL string; Client *http.Client; Log *log.Logger; Version string; BaseCtx context.Context}`
  - `api.New(d Deps) *Server`; `(*Server).ServeHTTP`; `(*Server).Wait()` blocks until running jobs finish (for tests and shutdown)
  - `api.Jobs`: `Start(ctx context.Context, key, kind string, fn func(context.Context) error) bool` (false when that key is already running), `Running() []Job`, `Recent() []Failure`; `Job{Key, Kind string; Since time.Time}`, `Failure{Key, Kind, Error string; At time.Time}`
  - Endpoints in this task: `GET /api/status`, `POST /api/setup`, `POST /api/login`, `POST /api/logout`, `GET /api/me`
  - Helpers used by later tasks: `(*Server).protected(h http.HandlerFunc) http.HandlerFunc` (login and CSRF check), `writeJSON(w, status, v)`, `writeError(w, status, msg)`, `readJSON(r, v) error`

Request and response shapes:
- `GET /api/status` (no login needed): `{"version": "...", "setupNeeded": bool, "signedIn": bool}`
- `POST /api/setup` `{"name","password"}` → `201 {"ok":true}` and a session cookie; `409` when an account exists; `400` for a weak password
- `POST /api/login` `{"name","password"}` → `200 {"ok":true}` and a cookie; `401` `wrong name or password`; `429` when locked
- `POST /api/logout` → `200 {"ok":true}`, cookie cleared (works without a session)
- `GET /api/me` → `200 {"name": "..."}`; `401` without a session (the user name comes from a new `Store.GetUserByID`, added in Step 1)

- [ ] **Step 1: Add `GetUserByID` and write the failing tests**

Append to `internal/store/auth.go`:

```go
func (s *Store) GetUserByID(id int64) (User, error) {
	var u User
	err := s.db.QueryRow(`SELECT id, name, password_hash FROM users WHERE id = ?`, id).Scan(&u.ID, &u.Name, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}
```

Append to `internal/store/auth_test.go`:

```go
func TestGetUserByID(t *testing.T) {
	s := openTest(t)
	u, _ := s.CreateUser("jordi", "h")
	got, err := s.GetUserByID(u.ID)
	if err != nil || got.Name != "jordi" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.GetUserByID(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
```

`internal/api/api_test.go` (the shared harness lives here; later test files reuse it):

```go
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
```

Append the session tests:

```go
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
	for _, p := range [][2]string{{"GET", "/api/updates"}, {"GET", "/api/containers"}, {"GET", "/api/history"}, {"POST", "/api/check"}} {
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/api/ ./internal/store/`
Expected: FAIL, `undefined: New`, `undefined: Jobs`, `GetUserByID` undefined in the store test build until Step 1 is applied.

- [ ] **Step 3: Implement jobs**

`internal/api/jobs.go`:

```go
package api

import (
	"context"
	"sync"
	"time"
)

type Job struct {
	Key   string    `json:"key"`
	Kind  string    `json:"kind"`
	Since time.Time `json:"since"`
}

type Failure struct {
	Key   string    `json:"key"`
	Kind  string    `json:"kind"`
	Error string    `json:"error"`
	At    time.Time `json:"at"`
}

// Jobs runs background work, one job per key at a time, and remembers the
// last failures so the UI can show why something did not happen.
type Jobs struct {
	mu      sync.Mutex
	running map[string]Job
	failed  []Failure
	wg      sync.WaitGroup
}

// Start runs fn in the background. It returns false when a job with the same
// key is already running.
func (j *Jobs) Start(ctx context.Context, key, kind string, fn func(context.Context) error) bool {
	j.mu.Lock()
	if j.running == nil {
		j.running = map[string]Job{}
	}
	if _, busy := j.running[key]; busy {
		j.mu.Unlock()
		return false
	}
	j.running[key] = Job{Key: key, Kind: kind, Since: time.Now()}
	j.wg.Add(1)
	j.mu.Unlock()

	go func() {
		defer j.wg.Done()
		err := fn(ctx)
		j.mu.Lock()
		defer j.mu.Unlock()
		delete(j.running, key)
		if err != nil {
			j.failed = append(j.failed, Failure{Key: key, Kind: kind, Error: err.Error(), At: time.Now()})
			if len(j.failed) > 20 {
				j.failed = j.failed[len(j.failed)-20:]
			}
		}
	}()
	return true
}

func (j *Jobs) Running() []Job {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := make([]Job, 0, len(j.running))
	for _, r := range j.running {
		out = append(out, r)
	}
	return out
}

func (j *Jobs) Recent() []Failure {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Failure(nil), j.failed...)
}

func (j *Jobs) Wait() { j.wg.Wait() }
```

- [ ] **Step 4: Implement the server**

`internal/api/api.go`:

```go
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
```

Add temporary stubs so the package compiles until Tasks 8-10 replace them. Create `internal/api/updates.go` with:

```go
package api

func (s *Server) registerUpdates() {}
```

and `internal/api/notifiers.go` with:

```go
package api

func (s *Server) registerNotifiers() {}
```

`TestProtectedRoutesNeedLogin` passes only once the routes exist; keep that test but expect it to fail (404) until Task 8. To keep every commit green, change the test now to check only the routes that exist, and extend it in Task 8:

```go
	for _, p := range [][2]string{{"GET", "/api/me"}} {
```

- [ ] **Step 5: Run to verify it passes**

Run: `gofmt -l . ; go vet ./... && go test -race -count=1 ./internal/api/ ./internal/store/`
Expected: `ok`

- [ ] **Step 6: Commit**

```bash
git add internal
git commit -m "api server: login, sessions, csrf header, background jobs"
```

---

### Task 8: Read endpoints and actions

**Files:**
- Modify: `internal/api/updates.go`, `internal/api/api_test.go` (extend `TestProtectedRoutesNeedLogin`)
- Test: `internal/api/updates_test.go`

**Interfaces:**
- Consumes: Task 7.
- Produces (all need a login; writes need the CSRF header):
  - `GET /api/updates` → `[{"container","image","oldVersion","newVersion","repo","breaking","reasons":[],"policy","detectedAt","busy"}]`, breaking updates first, then by container name
  - `GET /api/containers` → `[{"name","image","source","policy","httpUrl","repo","verifyWindowSeconds","updateAvailable","busy"}]` sorted by name
  - `GET /api/updates/{name}/changelog` → `{"repo","source","oldVersion","newVersion","releases":[{"tag","name","body","url","publishedAt"}]}`; `404` when no update is available for that container; an empty `releases` list and empty `repo` when no repo is known
  - `GET /api/history?limit=N` (default 50, max 200) → `[{"id","container","image","fromImage","toImage","startedAt","finishedAt","outcome","reason","log":[]}]`, newest first
  - `GET /api/jobs` → `{"running":[Job],"failed":[Failure]}`
  - `POST /api/check` → `202 {"started":true}`; `409` when a check is already running
  - `POST /api/updates/{name}/apply` → `202 {"started":true}`; `404` for an unknown container; `409` when a job for that container is running
  - `POST /api/containers/{name}/rollback` → same codes as apply
  - `PUT /api/containers/{name}/settings` body `{"policy","httpUrl","repo","verifyWindowSeconds"}` → `200` with the saved settings; `400` for an invalid policy, a repo that is not `owner/name`, an HTTP URL that is not `http(s)://`, or a negative window
- `busy` is true while a job for that container runs. Job keys: `"check"` for the check, the container name for update and rollback.

- [ ] **Step 1: Write the failing tests**

`internal/api/updates_test.go`:

```go
package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/changelog"
	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

func seedUpdates(t *testing.T, h *harness) {
	t.Helper()
	now := time.Now()
	h.st.ReplaceAvailable([]store.Available{
		{Container: "sonarr", Image: "linuxserver/sonarr:latest", LocalDigest: "sha256:1", RemoteDigest: "sha256:2", DetectedAt: now},
		{Container: "immich", Image: "ghcr.io/immich-app/immich-server:release", LocalDigest: "sha256:3", RemoteDigest: "sha256:4", DetectedAt: now},
	})
	h.st.ReplaceInfo([]store.Info{
		{Container: "sonarr", OldVersion: "4.0.9", NewVersion: "4.0.10", Repo: "Sonarr/Sonarr"},
		{Container: "immich", OldVersion: "1.98.0", NewVersion: "2.0.0", Repo: "immich-app/immich", Breaking: true, Reasons: []string{"Major version change from 1.98.0 to 2.0.0."}},
	})
	h.st.SetSettings(store.Settings{Container: "sonarr", Policy: store.PolicyAuto})
	h.eng.containers = []discovery.Container{
		{ID: "c1", Name: "sonarr", Image: "linuxserver/sonarr:latest", Source: discovery.SourceRun},
		{ID: "c2", Name: "immich", Image: "ghcr.io/immich-app/immich-server:release", Source: discovery.SourceCompose},
		{ID: "c3", Name: "jellyfin", Image: "jellyfin/jellyfin:latest", Source: discovery.SourceRun},
	}
}

func TestUpdatesListBreakingFirst(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	seedUpdates(t, h)
	list := decode[[]map[string]any](t, h.do("GET", "/api/updates", nil))
	if len(list) != 2 || list[0]["container"] != "immich" || list[0]["breaking"] != true || list[1]["container"] != "sonarr" {
		t.Fatalf("list %v", list)
	}
	if list[1]["policy"] != "auto" || list[0]["policy"] != "notify" || list[0]["newVersion"] != "2.0.0" {
		t.Fatalf("fields %v", list)
	}
	if reasons, _ := list[0]["reasons"].([]any); len(reasons) != 1 {
		t.Fatalf("reasons %v", list[0]["reasons"])
	}
}

func TestContainersList(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	seedUpdates(t, h)
	list := decode[[]map[string]any](t, h.do("GET", "/api/containers", nil))
	if len(list) != 3 || list[0]["name"] != "immich" || list[1]["name"] != "jellyfin" || list[2]["name"] != "sonarr" {
		t.Fatalf("list %v", list)
	}
	if list[0]["source"] != "compose" || list[0]["updateAvailable"] != true || list[1]["updateAvailable"] != false || list[2]["policy"] != "auto" {
		t.Fatalf("fields %v", list)
	}
}

func TestChangelog(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	seedUpdates(t, h)
	h.srv.d.Changelog = fakeChangelog{releases: []changelog.Release{
		{Tag: "v2.0.0", Name: "Two", Body: "Database migration runs on first start.", URL: "https://x/2"},
		{Tag: "v1.99.0", Body: "Small fixes."},
		{Tag: "v1.98.0", Body: "Old."},
	}}
	got := decode[map[string]any](t, h.do("GET", "/api/updates/immich/changelog", nil))
	rel, _ := got["releases"].([]any)
	if got["repo"] != "immich-app/immich" || len(rel) != 2 {
		t.Fatalf("changelog %v", got)
	}
	if first, _ := rel[0].(map[string]any); first["tag"] != "v2.0.0" || first["url"] != "https://x/2" {
		t.Fatalf("first release %v", rel[0])
	}
	if rec := h.do("GET", "/api/updates/jellyfin/changelog", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("no update for jellyfin: %d", rec.Code)
	}
}

func TestChangelogWithoutRepoIsEmptyNotAnError(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	h.st.ReplaceAvailable([]store.Available{{Container: "x", Image: "x:1", RemoteDigest: "sha256:2", DetectedAt: time.Now()}})
	h.st.ReplaceInfo([]store.Info{{Container: "x", OldVersion: "1.0.0", NewVersion: "1.1.0"}})
	got := decode[map[string]any](t, h.do("GET", "/api/updates/x/changelog", nil))
	if rel, _ := got["releases"].([]any); got["repo"] != "" || len(rel) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestHistory(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	base := time.Now()
	for i, out := range []string{"ok", "rolled_back", "ok"} {
		h.st.AddHistory(store.History{Container: "app", Image: "app:1", StartedAt: base.Add(time.Duration(i) * time.Minute), FinishedAt: base.Add(time.Duration(i)*time.Minute + time.Second), Outcome: out, Log: []string{"pull"}})
	}
	all := decode[[]map[string]any](t, h.do("GET", "/api/history", nil))
	if len(all) != 3 || all[0]["outcome"] != "ok" || all[1]["outcome"] != "rolled_back" {
		t.Fatalf("history %v", all)
	}
	if two := decode[[]map[string]any](t, h.do("GET", "/api/history?limit=2", nil)); len(two) != 2 {
		t.Fatalf("limit: %d", len(two))
	}
	if rec := h.do("GET", "/api/history?limit=abc", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", rec.Code)
	}
}

func TestCheckApplyRollbackRunInTheBackground(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	seedUpdates(t, h)

	if rec := h.do("POST", "/api/check", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("POST", "/api/updates/sonarr/apply", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("POST", "/api/containers/jellyfin/rollback", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body)
	}
	h.srv.Wait()
	h.eng.mu.Lock()
	defer h.eng.mu.Unlock()
	if h.eng.checks != 1 || len(h.eng.updates) != 1 || h.eng.updates[0] != "sonarr" || len(h.eng.rollbacks) != 1 || h.eng.rollbacks[0] != "jellyfin" {
		t.Fatalf("checks %d updates %v rollbacks %v", h.eng.checks, h.eng.updates, h.eng.rollbacks)
	}
}

func TestApplyUnknownContainerAndBusy(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	seedUpdates(t, h)
	if rec := h.do("POST", "/api/updates/nope/apply", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", rec.Code)
	}

	h.eng.block = make(chan struct{})
	if rec := h.do("POST", "/api/updates/sonarr/apply", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("first apply: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/updates/sonarr/apply", nil); rec.Code != http.StatusConflict {
		t.Fatalf("second apply while running: %d", rec.Code)
	}
	jobs := decode[map[string]any](t, h.do("GET", "/api/jobs", nil))
	if running, _ := jobs["running"].([]any); len(running) != 1 {
		t.Fatalf("jobs %v", jobs)
	}
	list := decode[[]map[string]any](t, h.do("GET", "/api/updates", nil))
	busy := map[string]any{}
	for _, u := range list {
		busy[u["container"].(string)] = u["busy"]
	}
	if busy["sonarr"] != true || busy["immich"] != false {
		t.Fatalf("busy flags %v", busy)
	}
	close(h.eng.block)
	h.srv.Wait()
}

func TestFailedJobIsReported(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	seedUpdates(t, h)
	h.eng.err = errFake
	h.do("POST", "/api/updates/sonarr/apply", nil)
	h.srv.Wait()
	jobs := decode[map[string]any](t, h.do("GET", "/api/jobs", nil))
	failed, _ := jobs["failed"].([]any)
	if len(failed) != 1 {
		t.Fatalf("jobs %v", jobs)
	}
	if f, _ := failed[0].(map[string]any); f["key"] != "sonarr" || f["error"] == "" {
		t.Fatalf("failure %v", failed[0])
	}
}

func TestSaveSettings(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	seedUpdates(t, h)
	rec := h.do("PUT", "/api/containers/sonarr/settings", map[string]any{"policy": "never", "httpUrl": "http://sonarr:8989/ping", "repo": "Sonarr/Sonarr", "verifyWindowSeconds": 90})
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	got, _ := h.st.GetSettings("sonarr")
	if got.Policy != "never" || got.HTTPURL != "http://sonarr:8989/ping" || got.Repo != "Sonarr/Sonarr" || got.VerifyWindow != 90*time.Second {
		t.Fatalf("stored %+v", got)
	}
	for name, body := range map[string]map[string]any{
		"bad policy": {"policy": "yolo"},
		"bad repo":   {"policy": "auto", "repo": "not a repo"},
		"bad url":    {"policy": "auto", "httpUrl": "ftp://x"},
		"bad window": {"policy": "auto", "verifyWindowSeconds": -5},
	} {
		if rec := h.do("PUT", "/api/containers/sonarr/settings", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec := h.do("PUT", "/api/containers/nope/settings", map[string]any{"policy": "auto"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown container: %d", rec.Code)
	}
}
```

Add `var errFake = errors.New("docker unreachable")` and the import `"errors"` to `internal/api/api_test.go`. Extend `TestProtectedRoutesNeedLogin` back to the full list:

```go
	for _, p := range [][2]string{{"GET", "/api/me"}, {"GET", "/api/updates"}, {"GET", "/api/containers"}, {"GET", "/api/history"}, {"GET", "/api/jobs"}, {"POST", "/api/check"}, {"POST", "/api/updates/x/apply"}, {"PUT", "/api/containers/x/settings"}} {
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/api/`
Expected: FAIL (404 for the new routes).

- [ ] **Step 3: Implement**

Replace `internal/api/updates.go`:

```go
package api

import (
	"context"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/changelog"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func (s *Server) registerUpdates() {
	s.mux.HandleFunc("GET /api/updates", s.protected(s.handleUpdates))
	s.mux.HandleFunc("GET /api/containers", s.protected(s.handleContainers))
	s.mux.HandleFunc("GET /api/updates/{name}/changelog", s.protected(s.handleChangelog))
	s.mux.HandleFunc("GET /api/history", s.protected(s.handleHistory))
	s.mux.HandleFunc("GET /api/jobs", s.protected(s.handleJobs))
	s.mux.HandleFunc("POST /api/check", s.protected(s.handleCheck))
	s.mux.HandleFunc("POST /api/updates/{name}/apply", s.protected(s.handleApply))
	s.mux.HandleFunc("POST /api/containers/{name}/rollback", s.protected(s.handleRollback))
	s.mux.HandleFunc("PUT /api/containers/{name}/settings", s.protected(s.handleSaveSettings))
}

func (s *Server) busy() map[string]bool {
	m := map[string]bool{}
	for _, j := range s.jobs.Running() {
		m[j.Key] = true
	}
	return m
}

func (s *Server) handleUpdates(w http.ResponseWriter, r *http.Request) {
	avail, err := s.d.Store.ListAvailable()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the updates.")
		return
	}
	infos, err := s.d.Store.ListInfo()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the updates.")
		return
	}
	info := map[string]store.Info{}
	for _, i := range infos {
		info[i.Container] = i
	}
	busy := s.busy()
	out := make([]map[string]any, 0, len(avail))
	for _, a := range avail {
		i := info[a.Container]
		settings, err := s.d.Store.GetSettings(a.Container)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not read the settings.")
			return
		}
		out = append(out, map[string]any{
			"container": a.Container, "image": a.Image, "oldVersion": i.OldVersion, "newVersion": i.NewVersion,
			"repo": i.Repo, "breaking": i.Breaking, "reasons": nonNil(i.Reasons), "policy": settings.Policy,
			"detectedAt": a.DetectedAt, "busy": busy[a.Container],
		})
	}
	sort.SliceStable(out, func(x, y int) bool {
		bx, by := out[x]["breaking"].(bool), out[y]["breaking"].(bool)
		if bx != by {
			return bx
		}
		return out[x]["container"].(string) < out[y]["container"].(string)
	})
	writeJSON(w, http.StatusOK, out)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Engine.Containers(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "Could not reach Docker.")
		return
	}
	avail, err := s.d.Store.ListAvailable()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the updates.")
		return
	}
	has := map[string]bool{}
	for _, a := range avail {
		has[a.Container] = true
	}
	busy := s.busy()
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	out := make([]map[string]any, 0, len(list))
	for _, c := range list {
		st, err := s.d.Store.GetSettings(c.Name)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "Could not read the settings.")
			return
		}
		out = append(out, map[string]any{
			"name": c.Name, "image": c.Image, "source": string(c.Source), "policy": st.Policy, "httpUrl": st.HTTPURL,
			"repo": st.Repo, "verifyWindowSeconds": int(st.VerifyWindow / time.Second),
			"updateAvailable": has[c.Name], "busy": busy[c.Name],
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleChangelog(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	infos, err := s.d.Store.ListInfo()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the updates.")
		return
	}
	var info *store.Info
	for i := range infos {
		if infos[i].Container == name {
			info = &infos[i]
		}
	}
	if info == nil {
		writeError(w, http.StatusNotFound, "There is no update available for that container.")
		return
	}
	releases := []map[string]any{}
	if info.Repo != "" && s.d.Changelog != nil {
		all, err := s.d.Changelog.Releases(r.Context(), info.Repo)
		if err != nil {
			s.logf("api: release notes for %s: %v", info.Repo, err)
		}
		for _, rel := range changelog.Between(all, info.OldVersion, info.NewVersion) {
			releases = append(releases, map[string]any{"tag": rel.Tag, "name": rel.Name, "body": rel.Body, "url": rel.URL, "publishedAt": rel.PublishedAt})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"repo": info.Repo, "oldVersion": info.OldVersion, "newVersion": info.NewVersion, "releases": releases})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "The limit must be a positive number.")
			return
		}
		limit = min(n, 200)
	}
	hist, err := s.d.Store.ListHistory(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the history.")
		return
	}
	out := make([]map[string]any, 0, len(hist))
	for _, h := range hist {
		out = append(out, map[string]any{
			"id": h.ID, "container": h.Container, "image": h.Image, "fromImage": h.FromImage, "toImage": h.ToImage,
			"startedAt": h.StartedAt, "finishedAt": h.FinishedAt, "outcome": h.Outcome, "reason": h.Reason, "log": nonNil(h.Log),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"running": s.jobs.Running(), "failed": s.jobs.Recent()})
}

func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	ok := s.jobs.Start(s.d.BaseCtx, "check", "check", func(ctx context.Context) error {
		_, err := s.d.Engine.Check(ctx)
		if err == nil {
			err = s.d.Store.SetSetting("last_check", time.Now().UTC().Format(time.RFC3339))
		}
		return err
	})
	if !ok {
		writeError(w, http.StatusConflict, "A check is already running.")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) knownContainer(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	list, err := s.d.Engine.Containers(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "Could not reach Docker.")
		return "", false
	}
	for _, c := range list {
		if c.Name == name {
			return name, true
		}
	}
	writeError(w, http.StatusNotFound, "There is no container with that name.")
	return "", false
}

func (s *Server) startContainerJob(w http.ResponseWriter, r *http.Request, kind string, fn func(context.Context, string) error) {
	name, ok := s.knownContainer(w, r)
	if !ok {
		return
	}
	if !s.jobs.Start(s.d.BaseCtx, name, kind, func(ctx context.Context) error { return fn(ctx, name) }) {
		writeError(w, http.StatusConflict, "That container is busy. Wait for the current action to finish.")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) handleApply(w http.ResponseWriter, r *http.Request) {
	s.startContainerJob(w, r, "update", func(ctx context.Context, name string) error {
		_, err := s.d.Engine.Update(ctx, name)
		return err
	})
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	s.startContainerJob(w, r, "rollback", func(ctx context.Context, name string) error {
		_, err := s.d.Engine.Rollback(ctx, name)
		return err
	})
}

type settingsBody struct {
	Policy              string `json:"policy"`
	HTTPURL             string `json:"httpUrl"`
	Repo                string `json:"repo"`
	VerifyWindowSeconds int    `json:"verifyWindowSeconds"`
}

func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request) {
	name, ok := s.knownContainer(w, r)
	if !ok {
		return
	}
	var b settingsBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	b.HTTPURL, b.Repo = strings.TrimSpace(b.HTTPURL), strings.TrimSpace(b.Repo)
	switch {
	case !store.ValidPolicy(b.Policy):
		writeError(w, http.StatusBadRequest, "The policy must be notify, auto or never.")
	case b.Repo != "" && !repoPattern.MatchString(b.Repo):
		writeError(w, http.StatusBadRequest, "The repository must look like owner/name.")
	case b.HTTPURL != "" && !strings.HasPrefix(b.HTTPURL, "http://") && !strings.HasPrefix(b.HTTPURL, "https://"):
		writeError(w, http.StatusBadRequest, "The health check URL must start with http:// or https://.")
	case b.VerifyWindowSeconds < 0 || b.VerifyWindowSeconds > 3600:
		writeError(w, http.StatusBadRequest, "The verify window must be between 0 and 3600 seconds.")
	default:
		st := store.Settings{Container: name, Policy: b.Policy, HTTPURL: b.HTTPURL, Repo: b.Repo, VerifyWindow: time.Duration(b.VerifyWindowSeconds) * time.Second}
		if err := s.d.Store.SetSettings(st); err != nil {
			writeError(w, http.StatusInternalServerError, "Could not save the settings.")
			return
		}
		writeJSON(w, http.StatusOK, b)
	}
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `gofmt -l . ; go vet ./... && go test -race -count=1 ./internal/api/`
Expected: `ok`

- [ ] **Step 5: Falsify the busy guard**

Temporarily make `Jobs.Start` ignore an existing key (delete the `if _, busy := ...` block), rerun `go test ./internal/api/ -run 'TestApplyUnknownContainerAndBusy|TestJobsRunOncePerKey'`, confirm both FAIL, then restore.

- [ ] **Step 6: Commit**

```bash
git add internal/api
git commit -m "api: updates, containers, changelog, history, actions, settings"
```

---

### Task 9: Notifier, push and widget endpoints

**Files:**
- Modify: `internal/api/notifiers.go`
- Test: `internal/api/notifiers_test.go`

**Interfaces:**
- Consumes: Task 7 and 8 helpers, `notify.Types`, `notify.Build`, `notify.SecretKeys`, `notify.SendTest`, `push`.
- Produces (login and, for writes, the CSRF header required, except the widget read):
  - `GET /api/notifiers/types` → `[{"type","label","fields":[{"key","label","secret","required"}]}]`
  - `GET /api/notifiers` → `[{"id","name","type","enabled","config":{...}}]`; secret values are `"********"` when set and `""` when empty
  - `POST /api/notifiers` `{"name","type","enabled","config"}` → `201` with the saved notifier (secrets masked); `400` with the `notify.Build` message when a required field is missing or the type is unknown, and for an empty name
  - `PUT /api/notifiers/{id}` same body; a secret sent as `"********"` keeps the stored value; `404` for an unknown ID
  - `DELETE /api/notifiers/{id}` → `200 {"ok":true}`
  - `POST /api/notifiers/{id}/test` → `200 {"ok":true}`, or `502 {"error": "<sender error>"}`
  - `GET /api/push/key` → `{"publicKey": "..."}`; `503` when push is not configured
  - `POST /api/push/subscribe` `{"endpoint","keys":{"p256dh","auth"}}` → `201`; `400` when a field is missing or the endpoint is not `https://`
  - `POST /api/push/unsubscribe` `{"endpoint"}` → `200`
  - `GET /api/widget/token` → `{"token"}` (created on first use); `POST /api/widget/token/rotate` → `{"token"}` (new value)
  - `GET /api/widget` (no session; `Authorization: Bearer <token>` or `?token=`) → `{"updates":N,"breaking":M,"lastCheck":"<RFC3339 or empty>","url":"<BaseURL>"}`; `401` for a missing or wrong token (constant-time compare)

- [ ] **Step 1: Write the failing tests**

`internal/api/notifiers_test.go`:

```go
package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/push"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

func TestNotifierTypes(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	types := decode[[]map[string]any](t, h.do("GET", "/api/notifiers/types", nil))
	if len(types) != 6 || types[0]["type"] != "ntfy" {
		t.Fatalf("types %v", types)
	}
	fields, _ := types[0]["fields"].([]any)
	if len(fields) != 3 {
		t.Fatalf("ntfy fields %v", fields)
	}
}

func TestNotifierCRUDMasksSecrets(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	rec := h.do("POST", "/api/notifiers", map[string]any{"name": "phone", "type": "ntfy", "enabled": true,
		"config": map[string]string{"url": "https://ntfy.sh", "topic": "updates", "token": "s3cret"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decode[map[string]any](t, rec)
	cfg, _ := created["config"].(map[string]any)
	if cfg["token"] != "********" || cfg["topic"] != "updates" {
		t.Fatalf("created config %v", cfg)
	}
	id := int64(created["id"].(float64))
	stored, _ := h.st.GetNotifier(id)
	if stored.Config["token"] != "s3cret" {
		t.Fatalf("the real secret must be stored: %v", stored.Config)
	}

	list := decode[[]map[string]any](t, h.do("GET", "/api/notifiers", nil))
	if len(list) != 1 || list[0]["config"].(map[string]any)["token"] != "********" {
		t.Fatalf("list %v", list)
	}

	// Updating with the mask keeps the stored secret; a new value replaces it.
	put := func(token string) {
		rec := h.do("PUT", "/api/notifiers/"+itoa(id), map[string]any{"name": "phone", "type": "ntfy", "enabled": false,
			"config": map[string]string{"url": "https://ntfy.sh", "topic": "updates", "token": token}})
		if rec.Code != http.StatusOK {
			t.Fatalf("update: %d %s", rec.Code, rec.Body)
		}
	}
	put("********")
	if got, _ := h.st.GetNotifier(id); got.Config["token"] != "s3cret" || got.Enabled {
		t.Fatalf("mask must keep the secret: %+v", got)
	}
	put("n3w")
	if got, _ := h.st.GetNotifier(id); got.Config["token"] != "n3w" {
		t.Fatalf("new secret not stored: %+v", got)
	}

	if rec := h.do("DELETE", "/api/notifiers/"+itoa(id), nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := h.do("PUT", "/api/notifiers/"+itoa(id), map[string]any{"name": "x", "type": "ntfy", "config": map[string]string{"url": "https://a", "topic": "t"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("update of a deleted notifier: %d", rec.Code)
	}
}

func TestNotifierValidation(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	for name, body := range map[string]map[string]any{
		"missing topic": {"name": "n", "type": "ntfy", "config": map[string]string{"url": "https://a"}},
		"unknown type":  {"name": "n", "type": "pigeon", "config": map[string]string{}},
		"empty name":    {"name": " ", "type": "webhook", "config": map[string]string{"url": "https://a"}},
	} {
		if rec := h.do("POST", "/api/notifiers", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestNotifierTestSend(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Get("Title") }))
	defer srv.Close()
	id, _ := h.st.AddNotifier(store.Notifier{Name: "n", Type: "ntfy", Config: map[string]string{"url": srv.URL, "topic": "t"}, Enabled: true})
	if rec := h.do("POST", "/api/notifiers/"+itoa(id)+"/test", nil); rec.Code != http.StatusOK || got != "nextupdate test" {
		t.Fatalf("test send: %d %s (title %q)", rec.Code, rec.Body, got)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	id2, _ := h.st.AddNotifier(store.Notifier{Name: "b", Type: "ntfy", Config: map[string]string{"url": bad.URL, "topic": "t"}, Enabled: true})
	if rec := h.do("POST", "/api/notifiers/"+itoa(id2)+"/test", nil); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "status 500") {
		t.Fatalf("failing test send: %d %s", rec.Code, rec.Body)
	}
}

func TestPushEndpoints(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	if rec := h.do("GET", "/api/push/key", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("push not configured: %d", rec.Code)
	}
	keys, _ := push.EnsureKeys(h.st)
	h.srv.d.Push = &push.Sender{Keys: keys, Subject: "mailto:a@b.c"}
	if k := decode[map[string]string](t, h.do("GET", "/api/push/key", nil)); k["publicKey"] != keys.Public {
		t.Fatalf("key %v", k)
	}

	sub := map[string]any{"endpoint": "https://push.example/abc", "keys": map[string]string{"p256dh": "P", "auth": "A"}}
	if rec := h.do("POST", "/api/push/subscribe", sub); rec.Code != http.StatusCreated {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body)
	}
	if list, _ := h.st.ListPushSubs(); len(list) != 1 || list[0].P256dh != "P" || list[0].UserID == 0 {
		t.Fatalf("stored %+v", list)
	}
	for name, body := range map[string]map[string]any{
		"http endpoint": {"endpoint": "http://push.example/x", "keys": map[string]string{"p256dh": "P", "auth": "A"}},
		"missing keys":  {"endpoint": "https://push.example/x", "keys": map[string]string{"p256dh": "P"}},
	} {
		if rec := h.do("POST", "/api/push/subscribe", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec := h.do("POST", "/api/push/unsubscribe", map[string]string{"endpoint": "https://push.example/abc"}); rec.Code != http.StatusOK {
		t.Fatalf("unsubscribe: %d", rec.Code)
	}
	if list, _ := h.st.ListPushSubs(); len(list) != 0 {
		t.Fatalf("still subscribed: %+v", list)
	}
}

func TestWidget(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	h.st.ReplaceAvailable([]store.Available{
		{Container: "a", Image: "a:1", RemoteDigest: "sha256:1", DetectedAt: time.Now()},
		{Container: "b", Image: "b:1", RemoteDigest: "sha256:2", DetectedAt: time.Now()},
		{Container: "c", Image: "c:1", RemoteDigest: "sha256:3", DetectedAt: time.Now()},
	})
	h.st.ReplaceInfo([]store.Info{{Container: "b", Breaking: true}})
	h.st.SetSetting("last_check", "2026-09-25T18:00:00Z")
	token := decode[map[string]string](t, h.do("GET", "/api/widget/token", nil))["token"]
	if len(token) < 32 {
		t.Fatalf("token %q", token)
	}
	if again := decode[map[string]string](t, h.do("GET", "/api/widget/token", nil))["token"]; again != token {
		t.Fatal("the token must be stable until rotated")
	}

	h.cookie = nil // the widget is read without a session
	get := func(auth, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/widget"+query, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.srv.ServeHTTP(rec, req)
		return rec
	}
	rec := get("Bearer "+token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("widget with bearer: %d %s", rec.Code, rec.Body)
	}
	w := decode[map[string]any](t, rec)
	if w["updates"] != float64(3) || w["breaking"] != float64(1) || w["lastCheck"] != "2026-09-25T18:00:00Z" || w["url"] != "https://nu.example" {
		t.Fatalf("widget %v", w)
	}
	if rec := get("", "?token="+token); rec.Code != http.StatusOK {
		t.Fatalf("widget with query token: %d", rec.Code)
	}
	if rec := get("Bearer wrong", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", rec.Code)
	}
	if rec := get("", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
}

func TestWidgetTokenRotation(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	old := decode[map[string]string](t, h.do("GET", "/api/widget/token", nil))["token"]
	fresh := decode[map[string]string](t, h.do("POST", "/api/widget/token/rotate", nil))["token"]
	if fresh == "" || fresh == old {
		t.Fatalf("old %q new %q", old, fresh)
	}
	req := httptest.NewRequest("GET", "/api/widget?token="+old, nil)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("the old token must stop working: %d", rec.Code)
	}
}
```

Add to `internal/api/api_test.go`: `import "strconv"` and `func itoa(n int64) string { return strconv.FormatInt(n, 10) }`.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/api/ -run 'TestNotifier|TestPush|TestWidget'`
Expected: FAIL (404 for the new routes).

- [ ] **Step 3: Implement**

Replace `internal/api/notifiers.go`:

```go
package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/jordibrouwer/nextupdate/internal/notify"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

const secretMask = "********"

func (s *Server) registerNotifiers() {
	s.mux.HandleFunc("GET /api/notifiers/types", s.protected(s.handleNotifierTypes))
	s.mux.HandleFunc("GET /api/notifiers", s.protected(s.handleNotifierList))
	s.mux.HandleFunc("POST /api/notifiers", s.protected(s.handleNotifierCreate))
	s.mux.HandleFunc("PUT /api/notifiers/{id}", s.protected(s.handleNotifierUpdate))
	s.mux.HandleFunc("DELETE /api/notifiers/{id}", s.protected(s.handleNotifierDelete))
	s.mux.HandleFunc("POST /api/notifiers/{id}/test", s.protected(s.handleNotifierTest))
	s.mux.HandleFunc("GET /api/push/key", s.protected(s.handlePushKey))
	s.mux.HandleFunc("POST /api/push/subscribe", s.protected(s.handlePushSubscribe))
	s.mux.HandleFunc("POST /api/push/unsubscribe", s.protected(s.handlePushUnsubscribe))
	s.mux.HandleFunc("GET /api/widget/token", s.protected(s.handleWidgetToken))
	s.mux.HandleFunc("POST /api/widget/token/rotate", s.protected(s.handleWidgetRotate))
	s.mux.HandleFunc("GET /api/widget", s.handleWidget)
}

func (s *Server) handleNotifierTypes(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, t := range notify.Types() {
		fields := []map[string]any{}
		for _, f := range t.Fields {
			fields = append(fields, map[string]any{"key": f.Key, "label": f.Label, "secret": f.Secret, "required": f.Required})
		}
		out = append(out, map[string]any{"type": t.Type, "label": t.Label, "fields": fields})
	}
	writeJSON(w, http.StatusOK, out)
}

// present is the API shape of a notifier: secrets are masked.
func present(n store.Notifier) map[string]any {
	secret := notify.SecretKeys(n.Type)
	cfg := map[string]string{}
	for k, v := range n.Config {
		if secret[k] && v != "" {
			v = secretMask
		}
		cfg[k] = v
	}
	return map[string]any{"id": n.ID, "name": n.Name, "type": n.Type, "enabled": n.Enabled, "config": cfg}
}

func (s *Server) handleNotifierList(w http.ResponseWriter, r *http.Request) {
	list, err := s.d.Store.ListNotifiers()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the notifiers.")
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, n := range list {
		out = append(out, present(n))
	}
	writeJSON(w, http.StatusOK, out)
}

type notifierBody struct {
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Enabled bool              `json:"enabled"`
	Config  map[string]string `json:"config"`
}

// parseNotifier validates a request body. keep holds the stored config, so a
// masked secret is replaced by its stored value.
func (s *Server) parseNotifier(w http.ResponseWriter, r *http.Request, keep map[string]string) (store.Notifier, bool) {
	var b notifierBody
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return store.Notifier{}, false
	}
	b.Name = strings.TrimSpace(b.Name)
	if b.Name == "" {
		writeError(w, http.StatusBadRequest, "Enter a name for this notifier.")
		return store.Notifier{}, false
	}
	cfg := map[string]string{}
	for k, v := range b.Config {
		if v == secretMask && keep != nil {
			v = keep[k]
		}
		cfg[k] = strings.TrimSpace(v)
	}
	if _, err := notify.Build(b.Type, cfg, nil); err != nil {
		writeError(w, http.StatusBadRequest, capitalize(err.Error())+".")
		return store.Notifier{}, false
	}
	return store.Notifier{Name: b.Name, Type: b.Type, Config: cfg, Enabled: b.Enabled}, true
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (s *Server) handleNotifierCreate(w http.ResponseWriter, r *http.Request) {
	n, ok := s.parseNotifier(w, r, nil)
	if !ok {
		return
	}
	id, err := s.d.Store.AddNotifier(n)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save the notifier.")
		return
	}
	n.ID = id
	writeJSON(w, http.StatusCreated, present(n))
}

func notifierID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) handleNotifierUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := notifierID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "There is no notifier with that ID.")
		return
	}
	old, err := s.d.Store.GetNotifier(id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "There is no notifier with that ID.")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the notifier.")
		return
	}
	n, ok := s.parseNotifier(w, r, old.Config)
	if !ok {
		return
	}
	n.ID = id
	if err := s.d.Store.UpdateNotifier(n); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save the notifier.")
		return
	}
	writeJSON(w, http.StatusOK, present(n))
}

func (s *Server) handleNotifierDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := notifierID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "There is no notifier with that ID.")
		return
	}
	if err := s.d.Store.DeleteNotifier(id); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not delete the notifier.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleNotifierTest(w http.ResponseWriter, r *http.Request) {
	id, ok := notifierID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "There is no notifier with that ID.")
		return
	}
	n, err := s.d.Store.GetNotifier(id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "There is no notifier with that ID.")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the notifier.")
		return
	}
	if err := notify.SendTest(r.Context(), s.d.Client, n); err != nil {
		writeError(w, http.StatusBadGateway, capitalize(err.Error())+".")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handlePushKey(w http.ResponseWriter, r *http.Request) {
	if s.d.Push == nil {
		writeError(w, http.StatusServiceUnavailable, "Push notifications are not set up on this server.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"publicKey": s.d.Push.Keys.Public})
}

func (s *Server) handlePushSubscribe(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Endpoint string            `json:"endpoint"`
		Keys     map[string]string `json:"keys"`
	}
	if err := readJSON(r, &b); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !strings.HasPrefix(b.Endpoint, "https://") || b.Keys["p256dh"] == "" || b.Keys["auth"] == "" {
		writeError(w, http.StatusBadRequest, "The subscription needs an https endpoint and both keys.")
		return
	}
	if err := s.d.Store.AddPushSub(store.PushSub{Endpoint: b.Endpoint, P256dh: b.Keys["p256dh"], Auth: b.Keys["auth"], UserID: userID(r)}); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not save the subscription.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"ok": true})
}

func (s *Server) handlePushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Endpoint string `json:"endpoint"`
	}
	if err := readJSON(r, &b); err != nil || b.Endpoint == "" {
		writeError(w, http.StatusBadRequest, "The request needs an endpoint.")
		return
	}
	if err := s.d.Store.DeletePushSub(b.Endpoint); err != nil {
		writeError(w, http.StatusInternalServerError, "Could not remove the subscription.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) widgetToken() (string, error) {
	t, err := s.d.Store.GetSetting("widget_token")
	if err != nil || t != "" {
		return t, err
	}
	return s.rotateWidgetToken()
}

func (s *Server) rotateWidgetToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	t := base64.RawURLEncoding.EncodeToString(raw)
	return t, s.d.Store.SetSetting("widget_token", t)
}

func (s *Server) handleWidgetToken(w http.ResponseWriter, r *http.Request) {
	t, err := s.widgetToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the widget token.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": t})
}

func (s *Server) handleWidgetRotate(w http.ResponseWriter, r *http.Request) {
	t, err := s.rotateWidgetToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not create a new widget token.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": t})
}

// handleWidget serves the numbers for a nextdash custom widget. It uses its
// own read-only token instead of a session.
func (s *Server) handleWidget(w http.ResponseWriter, r *http.Request) {
	given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if given == "" {
		given = r.URL.Query().Get("token")
	}
	want, err := s.widgetToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the widget token.")
		return
	}
	if given == "" || want == "" || subtle.ConstantTimeCompare([]byte(given), []byte(want)) != 1 {
		writeError(w, http.StatusUnauthorized, "The widget token is missing or wrong.")
		return
	}
	avail, err := s.d.Store.ListAvailable()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the updates.")
		return
	}
	infos, err := s.d.Store.ListInfo()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Could not read the updates.")
		return
	}
	breaking := 0
	for _, i := range infos {
		if i.Breaking {
			breaking++
		}
	}
	last, _ := s.d.Store.GetSetting("last_check")
	writeJSON(w, http.StatusOK, map[string]any{"updates": len(avail), "breaking": breaking, "lastCheck": last, "url": s.d.BaseURL})
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `gofmt -l . ; go vet ./... && go test -race -count=1 ./internal/api/`
Expected: `ok`

- [ ] **Step 5: Commit**

```bash
git add internal/api
git commit -m "api: notifiers, web push and widget endpoints"
```

---

### Task 10: Wire `serve`, record the last check, README

**Files:**
- Modify: `internal/scheduler/scheduler.go`, `internal/scheduler/scheduler_test.go`, `cmd/nextupdate/main.go`, `Dockerfile`
- Create: `README.md`

**Interfaces:**
- Consumes: everything above.
- Produces:
  - The scheduler writes the setting `last_check` (RFC3339, UTC) after every successful `Check`
  - `serve` starts the HTTP server on `NEXTUPDATE_LISTEN` (default `:8099`) next to the scheduler, uses `notify.Dispatcher` as notifier, and shuts both down on SIGTERM (10 s grace for the HTTP server, then waits for running jobs)
  - New environment: `NEXTUPDATE_LISTEN`, `NEXTUPDATE_BASE_URL` (used in notification links and the widget `url`; default empty), `NEXTUPDATE_PUSH_SUBJECT` (default `mailto:nextupdate@localhost`; browsers may require a real contact address)

- [ ] **Step 1: Write the failing scheduler test**

Append to `internal/scheduler/scheduler_test.go`:

```go
func TestRunOnceRecordsLastCheck(t *testing.T) {
	s, _, _ := setup(t, store.PolicyNotify, store.Info{})
	if v, _ := s.Store.GetSetting("last_check"); v != "" {
		t.Fatalf("last_check before the first cycle: %q", v)
	}
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Store.GetSetting("last_check")
	if _, err := time.Parse(time.RFC3339, v); err != nil {
		t.Fatalf("last_check %q is not RFC3339: %v", v, err)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/scheduler/ -run TestRunOnceRecordsLastCheck`
Expected: FAIL, `last_check before...` passes but the parse fails on an empty string.

- [ ] **Step 3: Implement the scheduler change**

In `internal/scheduler/scheduler.go`, in `RunOnce` right after the successful `Check`:

```go
	avail, err := s.Engine.Check(ctx)
	if err != nil {
		return err
	}
	if err := s.Store.SetSetting("last_check", time.Now().UTC().Format(time.RFC3339)); err != nil {
		s.logf("scheduler: record last check: %v", err)
	}
```

Run `go test -count=1 ./internal/scheduler/`; expect `ok`.

- [ ] **Step 4: Wire the server into `main.go`**

Add the imports `"net/http"`, `"errors"` (already present), `"github.com/jordibrouwer/nextupdate/internal/api"`, `"github.com/jordibrouwer/nextupdate/internal/auth"`, `"github.com/jordibrouwer/nextupdate/internal/notify"`, `"github.com/jordibrouwer/nextupdate/internal/push"`, and add `var version = "dev"` at package level (set at build time with `-ldflags "-X main.version=..."`).

Replace the `case "serve":` block with:

```go
	case "serve":
		if err := reconcile(ctx, api, journal, runner); err != nil {
			return err
		}
		keys, err := push.EnsureKeys(st)
		if err != nil {
			return err
		}
		baseURL := os.Getenv("NEXTUPDATE_BASE_URL")
		pusher := &push.Sender{Keys: keys, Subject: envOr("NEXTUPDATE_PUSH_SUBJECT", "mailto:nextupdate@localhost")}
		dispatcher := &notify.Dispatcher{Store: st, Push: pusher, BaseURL: baseURL}

		jobCtx, cancelJobs := context.WithCancel(context.Background())
		defer cancelJobs()
		server := apipkg.New(apipkg.Deps{
			Store: st, Engine: eng, Changelog: eng.Changelog, Mappings: eng.Mappings, Push: pusher,
			BaseURL: baseURL, Version: version, BaseCtx: jobCtx,
			Auth: &auth.Service{Store: st, Limiter: auth.NewLimiter(5, 15*time.Minute, time.Now)},
		})
		httpServer := &http.Server{Addr: envOr("NEXTUPDATE_LISTEN", ":8099"), Handler: server, ReadHeaderTimeout: 10 * time.Second}

		errs := make(chan error, 1)
		go func() {
			fmt.Printf("listening on %s\n", httpServer.Addr)
			if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- err
			}
		}()
		fmt.Printf("serving: checking every %s\n", interval)
		sched := &scheduler.Scheduler{Engine: eng, Store: st, Notifier: dispatcher, Interval: interval}
		done := make(chan error, 1)
		go func() { done <- sched.Run(ctx) }()

		select {
		case err := <-errs:
			return err
		case <-ctx.Done():
		}
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
		server.Wait() // let a running update finish; it never gets cancelled halfway
		return <-done
```

The `api` import name collides with the local variable `api` (the Docker client). Import the package as `apipkg "github.com/jordibrouwer/nextupdate/internal/api"`.

Note: background jobs use `jobCtx`, which is only cancelled by the deferred `cancelJobs` after `server.Wait()` returned, so an update that is running when SIGTERM arrives finishes (adapters already use `context.WithoutCancel` for their rollback steps).

`Dockerfile`: add `EXPOSE 8099` after the `VOLUME` line.

- [ ] **Step 5: Write the README**

`README.md`:

````markdown
# nextupdate

Update cockpit for Docker containers. It shows what changes before you update (release notes between your version and the new one), flags breaking updates, updates with one click or by per-container policy, and rolls back automatically when the new container fails its health check.

Works with Docker Compose services and plain `docker run` containers on one host.

## Run it

```yaml
services:
  nextupdate:
    image: nextupdate:dev          # build with: docker build -t nextupdate:dev .
    restart: unless-stopped
    ports:
      - "8099:8099"
    environment:
      NEXTUPDATE_BASE_URL: "http://your-server:8099"
      # NEXTUPDATE_GITHUB_TOKEN: "..."   # optional, raises the GitHub rate limit
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock
      - ./data:/data
      # Compose projects: mount each project directory at the SAME path as on the host.
      - /srv/stacks:/srv/stacks
```

Open `http://your-server:8099`. The first visit creates the admin account.

## Security

nextupdate can create and remove containers, so anyone who can sign in controls the Docker host. Put it behind HTTPS (a reverse proxy) and use a long password. For a smaller blast radius run a `docker-socket-proxy` and set `DOCKER_HOST=tcp://socket-proxy:2375`.

The `/data` folder holds the database with notifier secrets (ntfy, Gotify, Telegram tokens, SMTP password) in plain text. Protect it like the Docker socket.

## Command line

| Command | What it does |
|---|---|
| `nextupdate serve` | checks on a schedule, applies policies, serves the API on `:8099` |
| `nextupdate check` | lists containers with a newer image |
| `nextupdate update <name>` | updates one container, rolls back on failure |
| `nextupdate policy <name> <notify\|auto\|never>` | sets the update policy |
| `nextupdate reconcile` | repairs an update that a crash interrupted |

Policies: `notify` (default) tells you; `auto` updates patch and minor versions but never breaking updates, databases or unknown versions; `never` stays silent.

## Environment

| Variable | Default | Meaning |
|---|---|---|
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker endpoint |
| `NEXTUPDATE_DATA` | `/data` | data directory |
| `NEXTUPDATE_LISTEN` | `:8099` | HTTP listen address |
| `NEXTUPDATE_BASE_URL` | empty | public URL, used in notification links and the widget |
| `NEXTUPDATE_INTERVAL` | `6h` | time between checks |
| `NEXTUPDATE_VERIFY_WINDOW` | `60s` | how long a new container gets to become healthy |
| `NEXTUPDATE_KEEP_OLD_IMAGES` | `168h` | how long the previous image is kept for a rollback |
| `NEXTUPDATE_GITHUB_TOKEN` | empty | optional GitHub token |
| `NEXTUPDATE_PUSH_SUBJECT` | `mailto:nextupdate@localhost` | contact address sent to push services |

## nextdash widget

Get the token from `GET /api/widget/token` (signed in). Then point a nextdash custom widget at `http://your-server:8099/api/widget` with the header `Authorization: Bearer <token>`. It returns `{"updates": N, "breaking": M, "lastCheck": "...", "url": "..."}`.
````

- [ ] **Step 6: Build, run and smoke-test against real Docker**

Run: `gofmt -l . ; go vet ./... && go test -race -count=1 ./... && go build -o /tmp/nextupdate ./cmd/nextupdate`
Expected: everything green, binary built.

Start the server in the background on port 8099 with a scratch data directory and try the API with curl (each call under 30 s):

```bash
d=$(mktemp -d)
(NEXTUPDATE_DATA=$d NEXTUPDATE_LISTEN=127.0.0.1:8099 NEXTUPDATE_INTERVAL=1h /tmp/nextupdate serve > /tmp/nu-serve.log 2>&1 &)
sleep 2
curl -s localhost:8099/api/status
curl -s -c /tmp/nu-cookies -X POST localhost:8099/api/setup -H 'X-NextUpdate: 1' -d '{"name":"jordi","password":"long enough password"}'
curl -s -b /tmp/nu-cookies localhost:8099/api/containers | head -c 400
curl -s -b /tmp/nu-cookies localhost:8099/api/updates
curl -s -o /dev/null -w '%{http_code}\n' -X POST localhost:8099/api/check                    # 403: no header
curl -s -o /dev/null -w '%{http_code}\n' -X POST -b /tmp/nu-cookies -H 'X-NextUpdate: 1' localhost:8099/api/check   # 202
T=$(curl -s -b /tmp/nu-cookies localhost:8099/api/widget/token | sed 's/.*"token":"\([^"]*\)".*/\1/')
curl -s -H "Authorization: Bearer $T" localhost:8099/api/widget
```

Expected: `setupNeeded: true` first; setup returns `{"ok":true}`; containers lists your running containers with their source; the check without the header gives `403`, with it `202`; the widget returns `{"updates":N,...}`. Then stop the server (`pkill -f 'nextupdate serve'` only if it is the one you started; check `pgrep -fl nextupdate` first) and confirm the log shows `listening on 127.0.0.1:8099` and no error lines.

- [ ] **Step 7: Rebuild the image and run it once**

Run in the background: `docker build -t nextupdate:dev . > /tmp/nu-build3.log 2>&1; echo exit=$? >> /tmp/nu-build3.log`, then when `exit=0`:
`docker run --rm -d --name nu-smoke -p 8099:8099 -v /var/run/docker.sock:/var/run/docker.sock nextupdate:dev` and `curl -s localhost:8099/api/status`, then `docker rm -f nu-smoke`.
Expected: `{"version":"dev","setupNeeded":true,"signedIn":false}`.

- [ ] **Step 8: Commit**

```bash
git add README.md Dockerfile cmd internal
git commit -m "serve the api, record the last check, add a readme"
```

---

## Spec coverage (Plan 3a)

| Spec item | Task |
|---|---|
| Login, single admin, sessions, CSRF protection | 2, 7 |
| Web Push (VAPID) | 4, 5, 9 |
| ntfy, Gotify, Discord, Telegram, e-mail notifiers | 3, 5, 9 |
| Update list, changelog view, update and rollback actions, history, settings | 6, 8 |
| Manual rollback ("terug naar vorige") | 6, 8 |
| nextdash widget endpoint with a separate read-only token | 9 |
| Notifier secrets, registry-side settings in the UI | 9 (masked), UI in 3b |
| Recommended socket-proxy setup, security notes | 10 (README) |
| PWA manifest, service worker, the actual pages, themes | Plan 3b |
| Registry credentials in settings | Not planned: registry auth uses `~/.docker/config.json` (mount it) |
| Self-update through a helper container | Later (the engine still refuses to update itself) |

**Known limits, accepted for now:** the API has no CORS (the UI is served by the same server); sessions are not listed or revoked individually (log out removes the current one); a second manual rollback swaps forward again because it uses the newest successful update as its source; Telegram and Discord messages are plain text.
