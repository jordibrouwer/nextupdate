package updater

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/docker"
)

// ExecRunner runs the docker CLI.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func ComposeArgs(project, workdir string, files []string, extra ...string) []string {
	args := []string{"compose", "-p", project, "--project-directory", workdir}
	for _, f := range files {
		args = append(args, "-f", f)
	}
	return append(args, extra...)
}

func FindService(ctx context.Context, api docker.API, project, service string) (string, error) {
	list, err := api.ListContainers(ctx)
	if err != nil {
		return "", err
	}
	for _, s := range list {
		if s.Labels[discovery.LabelProject] != project || s.Labels[discovery.LabelService] != service {
			continue
		}
		if len(s.Names) > 0 && strings.HasSuffix(s.Names[0], discovery.OldSuffix) {
			continue
		}
		return s.ID, nil
	}
	return "", fmt.Errorf("no container for service %s/%s", project, service)
}

// Compose updates a compose service through the compose CLI, so the compose
// file stays the source of truth. Rollback re-tags the old image and brings
// the service up without pulling.
type Compose struct {
	API     docker.API
	Runner  Runner
	Journal Journal
	Verify  Verifier
}

func (cp *Compose) Update(ctx context.Context, c discovery.Container) Result {
	var res Result
	logf := func(format string, a ...any) { res.Log = append(res.Log, fmt.Sprintf(format, a...)) }
	fail := func(reason string) Result {
		res.Outcome, res.Reason = OutcomeFailed, reason
		logf("failed: %s", reason)
		return res
	}
	args := func(extra ...string) []string {
		return ComposeArgs(c.ComposeProject, c.ComposeWorkdir, c.ComposeFiles, extra...)
	}

	old, err := cp.API.InspectContainer(ctx, c.ID)
	if err != nil {
		return fail("inspect container: " + err.Error())
	}
	res.FromImage = old.Image
	jid, err := cp.Journal.Begin(c.Name, "compose", map[string]string{
		"old_image": old.Image, "ref": c.Image, "project": c.ComposeProject, "service": c.ComposeService,
		"workdir": c.ComposeWorkdir, "files": strings.Join(c.ComposeFiles, ","),
	})
	if err != nil {
		return fail("journal: " + err.Error())
	}

	logf("compose pull %s", c.ComposeService)
	if out, err := cp.Runner.Run(ctx, c.ComposeWorkdir, args("pull", c.ComposeService)...); err != nil {
		cp.Journal.Close(jid)
		return fail(fmt.Sprintf("compose pull: %v: %s", err, out))
	}
	newImg, err := cp.API.InspectImage(ctx, c.Image)
	if err != nil {
		cp.Journal.Close(jid)
		return fail("inspect new image: " + err.Error())
	}
	res.ToImage = newImg.ID
	if newImg.ID == old.Image {
		cp.Journal.Close(jid)
		logf("image unchanged")
		res.Outcome, res.Reason = OutcomeOK, "image unchanged"
		return res
	}

	rctx := context.WithoutCancel(ctx)
	rollback := func(reason string) Result {
		logf("rollback: %s", reason)
		repo, tag := docker.SplitRef(c.Image)
		if err := cp.API.TagImage(rctx, old.Image, repo, tag); err != nil {
			return fail(reason + "; rollback tag: " + err.Error())
		}
		if out, err := cp.Runner.Run(rctx, c.ComposeWorkdir, args("up", "-d", "--no-deps", "--pull", "never", c.ComposeService)...); err != nil {
			return fail(fmt.Sprintf("%s; rollback up: %v: %s", reason, err, out))
		}
		cp.Journal.Close(jid)
		res.Outcome, res.Reason = OutcomeRolledBack, reason
		return res
	}

	logf("compose up %s", c.ComposeService)
	if out, err := cp.Runner.Run(rctx, c.ComposeWorkdir, args("up", "-d", "--no-deps", c.ComposeService)...); err != nil {
		return rollback(fmt.Sprintf("compose up: %v: %s", err, out))
	}
	if !old.State.Running {
		_ = cp.Journal.Step(jid, "verified", nil)
		cp.Journal.Close(jid)
		res.Outcome = OutcomeOK
		return res
	}
	newID, err := FindService(ctx, cp.API, c.ComposeProject, c.ComposeService)
	if err != nil {
		return rollback(err.Error())
	}
	logf("verify %s", c.Name)
	if v := cp.Verify(ctx, c.Name, newID); !v.OK {
		return rollback("verify: " + v.Reason)
	}
	_ = cp.Journal.Step(jid, "verified", nil)
	cp.Journal.Close(jid)
	res.Outcome = OutcomeOK
	return res
}
