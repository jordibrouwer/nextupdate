// Package engine finds available updates and applies them.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/changelog"
	"github.com/jordibrouwer/nextupdate/internal/classify"
	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/registry"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
)

var ErrSelfUpdate = errors.New("nextupdate does not update its own container yet")
var ErrNoRollback = errors.New("there is nothing to roll back to")

type Engine struct {
	API      docker.API
	Registry registry.Checker
	Store    *store.Store
	Run      updater.Adapter
	Compose  updater.Adapter
	Self     string // own hostname = own short container ID
	Log      *log.Logger
	Now      func() time.Time

	Changelog changelog.Source
	Mappings  *changelog.Mappings
	Retention time.Duration // how long an update keeps the previous image; 0 = do not track

	RollbackRun     updater.Adapter // Run adapter with SkipPull
	RollbackCompose updater.Adapter // Compose adapter with SkipPull

	mu sync.Mutex // one update at a time
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e *Engine) logf(format string, a ...any) {
	if e.Log != nil {
		e.Log.Printf(format, a...)
	} else {
		log.Printf(format, a...)
	}
}

func (e *Engine) Check(ctx context.Context) ([]store.Available, error) {
	containers, err := discovery.Discover(ctx, e.API)
	if err != nil {
		return nil, err
	}
	var out []store.Available
	var infos []store.Info
	for _, c := range containers {
		img, err := e.API.InspectImage(ctx, c.ImageID)
		gone := errors.Is(err, docker.ErrNotFound)
		if err != nil && !gone {
			e.logf("check %s: inspect image: %v", c.Name, err)
			continue
		}
		local, ok := registry.LocalDigest(img, c.Image)
		if !ok && (gone || nameless(img)) {
			// The tag moved to a newer image (a rollback, or a pull without a
			// restart) and this container's image lost its name. It still came from
			// a registry: on the containerd image store its ID is the manifest digest.
			local, ok = digestOfImageID(c.ImageID)
		}
		if !ok {
			continue // built locally, no registry to compare with
		}
		remote, err := e.Registry.RemoteDigest(ctx, c.Image)
		if errors.Is(err, registry.ErrNotPublished) {
			continue // local build or private repo: nothing to compare with
		}
		if err != nil {
			e.logf("check %s: %v", c.Name, err)
			continue
		}
		if remote == local {
			continue
		}
		out = append(out, store.Available{Container: c.Name, Image: c.Image, LocalDigest: local, RemoteDigest: remote, DetectedAt: e.now()})
		infos = append(infos, e.describe(ctx, c, img))
	}
	if err := e.Store.ReplaceAvailable(out); err != nil {
		return nil, err
	}
	if err := e.Store.ReplaceInfo(infos); err != nil {
		return nil, err
	}
	return e.Store.ListAvailable()
}

// describe collects versions, release notes and the breaking verdict of an
// available update. Missing pieces are logged and left empty: an update
// without notes is still an update.
func (e *Engine) describe(ctx context.Context, c discovery.Container, local docker.ImageJSON) store.Info {
	info := store.Info{Container: c.Name, OldVersion: e.localVersion(ctx, c, local)}
	remoteLabels, err := e.Registry.RemoteLabels(ctx, c.Image, registry.PlatformOf(local))
	if err != nil {
		e.logf("check %s: read remote labels: %v", c.Name, err)
	}
	info.NewVersion = remoteLabels[registry.LabelVersion]
	settings, err := e.Store.GetSettings(c.Name)
	if err != nil {
		e.logf("check %s: read settings: %v", c.Name, err)
	}
	mapping, _ := e.Mappings.Lookup(c.Image)
	var releases []changelog.Release
	if repo, ok := changelog.Resolve(c.Image, remoteLabels, e.Mappings, settings.Repo); ok {
		info.Repo = repo.Name
		if e.Changelog != nil {
			all, err := e.Changelog.Releases(ctx, repo.Name)
			if err != nil {
				e.logf("check %s: release notes: %v", c.Name, err)
			}
			releases = changelog.Between(all, info.OldVersion, info.NewVersion)
		}
	}
	verdict := classify.Classify(info.OldVersion, info.NewVersion, releases, mapping.Breaking)
	info.Breaking, info.Reasons = verdict.Breaking, verdict.Reasons
	return info
}

func (e *Engine) Update(ctx context.Context, name string) (store.History, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	containers, err := discovery.Discover(ctx, e.API)
	if err != nil {
		return store.History{}, err
	}
	var target *discovery.Container
	for i := range containers {
		if containers[i].Name == name {
			target = &containers[i]
		}
	}
	if target == nil {
		return store.History{}, fmt.Errorf("no container named %q", name)
	}
	if e.Self != "" && strings.HasPrefix(target.ID, e.Self) {
		return store.History{}, ErrSelfUpdate
	}
	adapter := e.Run
	if target.Source == discovery.SourceCompose {
		adapter = e.Compose
	}
	started := e.now()
	res := adapter.Update(ctx, *target)
	h := store.History{
		Container: name, Image: target.Image, FromImage: res.FromImage, ToImage: res.ToImage,
		StartedAt: started, FinishedAt: e.now(), Outcome: res.Outcome, Reason: res.Reason, Log: res.Log,
	}
	if h.ID, err = e.Store.AddHistory(h); err != nil {
		return h, err
	}
	if res.Outcome == updater.OutcomeOK {
		if err := e.Store.RemoveAvailable(name); err != nil {
			return h, err
		}
		if e.Retention > 0 && res.FromImage != "" && res.FromImage != res.ToImage {
			if err := e.Store.AddOldImage(store.OldImage{ImageID: res.FromImage, Container: name, RemoveAfter: e.now().Add(e.Retention)}); err != nil {
				return h, err
			}
		}
	}
	return h, nil
}

