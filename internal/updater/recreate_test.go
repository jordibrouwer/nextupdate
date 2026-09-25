package updater

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/docker"
)

func TestBuildSpec(t *testing.T) {
	old := docker.ContainerJSON{
		ID: "0123456789abcdef0123",
		Config: map[string]any{
			"Image":    "app:latest",
			"Hostname": "0123456789ab",
			"Env":      []any{"PATH=/usr/bin", "APP_VERSION=1.0", "TZ=Europe/Amsterdam"},
			"Labels":   map[string]any{"org.opencontainers.image.version": "1.0", "mine": "yes"},
			"Cmd":      []any{"serve"},
			"User":     "1000",
		},
		HostConfig: json.RawMessage(`{"Binds":["/data:/data"]}`),
		NetworkSettings: docker.NetworkSettings{Networks: map[string]docker.EndpointSettings{
			"proxy": {Aliases: []string{"app", "0123456789ab"}},
		}},
	}
	oldImg := docker.ImageJSON{Config: map[string]any{
		"Env":    []any{"PATH=/usr/bin", "APP_VERSION=1.0"},
		"Labels": map[string]any{"org.opencontainers.image.version": "1.0"},
		"Cmd":    []any{"serve"},
		"User":   "",
	}}

	spec := BuildSpec(old, oldImg, "app:latest")

	if spec.Config["Image"] != "app:latest" {
		t.Errorf("image %v", spec.Config["Image"])
	}
	if !reflect.DeepEqual(spec.Config["Env"], []any{"TZ=Europe/Amsterdam"}) {
		t.Errorf("env %v", spec.Config["Env"])
	}
	if !reflect.DeepEqual(spec.Config["Labels"], map[string]any{"mine": "yes"}) {
		t.Errorf("labels %v", spec.Config["Labels"])
	}
	if _, ok := spec.Config["Cmd"]; ok {
		t.Error("Cmd equal to image default must be dropped")
	}
	if spec.Config["User"] != "1000" {
		t.Error("user-set User must stay")
	}
	if _, ok := spec.Config["Hostname"]; ok {
		t.Error("default hostname must be dropped")
	}
	if got := spec.NetworkingConfig.EndpointsConfig["proxy"].Aliases; !reflect.DeepEqual(got, []string{"app"}) {
		t.Errorf("aliases %v", got)
	}
	if string(spec.HostConfig) != `{"Binds":["/data:/data"]}` {
		t.Errorf("host config %s", spec.HostConfig)
	}
	if old.Config["Hostname"] != "0123456789ab" {
		t.Error("BuildSpec must not mutate the old config")
	}
}
