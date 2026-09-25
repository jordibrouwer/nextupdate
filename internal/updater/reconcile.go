package updater

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

// Reconcile repairs updates that were interrupted, using the journal.
// Anything not verified is rolled back; verified updates are finished.
func Reconcile(ctx context.Context, api docker.API, j Journal, runner Runner) ([]string, error) {
	entries, err := j.Open()
	if err != nil {
		return nil, err
	}
	var actions []string
	for _, e := range entries {
		var err error
		switch e.Adapter {
		case "run":
			err = reconcileRun(ctx, api, e, &actions)
		case "compose":
			err = reconcileCompose(ctx, api, runner, e, &actions)
		default:
			err = fmt.Errorf("unknown adapter %q", e.Adapter)
		}
		if err != nil {
			return actions, fmt.Errorf("reconcile %s: %w", e.Container, err)
		}
		if err := j.Close(e.ID); err != nil {
			return actions, err
		}
	}
	return actions, nil
}

func reconcileRun(ctx context.Context, api docker.API, e store.JournalEntry, actions *[]string) error {
	oldID := e.Data["old_id"]
	if e.Step == "verified" {
		if err := api.RemoveContainer(ctx, oldID); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return err
		}
		*actions = append(*actions, fmt.Sprintf("%s: finished verified update, removed old container", e.Container))
		return nil
	}
	if newID := e.Data["new_id"]; newID != "" {
		if err := api.RemoveContainer(ctx, newID); err != nil && !errors.Is(err, docker.ErrNotFound) {
			return err
		}
	}
	old, err := api.InspectContainer(ctx, oldID)
	if errors.Is(err, docker.ErrNotFound) {
		*actions = append(*actions, fmt.Sprintf("%s: old container gone, nothing to restore", e.Container))
		return nil
	}
	if err != nil {
		return err
	}
	if strings.TrimPrefix(old.Name, "/") != e.Container {
		if err := api.RenameContainer(ctx, oldID, e.Container); err != nil {
			return err
		}
	}
	if e.Data["was_running"] == "true" && !old.State.Running {
		if err := api.StartContainer(ctx, oldID); err != nil {
			return err
		}
	}
	*actions = append(*actions, fmt.Sprintf("%s: rolled back interrupted update", e.Container))
	return nil
}

func reconcileCompose(ctx context.Context, api docker.API, runner Runner, e store.JournalEntry, actions *[]string) error {
	if e.Step == "verified" {
		*actions = append(*actions, fmt.Sprintf("%s: finished verified update", e.Container))
		return nil
	}
	repo, tag := docker.SplitRef(e.Data["ref"])
	if err := api.TagImage(ctx, e.Data["old_image"], repo, tag); err != nil {
		return err
	}
	var files []string
	if f := e.Data["files"]; f != "" {
		files = strings.Split(f, ",")
	}
	args := ComposeArgs(e.Data["project"], e.Data["workdir"], files, "up", "-d", "--no-deps", "--pull", "never", e.Data["service"])
	if out, err := runner.Run(ctx, e.Data["workdir"], args...); err != nil {
		return fmt.Errorf("compose up: %v: %s", err, out)
	}
	*actions = append(*actions, fmt.Sprintf("%s: rolled back interrupted update", e.Container))
	return nil
}
