package registry

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	ggcrregistry "github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
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

func TestRemoteDigestNotPublished(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v2/" {
				return
			}
			w.WriteHeader(status)
		}))
		ref := strings.TrimPrefix(srv.URL, "http://") + "/team/app:1.0"
		_, err := NewRemote().RemoteDigest(context.Background(), ref)
		srv.Close()
		if !errors.Is(err, ErrNotPublished) {
			t.Errorf("status %d: want ErrNotPublished, got %v", status, err)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/" {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer srv.Close()
	_, err := NewRemote().RemoteDigest(context.Background(), strings.TrimPrefix(srv.URL, "http://")+"/team/app:1.0")
	if err == nil || errors.Is(err, ErrNotPublished) {
		t.Fatalf("a 500 is a real failure, got %v", err)
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

	got, err := NewRemote().RemoteLabels(context.Background(), ref, Platform{})
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

// A multi-platform image: each child has its own labels, like a real release.
func TestRemoteLabelsPicksTheHostPlatform(t *testing.T) {
	srv := httptest.NewServer(ggcrregistry.New())
	defer srv.Close()
	ref := strings.TrimPrefix(srv.URL, "http://") + "/team/multi:1"

	child := func(arch, version string) (v1.Image, v1.Descriptor) {
		base, err := random.Image(128, 1)
		if err != nil {
			t.Fatal(err)
		}
		img, err := mutate.Config(base, v1.Config{Labels: map[string]string{LabelVersion: version}})
		if err != nil {
			t.Fatal(err)
		}
		img, err = mutate.ConfigFile(img, mustConfig(t, img, arch))
		if err != nil {
			t.Fatal(err)
		}
		return img, v1.Descriptor{Platform: &v1.Platform{OS: "linux", Architecture: arch}}
	}
	amd, amdDesc := child("amd64", "1.0.0-amd64")
	arm, armDesc := child("arm64", "1.0.0-arm64")
	idx := mutate.AppendManifests(empty.Index, mutate.IndexAddendum{Add: amd, Descriptor: amdDesc}, mutate.IndexAddendum{Add: arm, Descriptor: armDesc})
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(r, idx); err != nil {
		t.Fatal(err)
	}

	rem := NewRemote()
	for _, c := range []struct {
		p    Platform
		want string
	}{
		{Platform{OS: "linux", Arch: "arm64", Variant: "v8"}, "1.0.0-arm64"}, // Docker reports v8; index children carry no variant
		{Platform{OS: "linux", Arch: "amd64"}, "1.0.0-amd64"},
	} {
		got, err := rem.RemoteLabels(context.Background(), ref, c.p)
		if err != nil || got[LabelVersion] != c.want {
			t.Errorf("platform %+v: got %v %v, want %s", c.p, got, err, c.want)
		}
	}
}

func mustConfig(t *testing.T, img v1.Image, arch string) *v1.ConfigFile {
	t.Helper()
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.DeepCopy()
	cfg.OS, cfg.Architecture = "linux", arch
	return cfg
}

func TestPlatformOf(t *testing.T) {
	got := PlatformOf(docker.ImageJSON{Os: "linux", Architecture: "arm", Variant: "v7"})
	if got != (Platform{OS: "linux", Arch: "arm", Variant: "v7"}) {
		t.Fatalf("got %+v", got)
	}
}
