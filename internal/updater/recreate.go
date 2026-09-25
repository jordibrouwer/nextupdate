package updater

import (
	"reflect"

	"github.com/jordibrouwer/nextupdate/internal/docker"
)

var imageDefaultKeys = []string{"Cmd", "Entrypoint", "WorkingDir", "User", "ExposedPorts", "Volumes", "Healthcheck", "StopSignal"}

// BuildSpec turns an inspected container into a create request for ref,
// keeping what the user set and dropping what the old image supplied.
func BuildSpec(old docker.ContainerJSON, oldImg docker.ImageJSON, ref string) docker.CreateSpec {
	cfg := make(map[string]any, len(old.Config))
	for k, v := range old.Config {
		cfg[k] = v
	}
	cfg["Image"] = ref
	imgCfg := oldImg.Config
	if imgCfg == nil {
		imgCfg = map[string]any{}
	}
	if v, ok := cfg["Env"]; ok {
		cfg["Env"] = subtractList(v, imgCfg["Env"])
	}
	if v, ok := cfg["Labels"]; ok {
		cfg["Labels"] = subtractMap(v, imgCfg["Labels"])
	}
	for _, k := range imageDefaultKeys {
		if v, ok := cfg[k]; ok && reflect.DeepEqual(v, imgCfg[k]) {
			delete(cfg, k)
		}
	}
	short := shortID(old.ID)
	if h, _ := cfg["Hostname"].(string); h == short {
		delete(cfg, "Hostname")
	}
	eps := make(map[string]docker.EndpointSettings, len(old.NetworkSettings.Networks))
	for name, ep := range old.NetworkSettings.Networks {
		ep.Aliases = without(ep.Aliases, short)
		eps[name] = ep
	}
	return docker.CreateSpec{Config: cfg, HostConfig: old.HostConfig, NetworkingConfig: docker.NetworkingConfig{EndpointsConfig: eps}}
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func subtractList(a, b any) any {
	al, ok := a.([]any)
	if !ok {
		return a
	}
	drop := map[any]bool{}
	if bl, ok := b.([]any); ok {
		for _, v := range bl {
			drop[v] = true
		}
	}
	out := []any{}
	for _, v := range al {
		if !drop[v] {
			out = append(out, v)
		}
	}
	return out
}

func subtractMap(a, b any) any {
	am, ok := a.(map[string]any)
	if !ok {
		return a
	}
	bm, _ := b.(map[string]any)
	out := map[string]any{}
	for k, v := range am {
		if bv, ok := bm[k]; ok && reflect.DeepEqual(bv, v) {
			continue
		}
		out[k] = v
	}
	return out
}

func without(list []string, drop string) []string {
	var out []string
	for _, s := range list {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}
