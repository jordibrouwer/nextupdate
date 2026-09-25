package store

import (
	"errors"
	"testing"
)

func TestNotifiersCRUD(t *testing.T) {
	s := openTest(t)
	id, err := s.AddNotifier(Notifier{Name: "phone", Type: "ntfy", Config: map[string]string{"url": "https://ntfy.sh", "topic": "t"}, Enabled: true})
	if err != nil || id == 0 {
		t.Fatal(id, err)
	}
	s.AddNotifier(Notifier{Name: "chat", Type: "discord", Config: map[string]string{"webhook_url": "https://x"}, Enabled: false})
	got, err := s.GetNotifier(id)
	if err != nil || got.Name != "phone" || got.Config["topic"] != "t" || !got.Enabled {
		t.Fatalf("get %+v %v", got, err)
	}
	got.Name, got.Enabled = "phone 2", false
	if err := s.UpdateNotifier(got); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListNotifiers()
	if len(list) != 2 || list[0].Name != "phone 2" || list[0].Enabled || list[1].Type != "discord" {
		t.Fatalf("list %+v", list)
	}
	if err := s.DeleteNotifier(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNotifier(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestPushSubs(t *testing.T) {
	s := openTest(t)
	s.AddPushSub(PushSub{Endpoint: "https://push/1", P256dh: "k1", Auth: "a1", UserID: 1})
	s.AddPushSub(PushSub{Endpoint: "https://push/1", P256dh: "k2", Auth: "a2", UserID: 1}) // same endpoint: replaced
	s.AddPushSub(PushSub{Endpoint: "https://push/2", P256dh: "k3", Auth: "a3", UserID: 1})
	list, err := s.ListPushSubs()
	if err != nil || len(list) != 2 || list[0].P256dh != "k2" {
		t.Fatalf("list %+v %v", list, err)
	}
	s.DeletePushSub("https://push/1")
	if list, _ = s.ListPushSubs(); len(list) != 1 || list[0].Endpoint != "https://push/2" {
		t.Fatalf("after delete %+v", list)
	}
}
