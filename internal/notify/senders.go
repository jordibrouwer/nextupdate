package notify

import (
	"context"
	"net/http"
	"strings"
)

type httpSender struct {
	typ    string
	cfg    map[string]string
	client *http.Client
}

func (s httpSender) Send(ctx context.Context, m Message) error {
	c := s.cfg
	switch s.typ {
	case "ntfy":
		h := map[string]string{"Title": m.Title}
		if m.URL != "" {
			h["Click"] = m.URL
		}
		if m.Breaking {
			h["Priority"] = "4"
		}
		if c["token"] != "" {
			h["Authorization"] = "Bearer " + c["token"]
		}
		return do(ctx, s.client, s.typ, strings.TrimRight(c["url"], "/")+"/"+strings.Trim(c["topic"], "/"), h, strings.NewReader(m.Body))
	case "gotify":
		prio := 5
		if m.Breaking {
			prio = 8
		}
		return postJSON(ctx, s.client, s.typ, strings.TrimRight(c["url"], "/")+"/message", map[string]string{"X-Gotify-Key": c["token"]},
			map[string]any{"title": m.Title, "message": m.Body, "priority": prio})
	case "discord":
		return postJSON(ctx, s.client, s.typ, c["webhook_url"], nil, map[string]string{"content": m.text()})
	case "telegram":
		base := strings.TrimRight(c["api_base"], "/")
		if base == "" {
			base = "https://api.telegram.org"
		}
		return postJSON(ctx, s.client, s.typ, base+"/bot"+c["bot_token"]+"/sendMessage", nil,
			map[string]string{"chat_id": c["chat_id"], "text": m.text()})
	default: // webhook
		return postJSON(ctx, s.client, s.typ, c["url"], nil,
			map[string]any{"title": m.Title, "body": m.Body, "url": m.URL, "breaking": m.Breaking})
	}
}
