// Package push sends Web Push messages to subscribed browsers.
package push

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/jordibrouwer/nextupdate/internal/store"
)

// ErrGone means the browser removed the subscription; delete it.
var ErrGone = errors.New("push subscription is gone")

type Keys struct {
	Public  string
	Private string
}

// EnsureKeys returns the VAPID key pair, creating and storing it on first use.
func EnsureKeys(st *store.Store) (Keys, error) {
	pub, err := st.GetSetting("vapid_public")
	if err != nil {
		return Keys{}, err
	}
	priv, err := st.GetSetting("vapid_private")
	if err != nil {
		return Keys{}, err
	}
	if pub != "" && priv != "" {
		return Keys{Public: pub, Private: priv}, nil
	}
	priv, pub, err = webpush.GenerateVAPIDKeys()
	if err != nil {
		return Keys{}, fmt.Errorf("generate VAPID keys: %w", err)
	}
	if err := st.SetSetting("vapid_public", pub); err != nil {
		return Keys{}, err
	}
	if err := st.SetSetting("vapid_private", priv); err != nil {
		return Keys{}, err
	}
	return Keys{Public: pub, Private: priv}, nil
}

type Sender struct {
	Keys    Keys
	Subject string // "mailto:..." or an https URL that identifies this server
	Client  *http.Client
}

func (s *Sender) Send(ctx context.Context, sub store.PushSub, payload []byte) error {
	client := s.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &webpush.Options{
		Subscriber:      s.Subject,
		VAPIDPublicKey:  s.Keys.Public,
		VAPIDPrivateKey: s.Keys.Private,
		TTL:             24 * 60 * 60,
		HTTPClient:      client,
	})
	if err != nil {
		return errors.New("push: request failed")
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound:
		return ErrGone
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return fmt.Errorf("push: status %d", resp.StatusCode)
	}
	return nil
}
