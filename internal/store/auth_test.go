package store

import (
	"errors"
	"testing"
	"time"
)

func TestUsers(t *testing.T) {
	s := openTest(t)
	if n, err := s.CountUsers(); err != nil || n != 0 {
		t.Fatalf("count %d %v", n, err)
	}
	u, err := s.CreateUser("Jordi", "hash1")
	if err != nil || u.ID == 0 {
		t.Fatalf("create %+v %v", u, err)
	}
	if _, err := s.CreateUser("jordi", "hash2"); err == nil {
		t.Fatal("duplicate name (case-insensitive) accepted")
	}
	got, err := s.GetUserByName("JORDI")
	if err != nil || got.ID != u.ID || got.PasswordHash != "hash1" || got.Name != "Jordi" {
		t.Fatalf("get %+v %v", got, err)
	}
	if _, err := s.GetUserByName("nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestGetUserByID(t *testing.T) {
	s := openTest(t)
	u, _ := s.CreateUser("jordi", "h")
	got, err := s.GetUserByID(u.ID)
	if err != nil || got.Name != "jordi" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := s.GetUserByID(999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSessions(t *testing.T) {
	s := openTest(t)
	now := time.UnixMilli(1_700_000_000_000)
	if err := s.CreateSession("h1", 7, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession("h2", 7, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if uid, err := s.SessionUser("h1", now); err != nil || uid != 7 {
		t.Fatalf("valid session: %d %v", uid, err)
	}
	if _, err := s.SessionUser("h2", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session: %v", err)
	}
	if _, err := s.SessionUser("nope", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown session: %v", err)
	}
	if err := s.DeleteExpiredSessions(now); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSession("h1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionUser("h1", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session still valid: %v", err)
	}
}

func TestSettingsKV(t *testing.T) {
	s := openTest(t)
	if v, err := s.GetSetting("k"); err != nil || v != "" {
		t.Fatalf("missing: %q %v", v, err)
	}
	s.SetSetting("k", "1")
	s.SetSetting("k", "2")
	if v, _ := s.GetSetting("k"); v != "2" {
		t.Fatalf("got %q", v)
	}
}
