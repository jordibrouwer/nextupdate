// Package semver parses and compares the version labels of container images.
package semver

import (
	"fmt"
	"strconv"
	"strings"
)

type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

func (v Version) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Parse reads "1", "1.2", "1.2.3", "v1.2.3", "1.2.3-rc.1" and "1.2.3+build".
func Parse(s string) (Version, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if core == "" || len(parts) > 3 {
		return Version{}, false
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return Version{}, false
		}
		n[i] = v
	}
	return Version{n[0], n[1], n[2], pre}, true
}

// Compare returns -1, 0 or 1. A pre-release sorts below its release.
func Compare(a, b Version) int {
	for _, d := range []int{a.Major - b.Major, a.Minor - b.Minor, a.Patch - b.Patch} {
		if d < 0 {
			return -1
		}
		if d > 0 {
			return 1
		}
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "":
		return 1
	case b.Pre == "":
		return -1
	case a.Pre < b.Pre:
		return -1
	}
	return 1
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
