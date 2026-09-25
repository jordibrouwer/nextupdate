package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/dockertest"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/verify"
)

func TestNewVerifierUsesContainerSettings(t *testing.T) {
	f := dockertest.New()
	f.AddImage("app:1", docker.ImageJSON{ID: "sha256:1"})
	c := f.AddContainer("c1", "app", "app:1", nil, true)
	c.State.Health = &docker.Health{Status: "healthy"}

	st, err := store.Open(filepath.Join(t.TempDir(), "v.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()

	v := NewVerifier(f, st, verify.Check{Window: 50 * time.Millisecond, Interval: 5 * time.Millisecond})
	ctx := context.Background()

	if got := v(ctx, "app", "c1"); !got.OK {
		t.Fatalf("no settings: %+v", got)
	}
	if err := st.SetSettings(store.Settings{Container: "app", Policy: store.PolicyNotify, HTTPURL: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if got := v(ctx, "app", "c1"); !got.OK {
		t.Fatalf("200 should pass: %+v", got)
	}
	status = http.StatusBadGateway
	if got := v(ctx, "app", "c1"); got.OK || !strings.Contains(got.Reason, "502") {
		t.Fatalf("502 should fail: %+v", got)
	}
	if got := v(ctx, "other", "c1"); !got.OK {
		t.Fatalf("settings of another container leaked: %+v", got)
	}
}
