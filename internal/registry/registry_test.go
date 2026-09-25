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
