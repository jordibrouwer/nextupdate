package store

import (
	"database/sql"
	"errors"
	"time"
)

var ErrNotFound = errors.New("not found")

type User struct {
	ID           int64
	Name         string
	PasswordHash string
}

func (s *Store) CountUsers() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func (s *Store) CreateUser(name, hash string) (User, error) {
	r, err := s.db.Exec(`INSERT INTO users (name, password_hash, created_at) VALUES (?, ?, ?)`, name, hash, time.Now().UnixMilli())
	if err != nil {
		return User{}, err
	}
	id, err := r.LastInsertId()
	return User{ID: id, Name: name, PasswordHash: hash}, err
}

func (s *Store) GetUserByName(name string) (User, error) {
	var u User
	err := s.db.QueryRow(`SELECT id, name, password_hash FROM users WHERE name = ?`, name).Scan(&u.ID, &u.Name, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) GetUserByID(id int64) (User, error) {
	var u User
	err := s.db.QueryRow(`SELECT id, name, password_hash FROM users WHERE id = ?`, id).Scan(&u.ID, &u.Name, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	return u, err
}

func (s *Store) CreateSession(tokenHash string, userID int64, expires time.Time) error {
	_, err := s.db.Exec(`INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?, ?, ?)`, tokenHash, userID, expires.UnixMilli())
	return err
}

func (s *Store) SessionUser(tokenHash string, now time.Time) (int64, error) {
	var uid int64
	err := s.db.QueryRow(`SELECT user_id FROM sessions WHERE token_hash = ? AND expires_at > ?`, tokenHash, now.UnixMilli()).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return uid, err
}

func (s *Store) DeleteSession(tokenHash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE token_hash = ?`, tokenHash)
	return err
}

func (s *Store) DeleteExpiredSessions(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at <= ?`, now.UnixMilli())
	return err
}

// GetSetting returns "" for a key that was never set.
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