// Cleanup removes old images whose retention period has passed. An image
// that is still in use is kept and tried again on the next cycle.
func (e *Engine) Cleanup(ctx context.Context) error {
	due, err := e.Store.DueOldImages(e.now())
	if err != nil {
		return err
	}
	for _, o := range due {
		err := e.API.RemoveImage(ctx, o.ImageID)
		switch {
		case err == nil, errors.Is(err, docker.ErrNotFound):
			if err := e.Store.DeleteOldImage(o.ImageID); err != nil {
				return err
			}
			e.logf("cleanup: removed old image %s of %s", o.ImageID, o.Container)
		case errors.Is(err, docker.ErrConflict):
			e.logf("cleanup: old image %s of %s is still in use, keeping it", o.ImageID, o.Container)
		default:
			e.logf("cleanup: remove %s: %v", o.ImageID, err)
		}
	}
	return nil
}

func (e *Engine) Containers(ctx context.Context) ([]discovery.Container, error) {
	return discovery.Discover(ctx, e.API)
}

// Rollback puts the previous image back. It keeps the previous image on
// disk for Retention, so this works until Cleanup has removed it.
func (e *Engine) Rollback(ctx context.Context, name string) (store.History, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	containers, err := discovery.Discover(ctx, e.API)
	if err != nil {
		return store.History{}, err
	}
	var target *discovery.Container
	for i := range containers {
		if containers[i].Name == name {
			target = &containers[i]
		}
	}
	if target == nil {
		return store.History{}, fmt.Errorf("no container named %q", name)
	}
	if e.Self != "" && strings.HasPrefix(target.ID, e.Self) {
		return store.History{}, ErrSelfUpdate
	}
	hist, err := e.Store.ListHistory(200)
	if err != nil {
		return store.History{}, err
	}
	var prev *store.History
	for i := range hist {
		h := hist[i]
		if h.Container == name && h.Outcome == updater.OutcomeOK && h.FromImage != "" && h.FromImage != h.ToImage {
			prev = &hist[i]
			break
		}
	}
	if prev == nil {
		return store.History{}, fmt.Errorf("%w: no earlier update of %s is recorded", ErrNoRollback, name)
	}
	if _, err := e.API.InspectImage(ctx, prev.FromImage); errors.Is(err, docker.ErrNotFound) {
		return store.History{}, fmt.Errorf("%w: the previous image was removed", ErrNoRollback)
	} else if err != nil {
		return store.History{}, err
	}
	repo, tag := docker.SplitRef(target.Image)
	if err := e.API.TagImage(ctx, prev.FromImage, repo, tag); err != nil {
		return store.History{}, fmt.Errorf("tag previous image: %w", err)
	}
	adapter := e.RollbackRun
	if target.Source == discovery.SourceCompose {
		adapter = e.RollbackCompose
	}
	started := e.now()
	res := adapter.Update(ctx, *target)
	reason := "Manual rollback."
	if res.Reason != "" {
		reason += " " + res.Reason
	}
	h := store.History{
		Container: name, Image: target.Image, FromImage: res.FromImage, ToImage: res.ToImage,
		StartedAt: started, FinishedAt: e.now(), Outcome: res.Outcome, Reason: reason, Log: res.Log,
	}
	if h.ID, err = e.Store.AddHistory(h); err != nil {
		return h, err
	}
	if res.Outcome == updater.OutcomeOK && e.Retention > 0 && res.FromImage != "" && res.FromImage != res.ToImage {
		if err := e.Store.AddOldImage(store.OldImage{ImageID: res.FromImage, Container: name, RemoveAfter: e.now().Add(e.Retention)}); err != nil {
			return h, err
		}
	}
	return h, nil
}

// nameless reports an image that has neither a tag nor a repo digest.
func nameless(img docker.ImageJSON) bool {
	return len(img.RepoTags) == 0 && len(img.RepoDigests) == 0
}

func digestOfImageID(id string) (string, bool) {
	if strings.HasPrefix(id, "sha256:") && len(id) == len("sha256:")+64 {
		return id, true
	}
	return "", false
}

// localVersion is the version the container runs. The image labels say so;
// when the image is gone, the container's own labels do, because a container
// inherits the labels of its image.
func (e *Engine) localVersion(ctx context.Context, c discovery.Container, img docker.ImageJSON) string {
	if v := registry.LocalLabels(img)[registry.LabelVersion]; v != "" {
		return v
	}
	cont, err := e.API.InspectContainer(ctx, c.ID)
	if err != nil {
		return ""
	}
	labels, _ := cont.Config["Labels"].(map[string]any)
	v, _ := labels[registry.LabelVersion].(string)
	return v
}
