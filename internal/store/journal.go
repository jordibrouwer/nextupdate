package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// Journal records the steps of an update in flight, so a crash can be
// repaired on the next start.
type Journal struct{ db *sql.DB }

func (s *Store) Journal() *Journal { return &Journal{db: s.db} }

type JournalEntry struct {
	ID        int64
	Container string
	Adapter   string
	Step      string
	Data      map[string]string
}

func (j *Journal) Begin(container, adapter string, data map[string]string) (int64, error) {
	b, err := json.Marshal(nonNil(data))
	if err != nil {
		return 0, err
	}
	r, err := j.db.Exec(`INSERT INTO journal (container, adapter, step, data) VALUES (?, ?, 'started', ?)`, container, adapter, string(b))
	if err != nil {
		return 0, err
	}
	return r.LastInsertId()
}

func (j *Journal) Step(id int64, step string, data map[string]string) error {
	var raw string
	if err := j.db.QueryRow(`SELECT data FROM journal WHERE id = ?`, id).Scan(&raw); err != nil {
		return fmt.Errorf("journal %d: %w", id, err)
	}
	merged := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &merged); err != nil {
		return fmt.Errorf("journal %d data: %w", id, err)
	}
	for k, v := range data {
		merged[k] = v
	}
	b, err := json.Marshal(merged)
	if err != nil {
		return err
	}
	_, err = j.db.Exec(`UPDATE journal SET step = ?, data = ? WHERE id = ?`, step, string(b), id)
	return err
}

func (j *Journal) Close(id int64) error {
	_, err := j.db.Exec(`UPDATE journal SET closed = 1 WHERE id = ?`, id)
	return err
}

func (j *Journal) Open() ([]JournalEntry, error) {
	rows, err := j.db.Query(`SELECT id, container, adapter, step, data FROM journal WHERE closed = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []JournalEntry
	for rows.Next() {
		var e JournalEntry
		var raw string
		if err := rows.Scan(&e.ID, &e.Container, &e.Adapter, &e.Step, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(raw), &e.Data); err != nil {
			return nil, fmt.Errorf("journal %d data: %w", e.ID, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
