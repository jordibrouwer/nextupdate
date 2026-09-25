// Package semver parses and compares the version labels of container images.
package semver

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is a parsed image version. Besides plain semver it reads the forms
// container images use: a fourth number (4.0.20.3014, as Sonarr does) and the
// image build suffix of linuxserver.io images (-ls325, or -r0-ls345 on Alpine).
type Version struct {
	Major, Minor, Patch int
	Rev                 int    // the fourth number, if there is one
	Build               int    // the 325 of -ls325: a rebuild of the same application version
	Pre                 string // any other suffix: a pre-release such as rc.1
	hasRev              bool
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.hasRev {
		s += fmt.Sprintf(".%d", v.Rev)
	}
	if v.Build > 0 {
		s += fmt.Sprintf("-ls%d", v.Build)
	}
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

var buildSuffix = regexp.MustCompile(`^(?:r\d+-)?ls(\d+)$`)

// Parse reads "1", "1.2", "1.2.3", "1.2.3.4", "v1.2.3", "1.2.3-rc.1",
// "1.2.3+build", "4.0.20.3014-ls325" and "1.26.3-r0-ls345".
func Parse(s string) (Version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "+")
	core, suffix, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if core == "" || len(parts) > 4 {
		return Version{}, false
	}
	var n [4]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return Version{}, false
		}
		n[i] = v
	}
	v := Version{Major: n[0], Minor: n[1], Patch: n[2], Rev: n[3], hasRev: len(parts) == 4}
	if m := buildSuffix.FindStringSubmatch(suffix); m != nil {
		v.Build, _ = strconv.Atoi(m[1])
	} else {
		v.Pre = suffix
	}
	return v, true
}

// Compare returns -1, 0 or 1. A pre-release sorts below its release, and an
// image build number only breaks a tie between equal versions.
func Compare(a, b Version) int {
	for _, d := range []int{a.Major - b.Major, a.Minor - b.Minor, a.Patch - b.Patch, a.Rev - b.Rev} {
		if d < 0 {
			return -1
		}
		if d > 0 {
			return 1
		}
	}
	switch {
	case a.Pre == b.Pre:
	case a.Pre == "":
		return 1
	case b.Pre == "":
		return -1
	case a.Pre < b.Pre:
		return -1
	default:
		return 1
	}
	switch {
	case a.Build < b.Build:
		return -1
	case a.Build > b.Build:
		return 1
	}
	return 0
}

type Change int

const (
	None Change = iota
	Patch
	Minor
	Major
	Downgrade
)

func (c Change) String() string {
	return [...]string{"none", "patch", "minor", "major", "downgrade"}[c]
}

// Diff classifies the step from one version to another. While both are 0.x
// a minor step counts as major, because 0.x has no stability promise.
func Diff(from, to Version) Change {
	switch c := Compare(from, to); {
	case c > 0:
		return Downgrade
	case c == 0:
		return None
	}
	switch {
	case to.Major != from.Major:
		return Major
	case to.Minor != from.Minor:
		if from.Major == 0 {
			return Major
		}
		return Minor
	}
	return Patch
}
