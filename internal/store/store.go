// Package store keeps nextupdate's state in SQLite.
package store

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schema string

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type History struct {
	ID         int64
	Container  string
	Image      string
	FromImage  string
	ToImage    string
	StartedAt  time.Time
	FinishedAt time.Time
	Outcome    string
	Reason     string
	Log        []string
}

func (s *Store) AddHistory(h History) (int64, error) {
	logJSON, err := json.Marshal(h.Log)
	if err != nil {
		return 0, err
	}
	r, err := s.db.Exec(`INSERT INTO history (container, image, from_image, to_image, started_at, finished_at, outcome, reason, log)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		h.Container, h.Image, h.FromImage, h.ToImage, h.StartedAt.UnixMilli(), h.FinishedAt.UnixMilli(), h.Outcome, h.Reason, string(logJSON))
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (s *Store) ListHistory(limit int) ([]History, error) {
	rows, err := s.db.Query(`SELECT id, container, image, from_image, to_image, started_at, finished_at, outcome, reason, log
		FROM history ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []History
	for rows.Next() {
		var h History
		var started, finished int64
		var logJSON string
		if err := rows.Scan(&h.ID, &h.Container, &h.Image, &h.FromImage, &h.ToImage, &started, &finished, &h.Outcome, &h.Reason, &logJSON); err != nil {
			return nil, err
		}
		h.StartedAt, h.FinishedAt = time.UnixMilli(started), time.UnixMilli(finished)
		if err := json.Unmarshal([]byte(logJSON), &h.Log); err != nil {
			return nil, fmt.Errorf("history %d log: %w", h.ID, err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

type Available struct {
	Container    string
	Image        string
	LocalDigest  string
	RemoteDigest string
	DetectedAt   time.Time
}

func (s *Store) ReplaceAvailable(list []Available) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM available`); err != nil {
		return err
	}
	for _, a := range list {
		if _, err := tx.Exec(`INSERT INTO available (container, image, local_digest, remote_digest, detected_at) VALUES (?, ?, ?, ?, ?)`,
			a.Container, a.Image, a.LocalDigest, a.RemoteDigest, a.DetectedAt.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListAvailable() ([]Available, error) {
	rows, err := s.db.Query(`SELECT container, image, local_digest, remote_digest, detected_at FROM available ORDER BY container`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Available
	for rows.Next() {
		var a Available
		var detected int64
		if err := rows.Scan(&a.Container, &a.Image, &a.LocalDigest, &a.RemoteDigest, &detected); err != nil {
			return nil, err
		}
		a.DetectedAt = time.UnixMilli(detected)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) RemoveAvailable(container string) error {
	_, err := s.db.Exec(`DELETE FROM available WHERE container = ?`, container)
	return err
}
