package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/changelog"
	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

func seedUpdates(t *testing.T, h *harness) {
	t.Helper()
	now := time.Now()
	h.st.ReplaceAvailable([]store.Available{
		{Container: "sonarr", Image: "linuxserver/sonarr:latest", LocalDigest: "sha256:1", RemoteDigest: "sha256:2", DetectedAt: now},
		{Container: "immich", Image: "ghcr.io/immich-app/immich-server:release", LocalDigest: "sha256:3", RemoteDigest: "sha256:4", DetectedAt: now},
	})
	h.st.ReplaceInfo([]store.Info{
		{Container: "sonarr", OldVersion: "4.0.9", NewVersion: "4.0.10", Repo: "Sonarr/Sonarr"},
		{Container: "immich", OldVersion: "1.98.0", NewVersion: "2.0.0", Repo: "immich-app/immich", Breaking: true, Reasons: []string{"Major version change from 1.98.0 to 2.0.0."}},
	})
	h.st.SetSettings(store.Settings{Container: "sonarr", Policy: store.PolicyAuto})
	h.eng.containers = []discovery.Container{
		{ID: "c1", Name: "sonarr", Image: "linuxserver/sonarr:latest", Source: discovery.SourceRun},
		{ID: "c2", Name: "immich", Image: "ghcr.io/immich-app/immich-server:release", Source: discovery.SourceCompose},
		{ID: "c3", Name: "jellyfin", Image: "jellyfin/jellyfin:latest", Source: discovery.SourceRun},
	}
}

func TestUpdatesListBreakingFirst(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	seedUpdates(t, h)
	// "aaa" sorts before "immich" by name, so only the breaking flag can put immich first.
	avail, _ := h.st.ListAvailable()
	h.st.ReplaceAvailable(append(avail, store.Available{Container: "aaa", Image: "aaa:1", RemoteDigest: "sha256:9", DetectedAt: time.Now()}))
	infos, _ := h.st.ListInfo()
	h.st.ReplaceInfo(append(infos, store.Info{Container: "aaa", OldVersion: "1.0.0", NewVersion: "1.0.1"}))

	list := decode[[]map[string]any](t, h.do("GET", "/api/updates", nil))
	if len(list) != 3 || list[0]["container"] != "immich" || list[0]["breaking"] != true || list[1]["container"] != "aaa" || list[2]["container"] != "sonarr" {
		t.Fatalf("breaking first, then by name: %v", list)
	}
	if list[2]["policy"] != "auto" || list[0]["policy"] != "notify" || list[0]["newVersion"] != "2.0.0" {
		t.Fatalf("fields %v", list)
	}
	if reasons, _ := list[0]["reasons"].([]any); len(reasons) != 1 {
		t.Fatalf("reasons %v", list[0]["reasons"])
	}
}

func TestContainersList(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	seedUpdates(t, h)
	list := decode[[]map[string]any](t, h.do("GET", "/api/containers", nil))
	if len(list) != 3 || list[0]["name"] != "immich" || list[1]["name"] != "jellyfin" || list[2]["name"] != "sonarr" {
		t.Fatalf("list %v", list)
	}
	if list[0]["source"] != "compose" || list[0]["updateAvailable"] != true || list[1]["updateAvailable"] != false || list[2]["policy"] != "auto" {
		t.Fatalf("fields %v", list)
	}
}

func TestChangelog(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	seedUpdates(t, h)
	h.srv.d.Changelog = fakeChangelog{releases: []changelog.Release{
		{Tag: "v2.0.0", Name: "Two", Body: "Database migration runs on first start.", URL: "https://x/2"},
		{Tag: "v1.99.0", Body: "Small fixes."},
		{Tag: "v1.98.0", Body: "Old."},
	}}
	got := decode[map[string]any](t, h.do("GET", "/api/updates/immich/changelog", nil))
	rel, _ := got["releases"].([]any)
	if got["repo"] != "immich-app/immich" || len(rel) != 2 {
		t.Fatalf("changelog %v", got)
	}
	if first, _ := rel[0].(map[string]any); first["tag"] != "v2.0.0" || first["url"] != "https://x/2" {
		t.Fatalf("first release %v", rel[0])
	}
	if rec := h.do("GET", "/api/updates/jellyfin/changelog", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("no update for jellyfin: %d", rec.Code)
	}
}

