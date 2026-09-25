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
