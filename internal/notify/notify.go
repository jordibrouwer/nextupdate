// Package notify sends update messages to ntfy, Gotify, Discord, Telegram,
// webhooks and e-mail.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

type Message struct {
	Title    string
	Body     string
	URL      string
	Breaking bool
}

type Sender interface {
	Send(ctx context.Context, m Message) error
}

type Field struct {
	Key      string
	Label    string
	Secret   bool
	Required bool
}

type TypeInfo struct {
	Type   string
	Label  string
	Fields []Field
}

var types = []TypeInfo{
	{"ntfy", "ntfy", []Field{{"url", "Server URL", false, true}, {"topic", "Topic", false, true}, {"token", "Access token", true, false}}},
	{"gotify", "Gotify", []Field{{"url", "Server URL", false, true}, {"token", "App token", true, true}}},
	{"discord", "Discord", []Field{{"webhook_url", "Webhook URL", true, true}}},
	{"telegram", "Telegram", []Field{{"bot_token", "Bot token", true, true}, {"chat_id", "Chat ID", false, true}, {"api_base", "API base URL", false, false}}},
	{"webhook", "Webhook", []Field{{"url", "URL", false, true}}},
	{"email", "E-mail", []Field{{"host", "SMTP host", false, true}, {"port", "SMTP port", false, false}, {"username", "Username", false, false}, {"password", "Password", true, false}, {"from", "From address", false, true}, {"to", "To address", false, true}}},
}

func Types() []TypeInfo { return append([]TypeInfo(nil), types...) }

func typeInfo(typ string) (TypeInfo, bool) {
	for _, t := range types {
		if t.Type == typ {
			return t, true
		}
	}
	return TypeInfo{}, false
}

func SecretKeys(typ string) map[string]bool {
	out := map[string]bool{}
	if t, ok := typeInfo(typ); ok {
		for _, f := range t.Fields {
			if f.Secret {
				out[f.Key] = true
			}
		}
	}
	return out
}

func Build(typ string, cfg map[string]string, client *http.Client) (Sender, error) {
	info, ok := typeInfo(typ)
	if !ok {
		return nil, fmt.Errorf("unknown notifier type %q", typ)
	}
	for _, f := range info.Fields {
		if f.Required && strings.TrimSpace(cfg[f.Key]) == "" {
			return nil, fmt.Errorf("missing %s", f.Label)
		}
	}
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	if typ == "email" {
		return emailSender{cfg: cfg}, nil
	}
	return httpSender{typ: typ, cfg: cfg, client: client}, nil
}

func SendTest(ctx context.Context, client *http.Client, n store.Notifier) error {
	s, err := Build(n.Type, n.Config, client)
	if err != nil {
		return err
	}
	return s.Send(ctx, Message{Title: "nextupdate test", Body: "This is a test message from nextupdate."})
}

// text joins the non-empty parts of a message, one per line.
func (m Message) text() string {
	var parts []string
	for _, p := range []string{m.Title, m.Body, m.URL} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, "\n")
}

func postJSON(ctx context.Context, client *http.Client, typ, url string, header map[string]string, body any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	if header == nil {
		header = map[string]string{}
	}
	header["Content-Type"] = "application/json"
	return do(ctx, client, typ, url, header, bytes.NewReader(b))
}

// do sends a POST. Errors never contain the URL, because it can hold a secret.
func do(ctx context.Context, client *http.Client, typ, url string, header map[string]string, body io.Reader) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, body)
	if err != nil {
		return fmt.Errorf("%s: invalid URL", typ)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: request failed", typ)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s: status %d", typ, resp.StatusCode)
	}
	return nil
}
