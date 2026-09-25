package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
)

type fakeEngine struct {
	avail   []store.Available
	outcome string
	err     error
	updated []string
	cleaned int
}

func (f *fakeEngine) Check(ctx context.Context) ([]store.Available, error) { return f.avail, nil }
func (f *fakeEngine) Update(ctx context.Context, name string) (store.History, error) {
	f.updated = append(f.updated, name)
	return store.History{Container: name, Outcome: f.outcome, Reason: "why"}, f.err
}
func (f *fakeEngine) Cleanup(ctx context.Context) error { f.cleaned++; return nil }

type recorder struct{ events []Event }

func (r *recorder) Notify(ctx context.Context, e Event) { r.events = append(r.events, e) }

func setup(t *testing.T, policy string, info store.Info) (*Scheduler, *fakeEngine, *recorder) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	info.Container = "app"
	if err := st.ReplaceInfo([]store.Info{info}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSettings(store.Settings{Container: "app", Policy: policy}); err != nil {
		t.Fatal(err)
	}
	eng := &fakeEngine{outcome: updater.OutcomeOK, avail: []store.Available{{Container: "app", Image: "app:latest", RemoteDigest: "sha256:new"}}}
	rec := &recorder{}
	return &Scheduler{Engine: eng, Store: st, Notifier: rec, Interval: time.Hour}, eng, rec
}

func TestNotifyPolicyAnnouncesOnce(t *testing.T) {
	s, eng, rec := setup(t, store.PolicyNotify, store.Info{OldVersion: "1.0.0", NewVersion: "1.0.1"})
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if err := s.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(eng.updated) != 0 {
		t.Fatalf("notify must not update: %v", eng.updated)
	}
	if len(rec.events) != 1 || rec.events[0].Kind != KindAvailable || rec.events[0].NewVersion != "1.0.1" {
		t.Fatalf("want one announcement, got %+v", rec.events)
	}
	if eng.cleaned != 2 {
		t.Fatalf("cleanup should run every cycle, ran %d", eng.cleaned)
	}
	// A newer digest is announced again.
	eng.avail[0].RemoteDigest = "sha256:newer"
	s.RunOnce(ctx)
	if len(rec.events) != 2 {
		t.Fatalf("new digest must be announced: %+v", rec.events)
	}
}

func TestAutoPolicyUpdates(t *testing.T) {
	s, eng, rec := setup(t, store.PolicyAuto, store.Info{OldVersion: "1.0.0", NewVersion: "1.1.0"})
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(eng.updated) != 1 || len(rec.events) != 1 || rec.events[0].Kind != KindUpdated {
		t.Fatalf("updated %v events %+v", eng.updated, rec.events)
	}
}

func TestAutoPolicyLeavesBreakingUpdatesToTheUser(t *testing.T) {
	s, eng, rec := setup(t, store.PolicyAuto, store.Info{OldVersion: "1.0.0", NewVersion: "2.0.0", Breaking: true, Reasons: []string{"Major version change from 1.0.0 to 2.0.0."}})
	s.RunOnce(context.Background())
	if len(eng.updated) != 0 || len(rec.events) != 1 || rec.events[0].Kind != KindAvailable || !rec.events[0].Breaking {
		t.Fatalf("updated %v events %+v", eng.updated, rec.events)
	}
}

func TestRolledBackUpdateIsNotRetriedForTheSameDigest(t *testing.T) {
	s, eng, rec := setup(t, store.PolicyAuto, store.Info{OldVersion: "1.0.0", NewVersion: "1.1.0"})
	eng.outcome = updater.OutcomeRolledBack
	ctx := context.Background()
	s.RunOnce(ctx)
	s.RunOnce(ctx)
	if len(eng.updated) != 1 {
		t.Fatalf("a rolled-back digest must not be retried: %v", eng.updated)
	}
	if len(rec.events) != 1 || rec.events[0].Kind != KindRolledBack {
		t.Fatalf("events %+v", rec.events)
	}
	eng.avail[0].RemoteDigest = "sha256:fixed"
	s.RunOnce(ctx)
	if len(eng.updated) != 2 {
		t.Fatalf("a new digest must be tried: %v", eng.updated)
	}
}

func TestUpdateErrorIsReported(t *testing.T) {
	s, eng, rec := setup(t, store.PolicyAuto, store.Info{OldVersion: "1.0.0", NewVersion: "1.1.0"})
	eng.err = errors.New("docker unreachable")
	s.RunOnce(context.Background())
	if len(rec.events) != 1 || rec.events[0].Kind != KindFailed || rec.events[0].Detail != "docker unreachable" {
		t.Fatalf("events %+v", rec.events)
	}
}

func TestNeverPolicyIsSilent(t *testing.T) {
	s, eng, rec := setup(t, store.PolicyNever, store.Info{OldVersion: "1.0.0", NewVersion: "1.0.1"})
	s.RunOnce(context.Background())
	if len(eng.updated) != 0 || len(rec.events) != 0 {
		t.Fatalf("updated %v events %+v", eng.updated, rec.events)
	}
}

func TestRunStopsWhenContextEnds(t *testing.T) {
	s, eng, _ := setup(t, store.PolicyNotify, store.Info{})
	s.Interval = 10 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	if err := s.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if eng.cleaned < 2 {
		t.Fatalf("Run should cycle repeatedly, cycled %d times", eng.cleaned)
	}
}
