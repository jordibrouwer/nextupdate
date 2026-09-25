package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/push"
	"github.com/jordibrouwer/nextupdate/internal/scheduler"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

func TestFormat(t *testing.T) {
	cases := []struct {
		ev       scheduler.Event
		title    string
		bodyHas  string
		breaking bool
	}{
		{scheduler.Event{Kind: scheduler.KindAvailable, Container: "app", OldVersion: "1.0.0", NewVersion: "1.1.0"}, "Update available: app (1.0.0 to 1.1.0)", "", false},
		{scheduler.Event{Kind: scheduler.KindAvailable, Container: "app", OldVersion: "1.0.0", NewVersion: "2.0.0", Breaking: true, Reasons: []string{"Major version change from 1.0.0 to 2.0.0."}, Detail: "The policy for this container is notify."}, "Breaking update available: app (1.0.0 to 2.0.0)", "Major version change", true},
		{scheduler.Event{Kind: scheduler.KindAvailable, Container: "app"}, "Update available: app", "", false},
		{scheduler.Event{Kind: scheduler.KindUpdated, Container: "app", NewVersion: "1.1.0"}, "Updated app to 1.1.0", "", false},
		{scheduler.Event{Kind: scheduler.KindRolledBack, Container: "app", Detail: "verify: healthcheck unhealthy"}, "Update of app was rolled back", "healthcheck unhealthy", false},
		{scheduler.Event{Kind: scheduler.KindFailed, Container: "app", Detail: "docker unreachable"}, "Update of app failed", "docker unreachable", false},
	}
	for _, c := range cases {
		m := Format(c.ev, "https://nu.example")
		if m.Title != c.title || !strings.Contains(m.Body, c.bodyHas) || m.URL != "https://nu.example" || m.Breaking != c.breaking {
			t.Errorf("%s: got %+v", c.ev.Kind, m)
		}
	}
}

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestDispatcherFansOutAndSurvivesFailures(t *testing.T) {
	st := newStore(t)
	var mu sync.Mutex
	var hits []string
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		hits = append(hits, r.URL.Path+" "+string(b))
		mu.Unlock()
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()

	st.AddNotifier(store.Notifier{Name: "broken", Type: "webhook", Config: map[string]string{"url": bad.URL}, Enabled: true})
	st.AddNotifier(store.Notifier{Name: "off", Type: "webhook", Config: map[string]string{"url": good.URL + "/off"}, Enabled: false})
	st.AddNotifier(store.Notifier{Name: "ok", Type: "webhook", Config: map[string]string{"url": good.URL + "/ok"}, Enabled: true})

	var logs strings.Builder
	d := &Dispatcher{Store: st, BaseURL: "https://nu.example", Log: newLogger(&logs)}
	d.Notify(context.Background(), scheduler.Event{Kind: scheduler.KindUpdated, Container: "app", NewVersion: "1.1.0"})

	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 1 || !strings.HasPrefix(hits[0], "/ok ") {
		t.Fatalf("only the enabled, working notifier should receive it: %v", hits)
	}
	var body map[string]any
	json.Unmarshal([]byte(strings.TrimPrefix(hits[0], "/ok ")), &body)
	if body["title"] != "Updated app to 1.1.0" {
		t.Fatalf("body %v", body)
	}
	if !strings.Contains(logs.String(), "broken") || strings.Contains(logs.String(), bad.URL) {
		t.Fatalf("the failure must be logged by name, without the URL: %q", logs.String())
	}
}

func TestDispatcherPushesAndDropsGoneSubscriptions(t *testing.T) {
	st := newStore(t)
	keys, _ := push.EnsureKeys(st)
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) }))
	defer live.Close()
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(410) }))
	defer gone.Close()

	st.AddPushSub(newPushSub(t, live.URL+"/a"))
	st.AddPushSub(newPushSub(t, gone.URL+"/b"))

	d := &Dispatcher{Store: st, Push: &push.Sender{Keys: keys, Subject: "mailto:a@b.c"}, BaseURL: "https://nu.example", Log: newLogger(&strings.Builder{})}
	d.Notify(context.Background(), scheduler.Event{Kind: scheduler.KindAvailable, Container: "app"})

	left, _ := st.ListPushSubs()
	if len(left) != 1 || !strings.HasPrefix(left[0].Endpoint, live.URL) {
		t.Fatalf("the gone subscription must be deleted, the live one kept: %+v", left)
	}
}
