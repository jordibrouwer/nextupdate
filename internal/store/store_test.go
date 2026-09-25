package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestHistoryRoundTrip(t *testing.T) {
	s := openTest(t)
	start := time.UnixMilli(1_700_000_000_000)
	for i, outcome := range []string{"ok", "rolled_back"} {
		_, err := s.AddHistory(History{
			Container: "app", Image: "app:latest", FromImage: "sha256:a", ToImage: "sha256:b",
			StartedAt: start.Add(time.Duration(i) * time.Minute), FinishedAt: start.Add(time.Duration(i)*time.Minute + time.Second),
			Outcome: outcome, Reason: "r", Log: []string{"pull app:latest", "stop app"},
		})
		if err != nil {
			t.Fatalf("add: %v", err)
		}
	}
	got, err := s.ListHistory(10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].Outcome != "rolled_back" {
		t.Fatalf("want newest first, got %+v", got)
	}
	if len(got[1].Log) != 2 || got[1].Log[1] != "stop app" || !got[1].StartedAt.Equal(start) {
		t.Fatalf("fields not preserved: %+v", got[1])
	}
}

func TestAvailableReplaceAndRemove(t *testing.T) {
	s := openTest(t)
	now := time.UnixMilli(1_700_000_000_000)
	if err := s.ReplaceAvailable([]Available{
		{Container: "b", Image: "b:1", LocalDigest: "sha256:1", RemoteDigest: "sha256:2", DetectedAt: now},
		{Container: "a", Image: "a:1", LocalDigest: "sha256:3", RemoteDigest: "sha256:4", DetectedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceAvailable([]Available{
		{Container: "a", Image: "a:1", LocalDigest: "sha256:3", RemoteDigest: "sha256:5", DetectedAt: now},
		{Container: "c", Image: "c:1", LocalDigest: "sha256:6", RemoteDigest: "sha256:7", DetectedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAvailable("c"); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListAvailable()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Container != "a" || got[0].RemoteDigest != "sha256:5" {
		t.Fatalf("got %+v", got)
	}
}

func TestJournalMergesAndCloses(t *testing.T) {
	j := openTest(t).Journal()
	id, err := j.Begin("app", "run", map[string]string{"old_id": "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Step(id, "created_new", map[string]string{"new_id": "c2"}); err != nil {
		t.Fatal(err)
	}
	open, err := j.Open()
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 1 || open[0].Step != "created_new" || open[0].Data["old_id"] != "c1" || open[0].Data["new_id"] != "c2" || open[0].Adapter != "run" {
		t.Fatalf("got %+v", open)
	}
	if err := j.Close(id); err != nil {
		t.Fatal(err)
	}
	if open, _ = j.Open(); len(open) != 0 {
		t.Fatalf("closed entry still open: %+v", open)
	}
}
