// Package classify decides whether an update is breaking.
package classify

import (
	"fmt"
	"strings"

	"github.com/jordibrouwer/nextupdate/internal/changelog"
	"github.com/jordibrouwer/nextupdate/internal/semver"
)

// keywords in release notes that suggest manual work.
var keywords = []string{"breaking", "migration", "deprecated", "backwards incompatible", "backward incompatible", "action required", "manual step"}

type Result struct {
	Breaking bool
	Reasons  []string
}

// Classify judges an update from oldV to newV. releases are the notes in
// between; flagged are versions the community mapping marks as breaking.
func Classify(oldV, newV string, releases []changelog.Release, flagged []string) Result {
	var res Result
	add := func(format string, a ...any) {
		res.Breaking = true
		res.Reasons = append(res.Reasons, fmt.Sprintf(format, a...))
	}
	o, okO := semver.Parse(oldV)
	n, okN := semver.Parse(newV)
	if okO && okN {
		switch semver.Diff(o, n) {
		case semver.Major:
			add("Major version change from %s to %s.", o, n)
		case semver.Downgrade:
			add("The new version %s is older than the running version %s.", n, o)
		}
	}
	if okN {
		for _, f := range flagged {
			fv, ok := semver.Parse(f)
			if !ok || semver.Compare(fv, n) > 0 || okO && semver.Compare(fv, o) <= 0 || !okO && semver.Compare(fv, n) != 0 {
				continue
			}
			add("Version %s is marked as breaking in the community mapping.", fv)
		}
	}
	for _, r := range releases {
		text := strings.ToLower(r.Name + "\n" + r.Body)
		for _, k := range keywords {
			if strings.Contains(text, k) {
				add("Release %s mentions %q.", r.Tag, k)
				break
			}
		}
	}
	return res
}
