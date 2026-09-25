package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Notifier is one configured notification target. Config holds the
// type-specific fields (URL, token, ...); secrets are stored in plain text.
type Notifier struct {
	ID      int64
	Name    string
	Type    string
	Config  map[string]string
	Enabled bool
}

func (s *Store) AddNotifier(n Notifier) (int64, error) {
	cfg, err := json.Marshal(nonNilMap(n.Config))
	if err != nil {
		return 0, err
	}
	r, err := s.db.Exec(`INSERT INTO notifiers (name, type, config, enabled) VALUES (?, ?, ?, ?)`, n.Name, n.Type, string(cfg), n.Enabled)
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func scanNotifier(sc interface{ Scan(...any) error }) (Notifier, error) {
	var n Notifier
	var cfg string
	if err := sc.Scan(&n.ID, &n.Name, &n.Type, &cfg, &n.Enabled); err != nil {
		return Notifier{}, err
	}
	if err := json.Unmarshal([]byte(cfg), &n.Config); err != nil {
		return Notifier{}, fmt.Errorf("notifier %d config: %w", n.ID, err)
	}
	return n, nil
}

func (s *Store) ListNotifiers() ([]Notifier, error) {
	rows, err := s.db.Query(`SELECT id, name, type, config, enabled FROM notifiers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Notifier
	for rows.Next() {
		n, err := scanNotifier(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Store) GetNotifier(id int64) (Notifier, error) {
	n, err := scanNotifier(s.db.QueryRow(`SELECT id, name, type, config, enabled FROM notifiers WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Notifier{}, ErrNotFound
	}
	return n, err
}

func (s *Store) UpdateNotifier(n Notifier) error {
	cfg, err := json.Marshal(nonNilMap(n.Config))
	if err != nil {
		return err
	}
	r, err := s.db.Exec(`UPDATE notifiers SET name = ?, type = ?, config = ?, enabled = ? WHERE id = ?`, n.Name, n.Type, string(cfg), n.Enabled, n.ID)
	if err != nil {
		return err
	}
	if c, _ := r.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteNotifier(id int64) error {
	_, err := s.db.Exec(`DELETE FROM notifiers WHERE id = ?`, id)
	return err
}

func nonNilMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// PushSub is one browser's Web Push subscription.
type PushSub struct {
	Endpoint string
	P256dh   string
	Auth     string
	UserID   int64
}

func (s *Store) AddPushSub(p PushSub) error {
	_, err := s.db.Exec(`INSERT INTO push_subscriptions (endpoint, p256dh, auth, user_id, created_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth, user_id = excluded.user_id`,
		p.Endpoint, p.P256dh, p.Auth, p.UserID, time.Now().UnixMilli())
	return err
}

func (s *Store) ListPushSubs() ([]PushSub, error) {
	rows, err := s.db.Query(`SELECT endpoint, p256dh, auth, user_id FROM push_subscriptions ORDER BY created_at, endpoint`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSub
	for rows.Next() {
		var p PushSub
		if err := rows.Scan(&p.Endpoint, &p.P256dh, &p.Auth, &p.UserID); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeletePushSub(endpoint string) error {
	_, err := s.db.Exec(`DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	return err
}
