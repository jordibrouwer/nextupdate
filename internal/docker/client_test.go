package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c, err := New("tcp://" + strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestInspectContainer(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.44/containers/app/json" {
			t.Errorf("path %s", r.URL.Path)
		}
		io.WriteString(w, `{"Id":"abc","Name":"/app","Image":"sha256:1","RestartCount":2,
			"State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}},
			"Config":{"Image":"app:latest","Env":["A=1"]},"HostConfig":{"Binds":["/x:/y"]},
			"NetworkSettings":{"Networks":{"bridge":{"Aliases":["app"]}}}}`)
	})
	got, err := c.InspectContainer(context.Background(), "app")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "abc" || got.RestartCount != 2 || got.State.Health.Status != "healthy" || got.Config["Image"] != "app:latest" {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(string(got.HostConfig), "/x:/y") || got.NetworkSettings.Networks["bridge"].Aliases[0] != "app" {
		t.Fatalf("host/network config lost: %+v", got)
	}
}

func TestNotFound(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"message":"No such container: x"}`)
	})
	_, err := c.InspectContainer(context.Background(), "x")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestStopNotModifiedIsOK(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotModified)
	})
	if err := c.StopContainer(context.Background(), "abc"); err != nil {
		t.Fatal(err)
	}
}

func TestPullReportsStreamError(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fromImage") != "ghcr.io/team/app" || r.URL.Query().Get("tag") != "1.2" {
			t.Errorf("query %s", r.URL.RawQuery)
		}
		io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"error":"manifest unknown"}`+"\n")
	})
	err := c.PullImage(context.Background(), "ghcr.io/team/app:1.2")
	if err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("want stream error, got %v", err)
	}
}

func TestCreateContainerBody(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "app" {
			t.Errorf("name %q", r.URL.Query().Get("name"))
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["Image"] != "app:2" || body["HostConfig"] == nil || body["NetworkingConfig"] == nil {
			t.Errorf("body %v", body)
		}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"Id":"new1"}`)
	})
	id, err := c.CreateContainer(context.Background(), "app", CreateSpec{
		Config:           map[string]any{"Image": "app:2"},
		HostConfig:       json.RawMessage(`{"Binds":["/x:/y"]}`),
		NetworkingConfig: NetworkingConfig{EndpointsConfig: map[string]EndpointSettings{"bridge": {}}},
	})
	if err != nil || id != "new1" {
		t.Fatalf("id %q err %v", id, err)
	}
}

func TestSplitRef(t *testing.T) {
	cases := map[string][2]string{
		"nginx":                  {"nginx", "latest"},
		"nginx:1.27":             {"nginx", "1.27"},
		"localhost:5000/app":     {"localhost:5000/app", "latest"},
		"localhost:5000/app:2":   {"localhost:5000/app", "2"},
		"ghcr.io/a/b@sha256:abc": {"ghcr.io/a/b", "sha256:abc"},
	}
	for in, want := range cases {
		repo, tag := SplitRef(in)
		if repo != want[0] || tag != want[1] {
			t.Errorf("SplitRef(%q) = %q, %q", in, repo, tag)
		}
	}
}

func TestRemoveImageConflict(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1.44/images/sha256:abc" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"message":"image is being used by running container"}`)
	})
	err := c.RemoveImage(context.Background(), "sha256:abc")
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}
