package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jordibrouwer/nextupdate/internal/docker"
	"github.com/jordibrouwer/nextupdate/internal/engine"
	"github.com/jordibrouwer/nextupdate/internal/registry"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/updater"
	"github.com/jordibrouwer/nextupdate/internal/verify"
)

const usage = `usage: nextupdate <command>

commands:
  check            list containers with a newer image
  update <name>    update one container, roll back if it fails
  reconcile        repair updates interrupted by a crash`

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
	window, err := time.ParseDuration(envOr("NEXTUPDATE_VERIFY_WINDOW", "60s"))
	if err != nil {
		return fmt.Errorf("NEXTUPDATE_VERIFY_WINDOW: %w", err)
	}
	chk := verify.Check{Window: window, Interval: time.Second, MaxRestarts: 3}
	verifier := func(ctx context.Context, id string) verify.Result { return verify.Verify(ctx, api, id, chk) }
	journal := st.Journal()
	runner := updater.ExecRunner{}
	self, _ := os.Hostname()
	eng := &engine.Engine{
		API: api, Registry: registry.NewRemote(), Store: st, Self: self,
		Run:     &updater.Run{API: api, Journal: journal, Verify: verifier},
		Compose: &updater.Compose{API: api, Runner: runner, Journal: journal, Verify: verifier},
	}

	switch cmd {
	case "check":
		list, err := eng.Check(ctx)
		if err != nil {
			return err
		}
		if len(list) == 0 {
			fmt.Println("everything is up to date")
		}
		for _, a := range list {
			fmt.Printf("%-30s %s\n", a.Container, a.Image)
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
	case "reconcile":
		return reconcile(ctx, api, journal, runner)
	default:
		return fmt.Errorf("unknown command %q\n%s", cmd, usage)
	}
}

func reconcile(ctx context.Context, api docker.API, j *store.Journal, runner updater.Runner) error {
	actions, err := updater.Reconcile(ctx, api, j, runner)
	for _, a := range actions {
		fmt.Println("reconcile:", a)
	}
	return err
}
