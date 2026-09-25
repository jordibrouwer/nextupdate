package store

import "testing"

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