func TestChangelogWithoutRepoIsEmptyNotAnError(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	h.st.ReplaceAvailable([]store.Available{{Container: "x", Image: "x:1", RemoteDigest: "sha256:2", DetectedAt: time.Now()}})
	h.st.ReplaceInfo([]store.Info{{Container: "x", OldVersion: "1.0.0", NewVersion: "1.1.0"}})
	got := decode[map[string]any](t, h.do("GET", "/api/updates/x/changelog", nil))
	if rel, _ := got["releases"].([]any); got["repo"] != "" || len(rel) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestHistory(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	base := time.Now()
	for i, out := range []string{"ok", "rolled_back", "ok"} {
		h.st.AddHistory(store.History{Container: "app", Image: "app:1", StartedAt: base.Add(time.Duration(i) * time.Minute), FinishedAt: base.Add(time.Duration(i)*time.Minute + time.Second), Outcome: out, Log: []string{"pull"}})
	}
	all := decode[[]map[string]any](t, h.do("GET", "/api/history", nil))
	if len(all) != 3 || all[0]["outcome"] != "ok" || all[1]["outcome"] != "rolled_back" {
		t.Fatalf("history %v", all)
	}
	if two := decode[[]map[string]any](t, h.do("GET", "/api/history?limit=2", nil)); len(two) != 2 {
		t.Fatalf("limit: %d", len(two))
	}
	if rec := h.do("GET", "/api/history?limit=abc", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad limit: %d", rec.Code)
	}
}

func TestCheckApplyRollbackRunInTheBackground(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	seedUpdates(t, h)

	if rec := h.do("POST", "/api/check", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("POST", "/api/updates/sonarr/apply", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body)
	}
	if rec := h.do("POST", "/api/containers/jellyfin/rollback", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("rollback: %d %s", rec.Code, rec.Body)
	}
	h.srv.Wait()
	h.eng.mu.Lock()
	defer h.eng.mu.Unlock()
	if h.eng.checks != 1 || len(h.eng.updates) != 1 || h.eng.updates[0] != "sonarr" || len(h.eng.rollbacks) != 1 || h.eng.rollbacks[0] != "jellyfin" {
		t.Fatalf("checks %d updates %v rollbacks %v", h.eng.checks, h.eng.updates, h.eng.rollbacks)
	}
	if v, _ := h.st.GetSetting("last_check"); v == "" {
		t.Fatal("a manual check must record the last check time")
	}
}

func TestApplyUnknownContainerAndBusy(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	seedUpdates(t, h)
	if rec := h.do("POST", "/api/updates/nope/apply", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown: %d", rec.Code)
	}

	h.eng.block = make(chan struct{})
	if rec := h.do("POST", "/api/updates/sonarr/apply", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("first apply: %d", rec.Code)
	}
	if rec := h.do("POST", "/api/updates/sonarr/apply", nil); rec.Code != http.StatusConflict {
		t.Fatalf("second apply while running: %d", rec.Code)
	}
	jobs := decode[map[string]any](t, h.do("GET", "/api/jobs", nil))
	if running, _ := jobs["running"].([]any); len(running) != 1 {
		t.Fatalf("jobs %v", jobs)
	}
	list := decode[[]map[string]any](t, h.do("GET", "/api/updates", nil))
	busy := map[string]any{}
	for _, u := range list {
		busy[u["container"].(string)] = u["busy"]
	}
	if busy["sonarr"] != true || busy["immich"] != false {
		t.Fatalf("busy flags %v", busy)
	}
	close(h.eng.block)
	h.srv.Wait()
}

func TestFailedJobIsReported(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	seedUpdates(t, h)
	h.eng.err = errFake
	h.do("POST", "/api/updates/sonarr/apply", nil)
	h.srv.Wait()
	jobs := decode[map[string]any](t, h.do("GET", "/api/jobs", nil))
	failed, _ := jobs["failed"].([]any)
	if len(failed) != 1 {
		t.Fatalf("jobs %v", jobs)
	}
	if f, _ := failed[0].(map[string]any); f["key"] != "sonarr" || f["error"] == "" {
		t.Fatalf("failure %v", failed[0])
	}
}

func TestSaveSettings(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	seedUpdates(t, h)
	rec := h.do("PUT", "/api/containers/sonarr/settings", map[string]any{"policy": "never", "httpUrl": "http://sonarr:8989/ping", "repo": "Sonarr/Sonarr", "verifyWindowSeconds": 90})
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	got, _ := h.st.GetSettings("sonarr")
	if got.Policy != "never" || got.HTTPURL != "http://sonarr:8989/ping" || got.Repo != "Sonarr/Sonarr" || got.VerifyWindow != 90*time.Second {
		t.Fatalf("stored %+v", got)
	}
	for name, body := range map[string]map[string]any{
		"bad policy": {"policy": "yolo"},
		"bad repo":   {"policy": "auto", "repo": "not a repo"},
		"bad url":    {"policy": "auto", "httpUrl": "ftp://x"},
		"bad window": {"policy": "auto", "verifyWindowSeconds": -5},
	} {
		if rec := h.do("PUT", "/api/containers/sonarr/settings", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec := h.do("PUT", "/api/containers/nope/settings", map[string]any{"policy": "auto"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown container: %d", rec.Code)
	}
}
