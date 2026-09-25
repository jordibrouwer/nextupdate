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
	container  string
	image      string
	source     discovery.Source
	oldV, newV string
	repo       string
	policy     string
	breaking   bool
	reasons    []string
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
		if err := e.st.SetSettings(store.Settings{Container: u.container, Policy: u.policy}); err != nil {
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
