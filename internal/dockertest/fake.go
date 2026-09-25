// Package dockertest provides an in-memory docker.API for tests.
package dockertest

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jordibrouwer/nextupdate/internal/docker"
)

type Fake struct {
	Containers map[string]*docker.ContainerJSON
	Images     map[string]docker.ImageJSON
	Calls      []string
	PullErr    error
	CreateErr  error
	StartErr   error
	OnPull     func(ref string)
	next       int
}

var _ docker.API = (*Fake)(nil)

func New() *Fake {
	return &Fake{Containers: map[string]*docker.ContainerJSON{}, Images: map[string]docker.ImageJSON{}}
}

func (f *Fake) AddImage(ref string, img docker.ImageJSON) {
	f.Images[ref] = img
	f.Images[img.ID] = img
}

func (f *Fake) AddContainer(id, name, ref string, labels map[string]string, running bool) *docker.ContainerJSON {
	lbl := map[string]any{}
	for k, v := range labels {
		lbl[k] = v
	}
	c := &docker.ContainerJSON{ID: id, Name: "/" + name, Image: f.Images[ref].ID,
		Config: map[string]any{"Image": ref, "Labels": lbl}}
	c.State.Running = running
	c.State.Status = "exited"
	if running {
		c.State.Status = "running"
	}
	f.Containers[id] = c
	return c
}

func (f *Fake) record(format string, a ...any) { f.Calls = append(f.Calls, fmt.Sprintf(format, a...)) }

func (f *Fake) find(idOrName string) (*docker.ContainerJSON, error) {
	if c, ok := f.Containers[idOrName]; ok {
		return c, nil
	}
	for _, c := range f.Containers {
		if c.Name == "/"+idOrName {
			return c, nil
		}
	}
	return nil, fmt.Errorf("container %s: %w", idOrName, docker.ErrNotFound)
}

func (f *Fake) ListContainers(ctx context.Context) ([]docker.ContainerSummary, error) {
	ids := make([]string, 0, len(f.Containers))
	for id := range f.Containers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var out []docker.ContainerSummary
	for _, id := range ids {
		c := f.Containers[id]
		labels := map[string]string{}
		if m, ok := c.Config["Labels"].(map[string]any); ok {
			for k, v := range m {
				labels[k], _ = v.(string)
			}
		}
		ref, _ := c.Config["Image"].(string)
		out = append(out, docker.ContainerSummary{ID: c.ID, Names: []string{c.Name}, Image: ref,
			ImageID: c.Image, Labels: labels, State: c.State.Status})
	}
	return out, nil
}

func (f *Fake) InspectContainer(ctx context.Context, id string) (docker.ContainerJSON, error) {
	c, err := f.find(id)
	if err != nil {
		return docker.ContainerJSON{}, err
	}
	return *c, nil
}

func (f *Fake) InspectImage(ctx context.Context, ref string) (docker.ImageJSON, error) {
	img, ok := f.Images[ref]
	if !ok {
		return docker.ImageJSON{}, fmt.Errorf("image %s: %w", ref, docker.ErrNotFound)
	}
	return img, nil
}

func (f *Fake) PullImage(ctx context.Context, ref string) error {
	f.record("pull %s", ref)
	if f.PullErr != nil {
		return f.PullErr
	}
	if f.OnPull != nil {
		f.OnPull(ref)
	}
	return nil
}

func (f *Fake) TagImage(ctx context.Context, id, repo, tag string) error {
	f.record("tag %s %s:%s", id, repo, tag)
	img, ok := f.Images[id]
	if !ok {
		return fmt.Errorf("image %s: %w", id, docker.ErrNotFound)
	}
	f.Images[repo+":"+tag] = img
	return nil
}

func (f *Fake) CreateContainer(ctx context.Context, name string, spec docker.CreateSpec) (string, error) {
	f.record("create %s", name)
	if f.CreateErr != nil {
		return "", f.CreateErr
	}
	if _, err := f.find(name); err == nil {
		return "", fmt.Errorf("name %s already in use", name)
	}
	ref, _ := spec.Config["Image"].(string)
	img, ok := f.Images[ref]
	if !ok {
		return "", fmt.Errorf("image %s: %w", ref, docker.ErrNotFound)
	}
	f.next++
	id := fmt.Sprintf("new%d", f.next)
	c := &docker.ContainerJSON{ID: id, Name: "/" + name, Image: img.ID, Config: spec.Config, HostConfig: spec.HostConfig}
	c.State.Status = "created"
	f.Containers[id] = c
	return id, nil
}

func (f *Fake) StartContainer(ctx context.Context, id string) error {
	f.record("start %s", id)
	if f.StartErr != nil {
		return f.StartErr
	}
	c, err := f.find(id)
	if err != nil {
		return err
	}
	c.State.Running, c.State.Status = true, "running"
	return nil
}

func (f *Fake) StopContainer(ctx context.Context, id string) error {
	f.record("stop %s", id)
	c, err := f.find(id)
	if err != nil {
		return err
	}
	c.State.Running, c.State.Status = false, "exited"
	return nil
}

func (f *Fake) RenameContainer(ctx context.Context, id, newName string) error {
	f.record("rename %s %s", id, newName)
	c, err := f.find(id)
	if err != nil {
		return err
	}
	if other, err := f.find(newName); err == nil && other.ID != c.ID {
		return fmt.Errorf("name %s already in use", newName)
	}
	c.Name = "/" + strings.TrimPrefix(newName, "/")
	return nil
}

func (f *Fake) RemoveContainer(ctx context.Context, id string) error {
	f.record("remove %s", id)
	c, err := f.find(id)
	if err != nil {
		return err
	}
	delete(f.Containers, c.ID)
	return nil
}
