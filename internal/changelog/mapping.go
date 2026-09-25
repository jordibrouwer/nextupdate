// Package changelog finds the release notes between two versions of an image.
package changelog

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
)

const labelSource = "org.opencontainers.image.source"

//go:embed mappings.json
var defaultMappings []byte

// Mapping ties an image to its GitHub repo. Breaking lists versions that
// are known to need manual steps.
type Mapping struct {
	Image    string   `json:"image"`
	Repo     string   `json:"repo"`
	Breaking []string `json:"breaking"`
}

type Mappings struct{ byImage map[string]Mapping }

func normalize(image string) (string, bool) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return "", false
	}
	return ref.Context().Name(), true
}

func LoadMappings(data []byte) (*Mappings, error) {
	var f struct {
		Entries []Mapping `json:"entries"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse mappings: %w", err)
	}
	m := &Mappings{byImage: map[string]Mapping{}}
	for _, e := range f.Entries {
		key, ok := normalize(e.Image)
		if !ok {
			return nil, fmt.Errorf("mapping image %q is not a valid reference", e.Image)
		}
		m.byImage[key] = e
	}
	return m, nil
}

// DefaultMappings loads the mappings bundled in the binary.
func DefaultMappings() *Mappings {
	m, err := LoadMappings(defaultMappings)
	if err != nil {
		panic("bundled mappings.json is invalid: " + err.Error())
	}
	return m
}

func (m *Mappings) Lookup(image string) (Mapping, bool) {
	if m == nil {
		return Mapping{}, false
	}
	key, ok := normalize(image)
	if !ok {
		return Mapping{}, false
	}
	e, ok := m.byImage[key]
	return e, ok
}

// RepoFromURL turns a GitHub URL into "owner/name".
func RepoFromURL(u string) (string, bool) {
	if u == "" || strings.ContainsAny(u, " \t") {
		return "", false
	}
	if !strings.Contains(u, "://") {
		u = "https://" + u
	}
	p, err := url.Parse(u)
	if err != nil {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(p.Host), "www.")
	if host != "github.com" {
		return "", false
	}
	parts := strings.Split(strings.Trim(p.Path, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"), true
}

type Repo struct {
	Name   string // "owner/name"
	Source string // "manual", "label" or "mapping"
}

// Resolve finds the GitHub repo of an image: a manual entry wins, then the
// source label of the image, then the bundled mapping.
func Resolve(image string, labels map[string]string, m *Mappings, manual string) (Repo, bool) {
	if manual != "" {
		return Repo{manual, "manual"}, true
	}
	if r, ok := RepoFromURL(labels[labelSource]); ok {
		return Repo{r, "label"}, true
	}
	if e, ok := m.Lookup(image); ok && e.Repo != "" {
		return Repo{e.Repo, "mapping"}, true
	}
	return Repo{}, false
}
