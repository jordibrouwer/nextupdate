package updater

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/docker"
)

// Run updates a container created with plain `docker run`: keep the old
// container under a new name until the new one passes verification.
type Run struct {
	API      docker.API
	Journal  Journal
	Verify   Verifier
	SkipPull bool // the image is already local (manual rollback)
}

func (r *Run) Update(ctx context.Context, c discovery.Container) Result {
	var res Result
	logf := func(format string, a ...any) { res.Log = append(res.Log, fmt.Sprintf(format, a...)) }
	fail := func(reason string) Result {
		res.Outcome, res.Reason = OutcomeFailed, reason
		logf("failed: %s", reason)
		return res
	}

	old, err := r.API.InspectContainer(ctx, c.ID)
	if err != nil {
		return fail("inspect container: " + err.Error())
	}
	res.FromImage = old.Image
	oldImg, err := r.API.InspectImage(ctx, old.Image)
	if errors.Is(err, docker.ErrNotFound) {
		// The tag moved and the image record went with it. Carry on with the
		// container's own settings; the image defaults cannot be subtracted.
		logf("previous image record is not available; keeping the container's own settings")
	} else if err != nil {
		return fail("inspect current image: " + err.Error())
	}
	wasRunning := old.State.Running
	jid, err := r.Journal.Begin(c.Name, "run", map[string]string{"old_id": old.ID, "was_running": strconv.FormatBool(wasRunning)})
	if err != nil {
		return fail("journal: " + err.Error())
	}

	if !r.SkipPull {
		logf("pull %s", c.Image)
		if err := r.API.PullImage(ctx, c.Image); err != nil {
			r.Journal.Close(jid)
			return fail("pull: " + err.Error())
		}
	}
	newImg, err := r.API.InspectImage(ctx, c.Image)
	if err != nil {
		r.Journal.Close(jid)
		return fail("inspect new image: " + err.Error())
	}
	res.ToImage = newImg.ID
	if newImg.ID == old.Image {
		r.Journal.Close(jid)
		logf("image unchanged")
		res.Outcome, res.Reason = OutcomeOK, "image unchanged"
		return res
	}

	rctx := context.WithoutCancel(ctx)
	rollback := func(newID, reason string) Result {
		logf("rollback: %s", reason)
		if newID != "" {
			if err := r.API.RemoveContainer(rctx, newID); err != nil && !errors.Is(err, docker.ErrNotFound) {
				return fail(reason + "; rollback remove new: " + err.Error())
			}
		}
		if err := r.API.RenameContainer(rctx, old.ID, c.Name); err != nil {
			return fail(reason + "; rollback rename: " + err.Error())
		}
		if wasRunning {
			if err := r.API.StartContainer(rctx, old.ID); err != nil {
				return fail(reason + "; rollback start: " + err.Error())
			}
		}
		r.Journal.Close(jid)
		res.Outcome, res.Reason = OutcomeRolledBack, reason
		return res
	}

	if wasRunning {
		logf("stop %s", c.Name)
		if err := r.API.StopContainer(ctx, old.ID); err != nil {
			r.Journal.Close(jid)
			return fail("stop: " + err.Error())
		}
	}
	if err := r.API.RenameContainer(rctx, old.ID, c.Name+discovery.OldSuffix); err != nil {
		if wasRunning {
			_ = r.API.StartContainer(rctx, old.ID)
		}
		r.Journal.Close(jid)
		return fail("rename old: " + err.Error())
	}
	_ = r.Journal.Step(jid, "renamed_old", nil)

	logf("create %s from %s", c.Name, c.Image)
	newID, err := r.API.CreateContainer(rctx, c.Name, BuildSpec(old, oldImg, c.Image))
	if err != nil {
		return rollback("", "create: "+err.Error())
	}
	_ = r.Journal.Step(jid, "created_new", map[string]string{"new_id": newID})

	if wasRunning {
		if err := r.API.StartContainer(rctx, newID); err != nil {
			return rollback(newID, "start: "+err.Error())
		}
		logf("verify %s", c.Name)
		if v := r.Verify(ctx, c.Name, newID); !v.OK {
			return rollback(newID, "verify: "+v.Reason)
		}
	}
	_ = r.Journal.Step(jid, "verified", nil)

	if err := r.API.RemoveContainer(rctx, old.ID); err != nil {
		logf("remove old container: %v", err)
	}
	r.Journal.Close(jid)
	res.Outcome = OutcomeOK
	return res
}
