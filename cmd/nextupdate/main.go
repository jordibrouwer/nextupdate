package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/changelog"
	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/engine"
	"github.com/jordibrouwer/nextupdate/internal/registry"
	"github.com/jordibrouwer/nextupdate/internal/scheduler"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
	"github.com/jordibrouwer/nextupdate/internal/verify"
)

var _ changelog.Cache = (*store.ChangelogCache)(nil)

const usage = `usage: nextupdate <command>

commands:
  serve                       check on a schedule and apply the update policies
  check                       list containers with a newer image
  update <name>               update one container, roll back if it fails
  policy <name> <policy>      set notify, auto or never for a container
  reconcile                   repair updates interrupted by a crash

environment:
  DOCKER_HOST                 Docker endpoint (default unix:///var/run/docker.sock)
  NEXTUPDATE_DATA             data directory (default /data)
  NEXTUPDATE_INTERVAL         time between checks in serve mode (default 6h)
  NEXTUPDATE_VERIFY_WINDOW    how long a new container gets to become healthy (default 60s)
  NEXTUPDATE_KEEP_OLD_IMAGES  how long to keep the previous image (default 168h)
  NEXTUPDATE_GITHUB_TOKEN     optional token for a higher GitHub rate limit`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "nextupdate:", err)
		os.Exit(1)
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envDuration(key, def string) (time.Duration, error) {
	d, err := time.ParseDuration(envOr(key, def))
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}

func run(ctx context.Context, cmd string, args []string) error {
	api, err := docker.New(os.Getenv("DOCKER_HOST"))
	if err != nil {
		return err
	}
	dataDir := envOr("NEXTUPDATE_DATA", "/data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(dataDir, "nextupdate.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	window, err := envDuration("NEXTUPDATE_VERIFY_WINDOW", "60s")
	if err != nil {
		return err
	}
	keep, err := envDuration("NEXTUPDATE_KEEP_OLD_IMAGES", "168h")
	if err != nil {
		return err
	}
	interval, err := envDuration("NEXTUPDATE_INTERVAL", "6h")
	if err != nil {
		return err
	}
	verifier := engine.NewVerifier(api, st, verify.Check{Window: window, Interval: time.Second, MaxRestarts: 3})
	journal := st.Journal()
	runner := updater.ExecRunner{}
	self, _ := os.Hostname()
	eng := &engine.Engine{
		API: api, Registry: registry.NewRemote(), Store: st, Self: self, Retention: keep,
		Run:             &updater.Run{API: api, Journal: journal, Verify: verifier},
		Compose:         &updater.Compose{API: api, Runner: runner, Journal: journal, Verify: verifier},
		RollbackRun:     &updater.Run{API: api, Journal: journal, Verify: verifier, SkipPull: true},
		RollbackCompose: &updater.Compose{API: api, Runner: runner, Journal: journal, Verify: verifier, SkipPull: true},
		Changelog:       &changelog.GitHub{Token: os.Getenv("NEXTUPDATE_GITHUB_TOKEN"), Cache: st.ChangelogCache()},
		Mappings:        changelog.DefaultMappings(),
	}

	switch cmd {
	case "serve":
		if err := reconcile(ctx, api, journal, runner); err != nil {
			return err
		}
		fmt.Printf("serving: checking every %s\n", interval)
		s := &scheduler.Scheduler{Engine: eng, Store: st, Notifier: scheduler.LogNotifier{}, Interval: interval}
		return s.Run(ctx)
	case "check":
		list, err := eng.Check(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("everything is up to date")
			return nil
		}
		infos, err := st.ListInfo()
		if err != nil {
			return err
		}
		byName := map[string]store.Info{}
		for _, i := range infos {
			byName[i.Container] = i
		}
		for _, a := range list {
			i := byName[a.Container]
			line := fmt.Sprintf("%-30s %s", a.Container, a.Image)
			if i.OldVersion != "" || i.NewVersion != "" {
				line += fmt.Sprintf("  %s to %s", orUnknown(i.OldVersion), orUnknown(i.NewVersion))
			}
			if i.Breaking {
				line += "  BREAKING"
			}
			fmt.Println(line)
			for _, r := range i.Reasons {
				fmt.Println("    " + r)
			}
		}
		return nil
	case "update":
		if len(args) != 1 {
			return errors.New("update needs exactly one container name")
		}
		if err := reconcile(ctx, api, journal, runner); err != nil {
			return err
		}
		h, err := eng.Update(ctx, args[0])
		if err != nil {
			return err
		}
		for _, l := range h.Log {
			fmt.Println("  " + l)
		}
		fmt.Printf("%s: %s %s\n", h.Container, h.Outcome, h.Reason)
		if h.Outcome != updater.OutcomeOK {
			return fmt.Errorf("update of %s ended as %s", h.Container, h.Outcome)
		}
		return nil
	case "policy":
		if len(args) != 2 {
			return errors.New("policy needs a container name and one of notify, auto, never")
		}
		s, err := st.GetSettings(args[0])
		if err != nil {
			return err
		}
		s.Policy = strings.ToLower(args[1])
		if err := st.SetSettings(s); err != nil {
			return err
		}
		fmt.Printf("%s: policy is now %s\n", args[0], s.Policy)
		return nil
	case "reconcile":
		return reconcile(ctx, api, journal, runner)
	default:
		return fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
}

func orUnknown(v string) string {
	if v == "" {
		return "unknown"
	}
	return v
}

func reconcile(ctx context.Context, api docker.API, j *store.Journal, runner updater.Runner) error {
	actions, err := updater.Reconcile(ctx, api, j, runner)
	for _, a := range actions {
		fmt.Println("reconcile:", a)
	}
	return err
}
