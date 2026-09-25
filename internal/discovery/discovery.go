// Package discovery finds containers and works out how they were created.
package discovery

import (
	"context"
	"fmt"
	"strings"

	"github.com/jordibrouwer/nextupdate/internal/docker"
)

const (
	LabelProject = "com.docker.compose.project"
	LabelService = "com.docker.compose.service"
	LabelWorkdir = "com.docker.compose.project.working_dir"
	LabelFiles   = "com.docker.compose.project.config_files"

	// OldSuffix marks the previous container kept during an update.
	OldSuffix = "-nu-old"
)

type Source string

const (
	SourceCompose Source = "compose"
	SourceRun     Source = "run"
)

type Container struct {
	ID             string
	Name           string
	Image          string // reference the container was created from, e.g. "nginx:latest"
	ImageID        string // image the container runs now
	Source         Source
	ComposeProject string
	ComposeService string
	ComposeWorkdir string
	ComposeFiles   []string
}

func Discover(ctx context.Context, api docker.API) ([]Container, error) {
	list, err := api.ListContainers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	var out []Container
	for _, s := range list {
		if len(s.Names) == 0 {
			continue
		}
		name := strings.TrimPrefix(s.Names[0], "/")
		if strings.HasSuffix(name, OldSuffix) {
			continue
		}
		ref := s.Image
		if isImageID(ref) {
			full, err := api.InspectContainer(ctx, s.ID)
			if err != nil {
				return nil, fmt.Errorf("inspect %s: %w", name, err)
			}
			ref, _ = full.Config["Image"].(string)
			if ref == "" || isImageID(ref) {
				continue // created from a bare image ID; nothing to update from
			}
		}
		c := Container{ID: s.ID, Name: name, Image: ref, ImageID: s.ImageID, Source: SourceRun}
		if p, wd := s.Labels[LabelProject], s.Labels[LabelWorkdir]; p != "" && wd != "" {
			c.Source = SourceCompose
			c.ComposeProject = p
			c.ComposeService = s.Labels[LabelService]
			c.ComposeWorkdir = wd
			if f := s.Labels[LabelFiles]; f != "" {
				c.ComposeFiles = strings.Split(f, ",")
			}
		}
		out = append(out, c)
	}
	return out, nil
}

func isImageID(s string) bool {
	if strings.HasPrefix(s, "sha256:") {
		return true
	}
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdef", r) {
			return false
		}
	}
	return true
}
