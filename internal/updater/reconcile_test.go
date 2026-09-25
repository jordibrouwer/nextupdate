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
