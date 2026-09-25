package engine

import (
	"bytes"
	"context"
	"errors"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/dockertest"
	"github.com/jordibrouwer/nextupdate/internal/registry"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

func engineFor(t *testing.T, f *dockertest.Fake, reg *fakeRegistry) *Engine {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Engine{API: f, Store: st, Registry: reg}
}

var versionLabel = map[string]string{"org.opencontainers.image.version": "1.0.0"}

// After a rollback, or after someone pulls without restarting the container,
// the container runs an image that lost its name: no tag, no repo digest. It
// must still be checked, not mistaken for a local build.
func TestCheckOffersUpdateToContainerOnAnImageWithoutAName(t *testing.T) {
	oldID := "sha256:" + strings.Repeat("a", 64)
	f := dockertest.New()
	f.AddImage("dang:1", docker.ImageJSON{ID: oldID})
	f.AddContainer("d1", "dang", "dang:1", versionLabel, true)
	reg := &fakeRegistry{digests: map[string]string{"dang:1": dNew}, labels: map[string]map[string]string{"dang:1": {"org.opencontainers.image.version": "1.1.0"}}}
	e := engineFor(t, f, reg)

	got, err := e.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Container != "dang" || got[0].LocalDigest != oldID || got[0].RemoteDigest != dNew {
		t.Fatalf("want an update for dang, got %+v", got)
	}
	infos, _ := e.Store.ListInfo()
	if len(infos) != 1 || infos[0].OldVersion != "1.0.0" || infos[0].NewVersion != "1.1.0" {
		t.Fatalf("the old version must come from the container's labels when the image is gone: %+v", infos)
	}
}

func TestCheckOffersUpdateWhenTheOldImageIsNotFoundAtAll(t *testing.T) {
	oldID := "sha256:" + strings.Repeat("b", 64)
	f := dockertest.New()
	f.AddImage("gone:1", docker.ImageJSON{ID: oldID})
	f.AddContainer("g1", "gone", "gone:1", versionLabel, true)
	delete(f.Images, oldID) // the image record is gone; the container still runs from it
	delete(f.Images, "gone:1")
	e := engineFor(t, f, &fakeRegistry{digests: map[string]string{"gone:1": dNew}})
	// Discovery resolves the reference through the container, so keep it there.
	f.Containers["g1"].Config["Image"] = "gone:1"

	got, err := e.Check(context.Background())
	if err != nil || len(got) != 1 || got[0].Container != "gone" || got[0].LocalDigest != oldID {
		t.Fatalf("got %+v %v", got, err)
	}
}

func TestCheckStillSkipsLocalBuilds(t *testing.T) {
	f := dockertest.New()
	// A local build has a tag but no registry digest.
	f.AddImage("mine:dev", docker.ImageJSON{ID: "sha256:" + strings.Repeat("c", 64), RepoTags: []string{"mine:dev"}})
	f.AddContainer("m1", "mine", "mine:dev", nil, true)
	e := engineFor(t, f, &fakeRegistry{digests: map[string]string{"mine:dev": dNew}})
	got, err := e.Check(context.Background())
	if err != nil || len(got) != 0 {
		t.Fatalf("a local build must not be offered an update: %+v %v", got, err)
	}
}

func TestCheckAsksForTheImagesOwnPlatform(t *testing.T) {
	f := dockertest.New()
	f.AddImage("arm:1", docker.ImageJSON{ID: "sha256:x", RepoDigests: []string{"arm@" + dOld}, Os: "linux", Architecture: "arm64", Variant: "v8"})
	f.AddContainer("a1", "arm", "arm:1", nil, true)
	reg := &fakeRegistry{digests: map[string]string{"arm:1": dNew}}
	e := engineFor(t, f, reg)
	if _, err := e.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if reg.platform != (registry.Platform{OS: "linux", Arch: "arm64", Variant: "v8"}) {
		t.Fatalf("labels were requested for %+v", reg.platform)
	}
}

// The same problem on every cycle (a registry that is down, a rate limit) is
// worth one log line, not one per check.
func TestCheckLogsTheSameProblemOnlyOnce(t *testing.T) {
	f := dockertest.New()
	f.AddImage("app:1", docker.ImageJSON{ID: "sha256:a", RepoDigests: []string{"app@" + dOld}})
	f.AddContainer("a1", "app", "app:1", nil, true)
	reg := &fakeRegistry{digestErr: errors.New("registry is down")}
	e := engineFor(t, f, reg)
	var buf bytes.Buffer
	e.Log = log.New(&buf, "", 0)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		if _, err := e.Check(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(buf.String(), "registry is down"); n != 1 {
		t.Fatalf("the same failure must be logged once, got %d times:\n%s", n, buf.String())
	}

	reg.digestErr = errors.New("a different failure")
	e.Check(ctx)
	if !strings.Contains(buf.String(), "a different failure") {
		t.Fatalf("a new kind of failure must be logged:\n%s", buf.String())
	}

	// After it recovered, the same failure is news again.
	reg.digestErr = nil
	reg.digests = map[string]string{"app:1": dOld}
	e.Check(ctx)
	reg.digestErr = errors.New("a different failure")
	e.Check(ctx)
	if n := strings.Count(buf.String(), "a different failure"); n != 2 {
		t.Fatalf("a failure that comes back after a good check must be logged again, got %d times:\n%s", n, buf.String())
	}
}
