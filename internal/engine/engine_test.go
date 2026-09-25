package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"testing"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/dockertest"
	"github.com/jordibrouwer/nextupdate/internal/registry"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
)

type fakeRegistry map[string]string

func (r fakeRegistry) RemoteDigest(ctx context.Context, ref string) (string, error) {
	if d, ok := r[ref]; ok {
		return d, nil
	}
	if ref == "down:1" {
		return "", fmt.Errorf("head %s: %w", ref, registry.ErrNotPublished)
	}
	return "", fmt.Errorf("unknown %s", ref)
}

func (r fakeRegistry) RemoteLabels(ctx context.Context, ref string) (map[string]string, error) {
	return nil, nil
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
	var buf bytes.Buffer
	e.Log = log.New(&buf, "", 0)
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
	if buf.Len() != 0 {
		t.Fatalf("not-published image must be skipped silently, log: %q", buf.String())
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
