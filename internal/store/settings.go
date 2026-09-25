package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	PolicyNotify = "notify"
	PolicyAuto   = "auto"
	PolicyNever  = "never"
)

func ValidPolicy(p string) bool {
	return p == PolicyNotify || p == PolicyAuto || p == PolicyNever
}

// Settings are the per-container options. A container without a row gets
// the defaults: notify only, no HTTP check, default verify window.
type Settings struct {
	Container    string
	Policy       string
	HTTPURL      string
	Repo         string // manual GitHub repo "owner/name"; beats label and mapping
	VerifyWindow time.Duration
}

func (s *Store) GetSettings(container string) (Settings, error) {
	st := Settings{Container: container, Policy: PolicyNotify}
	var window int64
	err := s.db.QueryRow(`SELECT policy, http_url, repo, verify_window_seconds FROM container_settings WHERE container = ?`, container).
		Scan(&st.Policy, &st.HTTPURL, &st.Repo, &window)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.VerifyWindow = time.Duration(window) * time.Second
	return st, nil
}

func (s *Store) SetSettings(st Settings) error {
	if !ValidPolicy(st.Policy) {
		return fmt.Errorf("invalid policy %q", st.Policy)
	}
	_, err := s.db.Exec(`INSERT INTO container_settings (container, policy, http_url, repo, verify_window_seconds)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(container) DO UPDATE SET policy = excluded.policy, http_url = excluded.http_url,
			repo = excluded.repo, verify_window_seconds = excluded.verify_window_seconds`,
		st.Container, st.Policy, st.HTTPURL, st.Repo, int64(st.VerifyWindow/time.Second))
	return err
}
