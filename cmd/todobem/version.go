package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"

	"github.com/extractumio/todobem/internal/buildinfo"
	"github.com/extractumio/todobem/internal/fleet"
	"github.com/extractumio/todobem/internal/insights"
	"github.com/extractumio/todobem/internal/state"
	"github.com/extractumio/todobem/internal/store"
	"github.com/extractumio/todobem/internal/update"
)

// versionInfo is what this binary is: `todobem version --json`, which another todobem reads
// before switching to it (internal/update.Info).
func versionInfo() update.Info {
	return update.Info{
		Version:      buildinfo.Current(),
		Commit:       buildinfo.Commit,
		OS:           runtime.GOOS,
		Arch:         runtime.GOARCH,
		Protocol:     fleet.Protocol,
		StateSchema:  state.Schema(),
		CacheVersion: store.CacheVersion(),
		FactsVersion: insights.FactsVersion,
	}
}

// versionLine is `todobem -version`: `todobem v0.1.0 linux/amd64 protocol 1 state 1`.
func versionLine() string {
	v := versionInfo()
	return fmt.Sprintf("todobem %s %s/%s protocol %d state %d", v.Version, v.OS, v.Arch, v.Protocol, v.StateSchema)
}

// runVersion is `todobem version [--json]`. It never reads the state, so it answers even when
// the state is newer than this binary.
func runVersion(args []string) int {
	fs := flag.NewFlagSet("todobem version", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "machine-readable")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !*asJSON {
		fmt.Println(versionLine())
		return 0
	}
	if err := json.NewEncoder(os.Stdout).Encode(versionInfo()); err != nil {
		return 1
	}
	return 0
}
