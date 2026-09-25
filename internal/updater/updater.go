// Package updater replaces a container with one running a newer image and
// rolls back when the new one fails verification.
package updater

import (
	"context"

	"github.com/jordibrouwer/nextupdate/internal/discovery"
	"github.com/jordibrouwer/nextupdate/internal/store"
	"github.com/jordibrouwer/nextupdate/internal/verify"
)

const (
	OutcomeOK         = "ok"
	OutcomeRolledBack = "rolled_back"
	OutcomeFailed     = "failed"
)

// Result of one update. FromImage and ToImage are image IDs.
type Result struct {
	Outcome   string
	Reason    string
	FromImage string
	ToImage   string
	Log       []string
}

// Adapter performs plan, apply, verify and rollback for one kind of container.
type Adapter interface {
	Update(ctx context.Context, c discovery.Container) Result
}

// Verifier judges a new container. name is the container name (its settings
// are looked up by name), id is the ID of the new container.
type Verifier func(ctx context.Context, name, id string) verify.Result

type Journal interface {
	Begin(container, adapter string, data map[string]string) (int64, error)
	Step(id int64, step string, data map[string]string) error
	Close(id int64) error
	Open() ([]store.JournalEntry, error)
}

type Runner interface {
	Run(ctx context.Context, dir string, args ...string) (string, error)
}

var _ Journal = (*store.Journal)(nil)
