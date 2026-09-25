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
	return func(ctx context.Context, name, id string) verify.Result {
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
