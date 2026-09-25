package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

func newSub(t *testing.T, endpoint string) store.PushSub {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	rand.Read(authSecret)
	return store.PushSub{
		Endpoint: endpoint,
		P256dh:   base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:     base64.RawURLEncoding.EncodeToString(authSecret),
	}
}

func TestEnsureKeysCreatesOnceAndReuses(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a, err := EnsureKeys(st)
	if err != nil || a.Public == "" || a.Private == "" {
		t.Fatalf("first: %+v %v", a, err)
	}
	b, err := EnsureKeys(st)
	if err != nil || a != b {
		t.Fatalf("second call must return the same keys: %+v vs %+v (%v)", a, b, err)
	}
}

func TestSendPostsAnEncryptedMessage(t *testing.T) {
	var gotAuth, gotEnc, gotTTL string
	var gotLen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotEnc, gotTTL = r.Header.Get("Authorization"), r.Header.Get("Content-Encoding"), r.Header.Get("TTL")
		buf := make([]byte, 8192)
		gotLen, _ = r.Body.Read(buf)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	st, _ := store.Open(filepath.Join(t.TempDir(), "p.db"))
	defer st.Close()
	keys, _ := EnsureKeys(st)
	s := &Sender{Keys: keys, Subject: "mailto:admin@example.com"}

	if err := s.Send(context.Background(), newSub(t, srv.URL+"/push/1"), []byte(`{"title":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	if gotAuth == "" || gotEnc != "aes128gcm" || gotTTL == "" || gotLen < 20 {
		t.Fatalf("auth %q enc %q ttl %q len %d", gotAuth, gotEnc, gotTTL, gotLen)
	}
}

func TestSendReportsGoneSubscriptions(t *testing.T) {
	for _, status := range []int{http.StatusGone, http.StatusNotFound} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		st, _ := store.Open(filepath.Join(t.TempDir(), "p.db"))
		keys, _ := EnsureKeys(st)
		err := (&Sender{Keys: keys, Subject: "mailto:a@b.c"}).Send(context.Background(), newSub(t, srv.URL), []byte("x"))
		srv.Close()
		st.Close()
		if !errors.Is(err, ErrGone) {
			t.Errorf("status %d: want ErrGone, got %v", status, err)
		}
	}
}

func TestSendFailsOnServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer srv.Close()
	st, _ := store.Open(filepath.Join(t.TempDir(), "p.db"))
	defer st.Close()
	keys, _ := EnsureKeys(st)
	err := (&Sender{Keys: keys, Subject: "mailto:a@b.c"}).Send(context.Background(), newSub(t, srv.URL), []byte("x"))
	if err == nil || errors.Is(err, ErrGone) {
		t.Fatalf("want a plain failure, got %v", err)
	}
}
