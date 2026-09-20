package fleet

import (
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
)

// BuildVersion names the build from the information `go build` embeds when the main package
// sits in a git clone: the revision's first seven characters, "+dirty" when the tree had
// uncommitted changes; "dev" when nothing is embedded (a build outside the clone). No -ldflags
// needed.
func BuildVersion() string {
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
	return rev
}

// VersionLine is what `todobem -version` prints: `todobem <version> <goos>/<goarch> protocol <n>`.
func VersionLine() string {
	return strings.Join([]string{"todobem", BuildVersion(), runtime.GOOS + "/" + runtime.GOARCH, "protocol", strconv.Itoa(Protocol)}, " ")
}
