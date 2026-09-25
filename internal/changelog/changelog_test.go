package changelog

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRepoFromURL(t *testing.T) {
	cases := map[string]string{
		"https://github.com/owner/name":          "owner/name",
		"https://github.com/owner/name.git":      "owner/name",
		"https://www.github.com/owner/name/":     "owner/name",
		"github.com/owner/name":                  "owner/name",
		"https://github.com/owner/name/tree/dev": "owner/name",
	}
	for in, want := range cases {
		if got, ok := RepoFromURL(in); !ok || got != want {
			t.Errorf("RepoFromURL(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "https://gitlab.com/o/n", "https://github.com/onlyowner", "not a url"} {
		if got, ok := RepoFromURL(in); ok {
			t.Errorf("RepoFromURL(%q) = %q, want no match", in, got)
		}
	}
}

func TestMappingsLookupNormalisesImage(t *testing.T) {
	m, err := LoadMappings([]byte(`{"entries":[{"image":"docker.io/vaultwarden/server","repo":"dani-garcia/vaultwarden","breaking":["1.99.0"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := m.Lookup("vaultwarden/server:1.30.0")
	if !ok || got.Repo != "dani-garcia/vaultwarden" || len(got.Breaking) != 1 {
		t.Fatalf("got %+v %v", got, ok)
	}
	if _, ok := m.Lookup("ghcr.io/other/app"); ok {
		t.Fatal("unrelated image matched")
	}
	var nilMappings *Mappings
	if _, ok := nilMappings.Lookup("x"); ok {
		t.Fatal("nil mappings must not match")
	}
}

func TestDefaultMappingsLoad(t *testing.T) {
	if _, ok := DefaultMappings().Lookup("docker.io/vaultwarden/server"); !ok {
		t.Fatal("bundled mapping missing vaultwarden")
	}
}

func TestResolvePriority(t *testing.T) {
	m, _ := LoadMappings([]byte(`{"entries":[{"image":"ghcr.io/team/app","repo":"mapped/app"}]}`))
	labels := map[string]string{"org.opencontainers.image.source": "https://github.com/labelled/app"}

	if r, ok := Resolve("ghcr.io/team/app:1", labels, m, "manual/app"); !ok || r.Name != "manual/app" || r.Source != "manual" {
		t.Errorf("manual: %+v", r)
	}
	if r, ok := Resolve("ghcr.io/team/app:1", labels, m, ""); !ok || r.Name != "labelled/app" || r.Source != "label" {
		t.Errorf("label: %+v", r)
	}
	if r, ok := Resolve("ghcr.io/team/app:1", nil, m, ""); !ok || r.Name != "mapped/app" || r.Source != "mapping" {
		t.Errorf("mapping: %+v", r)
	}
	if _, ok := Resolve("ghcr.io/team/none:1", nil, m, ""); ok {
		t.Error("nothing known, want no repo")
	}
}

func rel(tag string, pre bool) Release { return Release{Tag: tag, Prerelease: pre} }

func TestBetween(t *testing.T) {
	all := []Release{rel("v2.1.0", false), rel("v2.0.0", false), rel("v2.0.0-rc.1", true), rel("v1.5.0", false), rel("v1.4.0", false), rel("nightly", false)}

	tags := func(rs []Release) []string {
		var out []string
		for _, r := range rs {
			out = append(out, r.Tag)
		}
		return out
	}
	eq := func(got, want []string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	if got := tags(Between(all, "1.4.0", "2.0.0")); !eq(got, []string{"v2.0.0", "v1.5.0"}) {
		t.Errorf("range: %v", got)
	}
	if got := tags(Between(all, "", "2.0.0")); !eq(got, []string{"v2.0.0"}) {
		t.Errorf("new only: %v", got)
	}
	if got := Between(all, "1.4.0", ""); got != nil {
		t.Errorf("no new version: %v", got)
	}
}

type memCache map[string]struct {
	etag string
	body []byte
}

func (m memCache) Get(repo string) (string, []byte, bool) {
	e, ok := m[repo]
	return e.etag, e.body, ok
}
func (m memCache) Put(repo, etag string, body []byte) error {
	m[repo] = struct {
		etag string
		body []byte
	}{etag, body}
	return nil
}

func TestGitHubReleasesUsesETag(t *testing.T) {
	var gotAuth, gotINM string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotAuth, gotINM = r.Header.Get("Authorization"), r.Header.Get("If-None-Match")
		if r.URL.Path != "/repos/o/n/releases" {
			t.Errorf("path %s", r.URL.Path)
		}
		if gotINM == `"abc"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"abc"`)
		io.WriteString(w, `[{"tag_name":"v1.1.0","name":"One one","body":"notes","html_url":"https://x/1.1.0","published_at":"2026-01-02T03:04:05Z","prerelease":false,"draft":false},
			{"tag_name":"v1.2.0","draft":true}]`)
	}))
	defer srv.Close()
	g := &GitHub{BaseURL: srv.URL, Token: "tok", Cache: memCache{}}

	got, err := g.Releases(context.Background(), "o/n")
	if err != nil || len(got) != 1 || got[0].Tag != "v1.1.0" || got[0].Body != "notes" || got[0].URL != "https://x/1.1.0" {
		t.Fatalf("first call: %+v %v", got, err)
	}
	if gotAuth != "Bearer tok" || gotINM != "" {
		t.Fatalf("first call headers: auth %q inm %q", gotAuth, gotINM)
	}
	got, err = g.Releases(context.Background(), "o/n")
	if err != nil || len(got) != 1 || got[0].Tag != "v1.1.0" {
		t.Fatalf("second call (304): %+v %v", got, err)
	}
	if gotINM != `"abc"` || calls != 2 {
		t.Fatalf("second call: inm %q calls %d", gotINM, calls)
	}
}

func TestGitHubStatuses(t *testing.T) {
	for status, check := range map[int]func(rs []Release, err error) bool{
		http.StatusNotFound:            func(rs []Release, err error) bool { return err == nil && rs == nil },
		http.StatusForbidden:           func(rs []Release, err error) bool { return errors.Is(err, ErrRateLimited) },
		http.StatusTooManyRequests:     func(rs []Release, err error) bool { return errors.Is(err, ErrRateLimited) },
		http.StatusInternalServerError: func(rs []Release, err error) bool { return err != nil && !errors.Is(err, ErrRateLimited) },
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
		rs, err := (&GitHub{BaseURL: srv.URL}).Releases(context.Background(), "o/n")
		srv.Close()
		if !check(rs, err) {
			t.Errorf("status %d: %v %v", status, rs, err)
		}
	}
}
