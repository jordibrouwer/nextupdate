package verify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/dockertest"
)

func setup(t *testing.T, mutate func(c *docker.ContainerJSON)) *dockertest.Fake {
	t.Helper()
	f := dockertest.New()
	f.AddImage("app:1", docker.ImageJSON{ID: "sha256:1"})
	c := f.AddContainer("c1", "app", "app:1", nil, true)
	mutate(c)
	return f
}

var fast = Check{Window: 60 * time.Millisecond, Interval: 5 * time.Millisecond}

func TestVerify(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(c *docker.ContainerJSON)
		ok     bool
		reason string
	}{
		{"healthy", func(c *docker.ContainerJSON) { c.State.Health = &docker.Health{Status: "healthy"} }, true, ""},
		{"unhealthy", func(c *docker.ContainerJSON) { c.State.Health = &docker.Health{Status: "unhealthy"} }, false, "unhealthy"},
		{"never healthy", func(c *docker.ContainerJSON) { c.State.Health = &docker.Health{Status: "starting"} }, false, "not healthy"},
		{"no healthcheck, stays up", func(c *docker.ContainerJSON) {}, true, ""},
		{"exited", func(c *docker.ContainerJSON) { c.State.Running, c.State.Status = false, "exited" }, false, "exited"},
		{"crashloop", func(c *docker.ContainerJSON) { c.RestartCount = 3 }, false, "crashloop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Verify(context.Background(), setup(t, tc.mutate), "c1", fast)
			if got.OK != tc.ok || !strings.Contains(got.Reason, tc.reason) {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestVerifyHTTP(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()
	healthy := func(c *docker.ContainerJSON) { c.State.Health = &docker.Health{Status: "healthy"} }
	chk := fast
	chk.HTTPURL = srv.URL

	if got := Verify(context.Background(), setup(t, healthy), "c1", chk); !got.OK {
		t.Fatalf("200 should pass: %+v", got)
	}
	status = http.StatusBadGateway
	if got := Verify(context.Background(), setup(t, healthy), "c1", chk); got.OK || !strings.Contains(got.Reason, "502") {
		t.Fatalf("502 should fail: %+v", got)
	}
}
