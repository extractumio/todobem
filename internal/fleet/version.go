package fleet

import "github.com/extractumio/todobem/internal/buildinfo"

// BuildVersion names the build in a hello and on the Servers section: the release version, or
// `dev-<rev>` for a build from a git clone (internal/buildinfo).
func BuildVersion() string { return buildinfo.Current() }
