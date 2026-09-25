package notify

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/push"
	"github.com/jordibrouwer/nextupdate/internal/scheduler"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

// Format turns a scheduler event into a message.
func Format(ev scheduler.Event, baseURL string) Message {
	versions := ""
	if ev.OldVersion != "" && ev.NewVersion != "" {
		versions = " (" + ev.OldVersion + " to " + ev.NewVersion + ")"
	}
	m := Message{URL: baseURL, Breaking: ev.Breaking}
	var lines []string
	switch ev.Kind {
	case scheduler.KindAvailable:
		head := "Update available"
		if ev.Breaking {
			head = "Breaking update available"
		}
		m.Title = head + ": " + ev.Container + versions
		lines = append(lines, ev.Detail)
		lines = append(lines, ev.Reasons...)
	case scheduler.KindUpdated:
		m.Title = "Updated " + ev.Container
		if ev.NewVersion != "" {
			m.Title += " to " + ev.NewVersion
		}
	case scheduler.KindRolledBack:
		m.Title = "Update of " + ev.Container + " was rolled back"
		lines = append(lines, ev.Detail)
	default:
		m.Title = "Update of " + ev.Container + " failed"
		lines = append(lines, ev.Detail)
	}
	var kept []string
	for _, l := range lines {
		if l != "" {
			kept = append(kept, l)
		}
	}
	m.Body = strings.Join(kept, "\n")
	return m
}

// Dispatcher sends events to every enabled notifier and every push
// subscription. It implements scheduler.Notifier.
type Dispatcher struct {
	Store   *store.Store
	Push    *push.Sender // nil disables Web Push
	Client  *http.Client
	Log     *log.Logger
	BaseURL string
}

var _ scheduler.Notifier = (*Dispatcher)(nil)

func (d *Dispatcher) logf(format string, a ...any) {
	l := d.Log
	if l == nil {
		l = log.Default()
	}
	l.Printf(format, a...)
}

func (d *Dispatcher) Notify(ctx context.Context, ev scheduler.Event) {
	// Always leave a trace in the log, also when no notifier is set up.
	scheduler.LogNotifier{Log: d.Log}.Notify(ctx, ev)
	msg := Format(ev, d.BaseURL)

	notifiers, err := d.Store.ListNotifiers()
	if err != nil {
		d.logf("notify: list notifiers: %v", err)
	}
	for _, n := range notifiers {
		if !n.Enabled {
			continue
		}
		s, err := Build(n.Type, n.Config, d.Client)
		if err != nil {
			d.logf("notify: notifier %q: %v", n.Name, err)
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		if err := s.Send(cctx, msg); err != nil {
			d.logf("notify: notifier %q: %v", n.Name, err)
		}
		cancel()
	}

	if d.Push == nil {
		return
	}
	subs, err := d.Store.ListPushSubs()
	if err != nil {
		d.logf("notify: list push subscriptions: %v", err)
		return
	}
	payload, _ := json.Marshal(map[string]any{"title": msg.Title, "body": msg.Body, "url": msg.URL, "container": ev.Container, "kind": ev.Kind})
	for _, sub := range subs {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := d.Push.Send(cctx, sub, payload)
		cancel()
		switch {
		case errors.Is(err, push.ErrGone):
			if err := d.Store.DeletePushSub(sub.Endpoint); err != nil {
				d.logf("notify: delete push subscription: %v", err)
			}
		case err != nil:
			d.logf("notify: push: %v", err)
		}
	}
}
