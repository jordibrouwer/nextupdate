package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Info describes an available update: versions, the GitHub repo the notes
// come from, and whether the update looks breaking (and why).
type Info struct {
	Container  string
	OldVersion string
	NewVersion string
	Repo       string
	Breaking   bool
	Reasons    []string
}

func (s *Store) ReplaceInfo(list []Info) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM available_info`); err != nil {
		return err
	}
	for _, i := range list {
		reasons, err := json.Marshal(nonNilStrings(i.Reasons))
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT INTO available_info (container, old_version, new_version, repo, breaking, reasons) VALUES (?, ?, ?, ?, ?, ?)`,
			i.Container, i.OldVersion, i.NewVersion, i.Repo, i.Breaking, string(reasons)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ListInfo() ([]Info, error) {
	rows, err := s.db.Query(`SELECT container, old_version, new_version, repo, breaking, reasons FROM available_info ORDER BY container`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Info
	for rows.Next() {
		var i Info
		var reasons string
		if err := rows.Scan(&i.Container, &i.OldVersion, &i.NewVersion, &i.Repo, &i.Breaking, &reasons); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(reasons), &i.Reasons); err != nil {
			return nil, fmt.Errorf("info %s reasons: %w", i.Container, err)
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// ChangelogCache stores the last GitHub response per repo.
type ChangelogCache struct{ db *sql.DB }

func (s *Store) ChangelogCache() *ChangelogCache { return &ChangelogCache{db: s.db} }

func (c *ChangelogCache) Get(repo string) (etag string, body []byte, ok bool) {
	if err := c.db.QueryRow(`SELECT etag, body FROM changelog_cache WHERE repo = ?`, repo).Scan(&etag, &body); err != nil {
		return "", nil, false
	}
	return etag, body, true
}

func (c *ChangelogCache) Put(repo, etag string, body []byte) error {
	_, err := c.db.Exec(`INSERT INTO changelog_cache (repo, etag, body, fetched_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(repo) DO UPDATE SET etag = excluded.etag, body = excluded.body, fetched_at = excluded.fetched_at`,
		repo, etag, body, time.Now().UnixMilli())
	return err
}

// OldImage is an image a successful update left behind. It is kept until
// RemoveAfter, so a manual rollback stays possible for a while.
type OldImage struct {
	ImageID     string
	Container   string
	RemoveAfter time.Time
}

func (s *Store) AddOldImage(o OldImage) error {
	_, err := s.db.Exec(`INSERT INTO old_images (image_id, container, remove_after) VALUES (?, ?, ?)
		ON CONFLICT(image_id) DO UPDATE SET container = excluded.container, remove_after = excluded.remove_after`,
		o.ImageID, o.Container, o.RemoveAfter.UnixMilli())
	return err
}

func (s *Store) DueOldImages(now time.Time) ([]OldImage, error) {
	rows, err := s.db.Query(`SELECT image_id, container, remove_after FROM old_images WHERE remove_after <= ? ORDER BY remove_after`, now.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OldImage
	for rows.Next() {
		var o OldImage
		var after int64
		if err := rows.Scan(&o.ImageID, &o.Container, &after); err != nil {
			return nil, err
		}
		o.RemoveAfter = time.UnixMilli(after)
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *Store) DeleteOldImage(imageID string) error {
	_, err := s.db.Exec(`DELETE FROM old_images WHERE image_id = ?`, imageID)
	return err
}
