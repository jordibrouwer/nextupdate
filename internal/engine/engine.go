// Package engine finds available updates and applies them.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
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
		if err != nil {
			e.logf("check %s: inspect image: %v", c.Name, err)
			continue
		}
		local, ok := registry.LocalDigest(img, c.Image)
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
	info := store.Info{Container: c.Name, OldVersion: registry.LocalLabels(local)[registry.LabelVersion]}
	remoteLabels, err := e.Registry.RemoteLabels(ctx, c.Image)
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
	}
	return h, nil
}
