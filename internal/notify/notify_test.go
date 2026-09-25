package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

type capture struct {
	method, path, query string
	header              http.Header
	body                string
}

func server(t *testing.T, status int) (*httptest.Server, *capture) {
	t.Helper()
	c := &capture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.method, c.path, c.query, c.header, c.body = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Clone(), string(b)
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

var msg = Message{Title: "Update available: app", Body: "1.0.0 to 2.0.0", URL: "https://nu.example", Breaking: true}

func send(t *testing.T, typ string, cfg map[string]string) error {
	t.Helper()
	s, err := Build(typ, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s.Send(context.Background(), msg)
}

func TestNtfy(t *testing.T) {
	srv, c := server(t, 200)
	if err := send(t, "ntfy", map[string]string{"url": srv.URL + "/", "topic": "updates", "token": "tk"}); err != nil {
		t.Fatal(err)
	}
	if c.method != "POST" || c.path != "/updates" || c.body != "1.0.0 to 2.0.0" ||
		c.header.Get("Title") != "Update available: app" || c.header.Get("Click") != "https://nu.example" ||
		c.header.Get("Priority") != "4" || c.header.Get("Authorization") != "Bearer tk" {
		t.Fatalf("ntfy request: %+v", c)
	}
}

func TestGotify(t *testing.T) {
	srv, c := server(t, 200)
	if err := send(t, "gotify", map[string]string{"url": srv.URL, "token": "gt"}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal([]byte(c.body), &got)
	if c.path != "/message" || c.header.Get("X-Gotify-Key") != "gt" || got["title"] != "Update available: app" || got["priority"] != float64(8) {
		t.Fatalf("gotify request: %+v %v", c, got)
	}
}

func TestDiscordAndTelegramAndWebhook(t *testing.T) {
	srv, c := server(t, 204)
	if err := send(t, "discord", map[string]string{"webhook_url": srv.URL + "/hook"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.body, "Update available: app") || !strings.Contains(c.body, "https://nu.example") {
		t.Fatalf("discord body %s", c.body)
	}

	if err := send(t, "telegram", map[string]string{"bot_token": "123:abc", "chat_id": "42", "api_base": srv.URL}); err != nil {
		t.Fatal(err)
	}
	if c.path != "/bot123:abc/sendMessage" || !strings.Contains(c.body, `"chat_id":"42"`) {
		t.Fatalf("telegram: %s %s", c.path, c.body)
	}

	if err := send(t, "webhook", map[string]string{"url": srv.URL + "/wh"}); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	json.Unmarshal([]byte(c.body), &got)
	if c.path != "/wh" || got["breaking"] != true || got["url"] != "https://nu.example" {
		t.Fatalf("webhook: %s %v", c.path, got)
	}
}

func TestSendErrorHidesTheURL(t *testing.T) {
	srv, _ := server(t, 500)
	err := send(t, "discord", map[string]string{"webhook_url": srv.URL + "/secret-token"})
	if err == nil || !strings.Contains(err.Error(), "status 500") || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error %v", err)
	}
}

func TestBuildValidates(t *testing.T) {
	if _, err := Build("ntfy", map[string]string{"url": "https://x"}, nil); err == nil || !strings.Contains(err.Error(), "Topic") {
		t.Fatalf("missing topic: %v", err)
	}
	if _, err := Build("carrier-pigeon", nil, nil); err == nil || !strings.Contains(err.Error(), "unknown notifier type") {
		t.Fatalf("unknown type: %v", err)
	}
}

func TestTypesAndSecrets(t *testing.T) {
	types := Types()
	if len(types) != 6 || types[0].Type != "ntfy" || types[5].Type != "email" {
		t.Fatalf("types %+v", types)
	}
	if !SecretKeys("ntfy")["token"] || SecretKeys("ntfy")["url"] || !SecretKeys("email")["password"] {
		t.Fatal("secret keys wrong")
	}
}

func TestSendTest(t *testing.T) {
	srv, c := server(t, 200)
	err := SendTest(context.Background(), nil, store.Notifier{Type: "ntfy", Config: map[string]string{"url": srv.URL, "topic": "t"}})
	if err != nil || c.header.Get("Title") != "nextupdate test" {
		t.Fatalf("%v %+v", err, c)
	}
}
