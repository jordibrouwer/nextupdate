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
