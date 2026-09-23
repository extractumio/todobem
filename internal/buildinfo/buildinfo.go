// Package buildinfo names the build: a release build carries its version, commit, build time
// and the repository its releases are published in, set with `go build -ldflags -X`; any other
// build names itself after the git revision `go build` embeds.
package buildinfo

import (
	"runtime/debug"
	"strconv"
)

// Set by the release build (`-ldflags "-X github.com/extractumio/todobem/internal/buildinfo.Version=v0.1.0 …"`).
// Empty in every other build.
var (
	Version = ""
	Commit  = ""
	BuiltAt = ""
)

// ReleasesRepo is the GitHub repository `todobem upgrade` fetches releases from. The release
// build may set it (a dry run publishes to a scratch repository); nothing at run time can.
var ReleasesRepo = "extractumio/todobem"

// IsRelease reports whether this binary came from a release build.
func IsRelease() bool { return Version != "" }

// Current is the version this binary reports: the release version, else `dev-<rev>` from the
// revision `go build` embeds in a git clone ("+dirty" when the tree had uncommitted changes),
// else "dev".
func Current() string {
	if Version != "" {
		return Version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty, _ = strconv.ParseBool(s.Value)
		}
	}
	if rev == "" {
		return "dev"
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if dirty {
		rev += "+dirty"
	}
	return "dev-" + rev
}
