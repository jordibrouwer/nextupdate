package notify

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"io"
	"log"
	"testing"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

func newLogger(w io.Writer) *log.Logger { return log.New(w, "", 0) }

func newPushSub(t *testing.T, endpoint string) store.PushSub {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	rand.Read(authSecret)
	return store.PushSub{Endpoint: endpoint,
		P256dh: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(authSecret)}
}
