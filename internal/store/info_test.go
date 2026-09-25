package store

import (
	"testing"
	"time"
)

func TestInfoReplaceAndList(t *testing.T) {
	s := openTest(t)
	if err := s.ReplaceInfo([]Info{
		{Container: "b", OldVersion: "1.0.0", NewVersion: "2.0.0", Repo: "o/b", Breaking: true, Reasons: []string{"Major version change from 1.0.0 to 2.0.0."}},
		{Container: "a", OldVersion: "1.0.0", NewVersion: "1.0.1"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceInfo([]Info{
		{Container: "b", OldVersion: "1.0.0", NewVersion: "2.0.0", Repo: "o/b", Breaking: true, Reasons: []string{"Major version change from 1.0.0 to 2.0.0."}},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListInfo()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Container != "b" || !got[0].Breaking || got[0].Repo != "o/b" || len(got[0].Reasons) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestChangelogCache(t *testing.T) {
	c := openTest(t).ChangelogCache()
	if _, _, ok := c.Get("o/n"); ok {
		t.Fatal("empty cache returned a hit")
	}
	if err := c.Put("o/n", `"e1"`, []byte(`[1]`)); err != nil {
		t.Fatal(err)
	}
	if err := c.Put("o/n", `"e2"`, []byte(`[2]`)); err != nil {
		t.Fatal(err)
	}
	etag, body, ok := c.Get("o/n")
	if !ok || etag != `"e2"` || string(body) != `[2]` {
		t.Fatalf("got %q %q %v", etag, body, ok)
	}
}

func TestOldImages(t *testing.T) {
	s := openTest(t)
	base := time.UnixMilli(1_700_000_000_000)
	for _, o := range []OldImage{
		{ImageID: "sha256:a", Container: "app", RemoveAfter: base.Add(time.Hour)},
		{ImageID: "sha256:b", Container: "app", RemoveAfter: base.Add(48 * time.Hour)},
	} {
		if err := s.AddOldImage(o); err != nil {
			t.Fatal(err)
		}
	}
	due, err := s.DueOldImages(base.Add(2 * time.Hour))
	if err != nil || len(due) != 1 || due[0].ImageID != "sha256:a" {
		t.Fatalf("due %+v %v", due, err)
	}
	if err := s.DeleteOldImage("sha256:a"); err != nil {
		t.Fatal(err)
	}
	if due, _ = s.DueOldImages(base.Add(2 * time.Hour)); len(due) != 0 {
		t.Fatalf("still due: %+v", due)
	}
}

func TestSeen(t *testing.T) {
	s := openTest(t)
	if d, err := s.Seen("app", "available"); err != nil || d != "" {
		t.Fatalf("empty: %q %v", d, err)
	}
	if err := s.MarkSeen("app", "available", "sha256:1"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSeen("app", "attempted", "sha256:2"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSeen("app", "available", "sha256:3"); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.Seen("app", "available"); d != "sha256:3" {
		t.Fatalf("available: %q", d)
	}
	if d, _ := s.Seen("app", "attempted"); d != "sha256:2" {
		t.Fatalf("kinds must not mix: %q", d)
	}
}
