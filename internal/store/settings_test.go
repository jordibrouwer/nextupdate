package store

import (
	"testing"
	"time"
)

func TestSettingsDefaultsAndRoundTrip(t *testing.T) {
	s := openTest(t)
	got, err := s.GetSettings("app")
	if err != nil {
		t.Fatal(err)
	}
	if got.Policy != PolicyNotify || got.HTTPURL != "" || got.Repo != "" || got.VerifyWindow != 0 || got.Container != "app" {
		t.Fatalf("defaults: %+v", got)
	}
	want := Settings{Container: "app", Policy: PolicyAuto, HTTPURL: "http://app:8080/health", Repo: "owner/app", VerifyWindow: 90 * time.Second}
	if err := s.SetSettings(want); err != nil {
		t.Fatal(err)
	}
	want.Policy = PolicyNever
	if err := s.SetSettings(want); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetSettings("app")
	if err != nil || got != want {
		t.Fatalf("got %+v %v, want %+v", got, err, want)
	}
}

func TestSetSettingsRejectsInvalidPolicy(t *testing.T) {
	if err := openTest(t).SetSettings(Settings{Container: "app", Policy: "yolo"}); err == nil {
		t.Fatal("invalid policy accepted")
	}
}
