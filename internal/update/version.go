// Package update installs a newer release of todobem over the installed one and rolls back to
// the previous one. The installed binary only fetches, checks and hands over (Fetch); the
// incoming binary switches itself in (Apply) — so what a release does after the hand-off can
// change in any later release. docs/ARCHITECTURE.md §12.
package update

import (
	"fmt"
	"strconv"
	"strings"
)

// Version is a release version: vMAJOR.MINOR.PATCH, optionally with a -suffix (a test or
// candidate build), which orders below the same version without it.
type Version struct {
	Major, Minor, Patch int
	Suffix              string
}

// ParseVersion reads "v1.2.3" or "v1.2.3-e2e".
func ParseVersion(s string) (Version, error) {
	bad := fmt.Errorf("%q is not a release version (vMAJOR.MINOR.PATCH)", s)
	rest, ok := strings.CutPrefix(s, "v")
	if !ok {
		return Version{}, bad
	}
	core, suffix, _ := strings.Cut(rest, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, bad
	}
	var n [3]int
	for i, p := range parts {
		if p == "" || (len(p) > 1 && p[0] == '0') {
			return Version{}, bad
		}
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 {
			return Version{}, bad
		}
		n[i] = v
	}
	if strings.Contains(s, "-") && suffix == "" {
		return Version{}, bad
	}
	for _, r := range suffix {
		if !(r == '.' || r == '-' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return Version{}, bad
		}
	}
	return Version{n[0], n[1], n[2], suffix}, nil
}

func (v Version) String() string {
	s := fmt.Sprintf("v%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Suffix != "" {
		s += "-" + v.Suffix
	}
	return s
}

// Compare is -1, 0 or 1. Suffixes of the same core compare as strings.
func (v Version) Compare(o Version) int {
	for _, d := range [3]int{v.Major - o.Major, v.Minor - o.Minor, v.Patch - o.Patch} {
		if d < 0 {
			return -1
		}
		if d > 0 {
			return 1
		}
	}
	switch {
	case v.Suffix == o.Suffix:
		return 0
	case v.Suffix == "":
		return 1
	case o.Suffix == "":
		return -1
	case v.Suffix < o.Suffix:
		return -1
	}
	return 1
}
