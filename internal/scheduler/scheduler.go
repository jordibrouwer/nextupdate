// Package scheduler runs the check, decide, update loop.
package scheduler

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/policy"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
)

const (
	KindAvailable  = "update_available"
	KindUpdated    = "update_ok"
	KindRolledBack = "update_rolled_back"
	KindFailed     = "update_failed"
)

// Event is what a notifier is told about.
type Event struct {
	Kind       string
	Container  string
	Image      string
	OldVersion string
	NewVersion string
	Breaking   bool
	Reasons    []string
	Detail     string
}

type Notifier interface {
	Notify(ctx context.Context, e Event)
}

// LogNotifier writes events to a log; real targets come later.
type LogNotifier struct{ Log *log.Logger }

func (n LogNotifier) Notify(ctx context.Context, e Event) {
	l := n.Log
	if l == nil {
		l = log.Default()
	}
	msg := e.Kind + ": " + e.Container + " (" + e.Image + ")"
	if e.OldVersion != "" || e.NewVersion != "" {
		msg += " " + e.OldVersion + " to " + e.NewVersion
	}
	if e.Breaking {
		msg += " [breaking: " + strings.Join(e.Reasons, " ") + "]"
	}
	if e.Detail != "" {
		msg += " - " + e.Detail
	}
	l.Print(msg)
}

type Engine interface {
	Check(ctx context.Context) ([]store.Available, error)
	Update(ctx context.Context, name string) (store.History, error)
	Cleanup(ctx context.Context) error
}

type Scheduler struct {
	Engine   Engine
	Store    *store.Store
	Notifier Notifier
	Interval time.Duration
	Log      *log.Logger
}

func (s *Scheduler) logf(format string, a ...any) {
	l := s.Log
	if l == nil {
		l = log.Default()
	}
	l.Printf(format, a...)
}

// RunOnce does one cycle: find updates, announce or apply each one
// according to its policy, and clean up old images.
func (s *Scheduler) RunOnce(ctx context.Context) error {
	avail, err := s.Engine.Check(ctx)
	if err != nil {
		return err
	}
	infos, err := s.Store.ListInfo()
	if err != nil {
		return err
	}
	byName := map[string]store.Info{}
	for _, i := range infos {
		byName[i.Container] = i
	}
	for _, a := range avail {
		if ctx.Err() != nil {
			return nil
		}
		info := byName[a.Container]
		settings, err := s.Store.GetSettings(a.Container)
		if err != nil {
			s.logf("scheduler: settings of %s: %v", a.Container, err)
			continue
		}
		action, why := policy.Decide(settings, info, a.Image)
		ev := Event{Container: a.Container, Image: a.Image, OldVersion: info.OldVersion, NewVersion: info.NewVersion, Breaking: info.Breaking, Reasons: info.Reasons}
		switch action {
		case policy.Notify:
			s.announce(ctx, a, ev, why)
		case policy.Update:
			s.apply(ctx, a, ev)
		}
	}
	if err := s.Engine.Cleanup(ctx); err != nil {
		s.logf("scheduler: cleanup: %v", err)
	}
	return nil
}

func (s *Scheduler) announce(ctx context.Context, a store.Available, ev Event, why string) {
	if seen, _ := s.Store.Seen(a.Container, "available"); seen == a.RemoteDigest {
		return
	}
	ev.Kind, ev.Detail = KindAvailable, why
	s.Notifier.Notify(ctx, ev)
	if err := s.Store.MarkSeen(a.Container, "available", a.RemoteDigest); err != nil {
		s.logf("scheduler: mark seen %s: %v", a.Container, err)
	}
}

func (s *Scheduler) apply(ctx context.Context, a store.Available, ev Event) {
	if seen, _ := s.Store.Seen(a.Container, "attempted"); seen == a.RemoteDigest {
		return // this digest already failed or was tried; wait for a new one
	}
	if err := s.Store.MarkSeen(a.Container, "attempted", a.RemoteDigest); err != nil {
		s.logf("scheduler: mark attempted %s: %v", a.Container, err)
	}
	h, err := s.Engine.Update(ctx, a.Container)
	switch {
	case err != nil:
		ev.Kind, ev.Detail = KindFailed, err.Error()
	case h.Outcome == updater.OutcomeOK:
		ev.Kind = KindUpdated
	case h.Outcome == updater.OutcomeRolledBack:
		ev.Kind, ev.Detail = KindRolledBack, h.Reason
	default:
		ev.Kind, ev.Detail = KindFailed, h.Reason
	}
	s.Notifier.Notify(ctx, ev)
}

// Run cycles until the context ends: once right away, then every Interval.
func (s *Scheduler) Run(ctx context.Context) error {
	t := time.NewTicker(s.Interval)
	defer t.Stop()
	for {
		if err := s.RunOnce(ctx); err != nil {
			s.logf("scheduler: cycle failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}
