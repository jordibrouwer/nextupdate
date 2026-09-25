// Package policy decides what to do with an available update.
package policy

import (
	"path"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/jordibrouwer/nextupdate/internal/semver"
	"github.com/jordibrouwer/nextupdate/internal/store"
)

type Action int

const (
	Notify Action = iota // tell the user, do not update
	Update               // update now
	Skip                 // say nothing
)

func (a Action) String() string { return [...]string{"notify", "update", "skip"}[a] }

// protected are images whose update can break other containers or lose
// data; they are never updated automatically.
var protected = map[string]bool{
	"postgres": true, "mysql": true, "mariadb": true, "mongo": true, "redis": true, "valkey": true,
	"couchdb": true, "influxdb": true, "timescaledb": true, "clickhouse": true, "docker-socket-proxy": true,
}

func Protected(image string) bool {
	ref, err := name.ParseReference(image)
	if err != nil {
		return false
	}
	return protected[path.Base(ref.Context().RepositoryStr())]
}

// Decide returns the action for one available update and a plain-English
// reason (empty when the update simply goes ahead).
func Decide(s store.Settings, info store.Info, image string) (Action, string) {
	switch s.Policy {
	case store.PolicyNever:
		return Skip, "The policy for this container is never."
	case store.PolicyAuto:
	default:
		return Notify, "The policy for this container is notify."
	}
	if Protected(image) {
		return Notify, "This is a protected image (database or socket proxy); update it by hand."
	}
	if info.Breaking {
		return Notify, "Breaking update: " + strings.Join(info.Reasons, " ")
	}
	o, okO := semver.Parse(info.OldVersion)
	n, okN := semver.Parse(info.NewVersion)
	if !okO || !okN {
		return Notify, "The version is unknown, so the update is not applied automatically."
	}
	switch semver.Diff(o, n) {
	case semver.None, semver.Patch, semver.Minor:
		return Update, ""
	}
	return Notify, "This is a major version change or a downgrade, so the update is not applied automatically."
}
