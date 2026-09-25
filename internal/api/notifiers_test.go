package api

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/push"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

func TestNotifierTypes(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	types := decode[[]map[string]any](t, h.do("GET", "/api/notifiers/types", nil))
	if len(types) != 6 || types[0]["type"] != "ntfy" {
		t.Fatalf("types %v", types)
	}
	fields, _ := types[0]["fields"].([]any)
	if len(fields) != 3 {
		t.Fatalf("ntfy fields %v", fields)
	}
}

func TestNotifierCRUDMasksSecrets(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	rec := h.do("POST", "/api/notifiers", map[string]any{"name": "phone", "type": "ntfy", "enabled": true,
		"config": map[string]string{"url": "https://ntfy.sh", "topic": "updates", "token": "s3cret"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	created := decode[map[string]any](t, rec)
	cfg, _ := created["config"].(map[string]any)
	if cfg["token"] != "********" || cfg["topic"] != "updates" {
		t.Fatalf("created config %v", cfg)
	}
	id := int64(created["id"].(float64))
	stored, _ := h.st.GetNotifier(id)
	if stored.Config["token"] != "s3cret" {
		t.Fatalf("the real secret must be stored: %v", stored.Config)
	}

	list := decode[[]map[string]any](t, h.do("GET", "/api/notifiers", nil))
	if len(list) != 1 || list[0]["config"].(map[string]any)["token"] != "********" {
		t.Fatalf("list %v", list)
	}

	// Updating with the mask keeps the stored secret; a new value replaces it.
	put := func(token string) {
		rec := h.do("PUT", "/api/notifiers/"+itoa(id), map[string]any{"name": "phone", "type": "ntfy", "enabled": false,
			"config": map[string]string{"url": "https://ntfy.sh", "topic": "updates", "token": token}})
		if rec.Code != http.StatusOK {
			t.Fatalf("update: %d %s", rec.Code, rec.Body)
		}
	}
	put("********")
	if got, _ := h.st.GetNotifier(id); got.Config["token"] != "s3cret" || got.Enabled {
		t.Fatalf("mask must keep the secret: %+v", got)
	}
	put("n3w")
	if got, _ := h.st.GetNotifier(id); got.Config["token"] != "n3w" {
		t.Fatalf("new secret not stored: %+v", got)
	}

	if rec := h.do("DELETE", "/api/notifiers/"+itoa(id), nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := h.do("PUT", "/api/notifiers/"+itoa(id), map[string]any{"name": "x", "type": "ntfy", "config": map[string]string{"url": "https://a", "topic": "t"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("update of a deleted notifier: %d", rec.Code)
	}
}

func TestNotifierValidation(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	for name, body := range map[string]map[string]any{
		"missing topic": {"name": "n", "type": "ntfy", "config": map[string]string{"url": "https://a"}},
		"unknown type":  {"name": "n", "type": "pigeon", "config": map[string]string{}},
		"empty name":    {"name": " ", "type": "webhook", "config": map[string]string{"url": "https://a"}},
	} {
		if rec := h.do("POST", "/api/notifiers", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestNotifierTestSend(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.Header.Get("Title") }))
	defer srv.Close()
	id, _ := h.st.AddNotifier(store.Notifier{Name: "n", Type: "ntfy", Config: map[string]string{"url": srv.URL, "topic": "t"}, Enabled: true})
	if rec := h.do("POST", "/api/notifiers/"+itoa(id)+"/test", nil); rec.Code != http.StatusOK || got != "nextupdate test" {
		t.Fatalf("test send: %d %s (title %q)", rec.Code, rec.Body, got)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	id2, _ := h.st.AddNotifier(store.Notifier{Name: "b", Type: "ntfy", Config: map[string]string{"url": bad.URL, "topic": "t"}, Enabled: true})
	if rec := h.do("POST", "/api/notifiers/"+itoa(id2)+"/test", nil); rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "status 500") {
		t.Fatalf("failing test send: %d %s", rec.Code, rec.Body)
	}
}

func TestPushEndpoints(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	if rec := h.do("GET", "/api/push/key", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("push not configured: %d", rec.Code)
	}
	keys, _ := push.EnsureKeys(h.st)
	h.srv.d.Push = &push.Sender{Keys: keys, Subject: "mailto:a@b.c"}
	if k := decode[map[string]string](t, h.do("GET", "/api/push/key", nil)); k["publicKey"] != keys.Public {
		t.Fatalf("key %v", k)
	}

	sub := map[string]any{"endpoint": "https://push.example/abc", "keys": map[string]string{"p256dh": "P", "auth": "A"}}
	if rec := h.do("POST", "/api/push/subscribe", sub); rec.Code != http.StatusCreated {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body)
	}
	if list, _ := h.st.ListPushSubs(); len(list) != 1 || list[0].P256dh != "P" || list[0].UserID == 0 {
		t.Fatalf("stored %+v", list)
	}
	for name, body := range map[string]map[string]any{
		"http endpoint": {"endpoint": "http://push.example/x", "keys": map[string]string{"p256dh": "P", "auth": "A"}},
		"missing keys":  {"endpoint": "https://push.example/x", "keys": map[string]string{"p256dh": "P"}},
	} {
		if rec := h.do("POST", "/api/push/subscribe", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if rec := h.do("POST", "/api/push/unsubscribe", map[string]string{"endpoint": "https://push.example/abc"}); rec.Code != http.StatusOK {
		t.Fatalf("unsubscribe: %d", rec.Code)
	}
	if list, _ := h.st.ListPushSubs(); len(list) != 0 {
		t.Fatalf("still subscribed: %+v", list)
	}
}

func TestWidget(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	h.st.ReplaceAvailable([]store.Available{
		{Container: "a", Image: "a:1", RemoteDigest: "sha256:1", DetectedAt: time.Now()},
		{Container: "b", Image: "b:1", RemoteDigest: "sha256:2", DetectedAt: time.Now()},
		{Container: "c", Image: "c:1", RemoteDigest: "sha256:3", DetectedAt: time.Now()},
	})
	h.st.ReplaceInfo([]store.Info{{Container: "b", Breaking: true}})
	h.st.SetSetting("last_check", "2026-09-25T18:00:00Z")
	token := decode[map[string]string](t, h.do("GET", "/api/widget/token", nil))["token"]
	if len(token) < 32 {
		t.Fatalf("token %q", token)
	}
	if again := decode[map[string]string](t, h.do("GET", "/api/widget/token", nil))["token"]; again != token {
		t.Fatal("the token must be stable until rotated")
	}

	h.cookie = nil // the widget is read without a session
	get := func(auth, query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/widget"+query, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		h.srv.ServeHTTP(rec, req)
		return rec
	}
	rec := get("Bearer "+token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("widget with bearer: %d %s", rec.Code, rec.Body)
	}
	w := decode[map[string]any](t, rec)
	if w["updates"] != float64(3) || w["breaking"] != float64(1) || w["lastCheck"] != "2026-09-25T18:00:00Z" || w["url"] != "https://nu.example" {
		t.Fatalf("widget %v", w)
	}
	if rec := get("", "?token="+token); rec.Code != http.StatusOK {
		t.Fatalf("widget with query token: %d", rec.Code)
	}
	if rec := get("Bearer wrong", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", rec.Code)
	}
	if rec := get("", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: %d", rec.Code)
	}
}

func TestWidgetTokenRotation(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	old := decode[map[string]string](t, h.do("GET", "/api/widget/token", nil))["token"]
	fresh := decode[map[string]string](t, h.do("POST", "/api/widget/token/rotate", nil))["token"]
	if fresh == "" || fresh == old {
		t.Fatalf("old %q new %q", old, fresh)
	}
	req := httptest.NewRequest("GET", "/api/widget?token="+old, nil)
	rec := httptest.NewRecorder()
	h.srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("the old token must stop working: %d", rec.Code)
	}
}

func testPushSub(t *testing.T, endpoint string) store.PushSub {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	rand.Read(authSecret)
	return store.PushSub{Endpoint: endpoint,
		P256dh: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(authSecret)}
}

func TestPushTest(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	if rec := h.do("POST", "/api/push/test", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("push not configured: %d", rec.Code)
	}
	keys, _ := push.EnsureKeys(h.st)
	h.srv.d.Push = &push.Sender{Keys: keys, Subject: "mailto:a@b.c"}
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201) }))
	defer live.Close()
	gone := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(410) }))
	defer gone.Close()
	h.st.AddPushSub(testPushSub(t, live.URL+"/a"))
	h.st.AddPushSub(testPushSub(t, gone.URL+"/b"))

	got := decode[map[string]int](t, h.do("POST", "/api/push/test", nil))
	if got["sent"] != 1 || got["removed"] != 1 {
		t.Fatalf("result %v", got)
	}
	if left, _ := h.st.ListPushSubs(); len(left) != 1 {
		t.Fatalf("the gone subscription must be deleted: %+v", left)
	}
}
