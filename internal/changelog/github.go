package changelog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/semver"
)

type Release struct {
	Tag         string
	Name        string
	Body        string
	URL         string
	PublishedAt time.Time
	Prerelease  bool
}

type Source interface {
	Releases(ctx context.Context, repo string) ([]Release, error)
}

// Cache keeps the last response per repo, so a repeat call can use a
// conditional request.
type Cache interface {
	Get(repo string) (etag string, body []byte, ok bool)
	Put(repo, etag string, body []byte) error
}

var ErrRateLimited = errors.New("github rate limit reached")

type GitHub struct {
	Client  *http.Client
	BaseURL string // default https://api.github.com
	Token   string
	Cache   Cache
}

func (g *GitHub) Releases(ctx context.Context, repo string) ([]Release, error) {
	base := g.BaseURL
	if base == "" {
		base = "https://api.github.com"
	}
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/repos/"+repo+"/releases?per_page=30", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.Token != "" {
		req.Header.Set("Authorization", "Bearer "+g.Token)
	}
	var cached []byte
	if g.Cache != nil {
		if etag, body, ok := g.Cache.Get(repo); ok && etag != "" {
			req.Header.Set("If-None-Match", etag)
			cached = body
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github %s: %w", repo, err)
	}
	defer resp.Body.Close()

	var body []byte
	switch resp.StatusCode {
	case http.StatusOK:
		body, err = io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return nil, fmt.Errorf("github %s: %w", repo, err)
		}
		if g.Cache != nil {
			_ = g.Cache.Put(repo, resp.Header.Get("ETag"), body) // a cache miss next time is harmless
		}
	case http.StatusNotModified:
		if cached == nil {
			return nil, fmt.Errorf("github %s: 304 without a cached copy", repo)
		}
		body = cached
	case http.StatusNotFound:
		return nil, nil // no such repo, or private
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, ErrRateLimited
	default:
		return nil, fmt.Errorf("github %s: status %d", repo, resp.StatusCode)
	}
	return parseReleases(body)
}

func parseReleases(body []byte) ([]Release, error) {
	var raw []struct {
		Tag         string    `json:"tag_name"`
		Name        string    `json:"name"`
		Body        string    `json:"body"`
		URL         string    `json:"html_url"`
		PublishedAt time.Time `json:"published_at"`
		Prerelease  bool      `json:"prerelease"`
		Draft       bool      `json:"draft"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse releases: %w", err)
	}
	var out []Release
	for _, r := range raw {
		if r.Draft {
			continue
		}
		out = append(out, Release{Tag: r.Tag, Name: r.Name, Body: r.Body, URL: r.URL, PublishedAt: r.PublishedAt, Prerelease: r.Prerelease})
	}
	return out, nil
}

// Between returns the releases newer than oldV and up to newV, newest first.
// Without a known old version only the release of newV itself is returned;
// without a known new version nothing is. Tags that are not versions are
// skipped, and pre-releases only count when newV is one.
func Between(releases []Release, oldV, newV string) []Release {
	n, okN := semver.Parse(newV)
	if !okN {
		return nil
	}
	o, okO := semver.Parse(oldV)
	type item struct {
		r Release
		v semver.Version
	}
	var picked []item
	for _, r := range releases {
		v, ok := semver.Parse(r.Tag)
		if !ok || (r.Prerelease && n.Pre == "") {
			continue
		}
		if okO && semver.Compare(v, o) > 0 && semver.Compare(v, n) <= 0 || !okO && semver.Compare(v, n) == 0 {
			picked = append(picked, item{r, v})
		}
	}
	sort.SliceStable(picked, func(i, j int) bool { return semver.Compare(picked[i].v, picked[j].v) > 0 })
	var out []Release
	for _, p := range picked {
		out = append(out, p.r)
	}
	return out
}
