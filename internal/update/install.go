package update

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/extractumio/todobem/internal/state"
)

// Names in the installation directory, ~/.todobem/bin. Frozen from the first release on.
const (
	BinName  = "todobem"
	PrevName = "todobem.prev"
	NewName  = ".todobem.new"
	LockName = "update.lock"
)

// ProbeTimeout bounds `<candidate> version --json`. A first launch on macOS can wait for the
// system's malware scan, hence generous.
const ProbeTimeout = 15 * time.Second

// Info is `todobem version --json`, the one thing an installed binary asks of another. Fields
// are only ever added.
type Info struct {
	Version      string `json:"version"`
	Commit       string `json:"commit"`
	OS           string `json:"os"`
	Arch         string `json:"arch"`
	Protocol     int    `json:"protocol"`
	StateSchema  int    `json:"state_schema"`
	CacheVersion int    `json:"cache_version"`
	FactsVersion int    `json:"facts_version"`
}

// ErrNotManaged is a binary outside the installation directory: a development build or a copy
// elsewhere is never replaced.
var ErrNotManaged = errors.New("not a managed installation")

// Install is one installation: the state directory (~/.todobem) and its bin/.
type Install struct{ Dir string }

func (in Install) BinDir() string { return filepath.Join(in.Dir, "bin") }
func (in Install) Bin() string    { return filepath.Join(in.BinDir(), BinName) }
func (in Install) Prev() string   { return filepath.Join(in.BinDir(), PrevName) }
func (in Install) New() string    { return filepath.Join(in.BinDir(), NewName) }

// Owns reports whether exe (os.Executable of the running process) is one of the named files
// of this installation, symlinks resolved.
func (in Install) Owns(exe string, names ...string) bool {
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return false
	}
	for _, n := range names {
		p, err := filepath.EvalSymlinks(filepath.Join(in.BinDir(), n))
		if err == nil && p == real {
			return true
		}
	}
	return false
}

// Lock takes the installation's update lock without waiting.
func (in Install) Lock() (*os.File, error) {
	f, err := state.Lock(filepath.Join(in.BinDir(), LockName), true)
	if errors.Is(err, state.ErrLocked) {
		return nil, errors.New("another upgrade or rollback is running")
	}
	return f, err
}

// Probe runs `<bin> version --json` and decodes the answer.
func Probe(bin string) (Info, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ProbeTimeout)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, "version", "--json")
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return Info{}, fmt.Errorf("%s did not answer within %s", bin, ProbeTimeout)
		}
		return Info{}, fmt.Errorf("%s cannot run: %v %s", bin, err, bytes.TrimSpace(errOut.Bytes()))
	}
	var info Info
	if err := json.Unmarshal(out.Bytes(), &info); err != nil || info.Version == "" {
		return Info{}, fmt.Errorf("%s: no version in its answer", bin)
	}
	return info, nil
}

// Stage extracts the binary of a checked archive to bin/.todobem.new and probes it: version,
// OS and architecture must be the ones asked for. A candidate that fails is removed; the
// installation is untouched either way.
func (in Install) Stage(archive string, want Version, goos, goarch string) (Info, error) {
	if err := ExtractBinary(archive, in.New()); err != nil {
		return Info{}, err
	}
	info, err := Probe(in.New())
	if err == nil && (info.Version != want.String() || info.OS != goos || info.Arch != goarch) {
		err = fmt.Errorf("the archive holds todobem %s %s/%s, not %s %s/%s", info.Version, info.OS, info.Arch, want, goos, goarch)
	}
	if err != nil {
		os.Remove(in.New())
		return Info{}, err
	}
	return info, nil
}

// Apply is the incoming binary switching itself in: the installed binary becomes todobem.prev,
// bin/.todobem.new becomes todobem. Renames only — a running todobem keeps its old file, and
// macOS never sees a signed binary overwritten in place.
func (in Install) Apply() error {
	if _, err := os.Stat(in.New()); err != nil {
		return fmt.Errorf("nothing staged: %w", err)
	}
	if _, err := os.Stat(in.Bin()); err == nil {
		tmp := in.Prev() + ".tmp"
		os.Remove(tmp)
		if err := os.Link(in.Bin(), tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, in.Prev()); err != nil {
			os.Remove(tmp)
			return err
		}
	}
	if err := os.Rename(in.New(), in.Bin()); err != nil {
		return err
	}
	return syncDir(in.BinDir())
}

// Rollback brings back the previous version: the state first, when a migration made it newer
// than the previous version reads (that needs every todobem of this user stopped), then the two
// binaries swap — a second rollback goes forward again. It returns the previous version's info
// and the backup it restored, if any.
func (in Install) Rollback() (Info, string, error) {
	if _, err := os.Stat(in.Prev()); errors.Is(err, os.ErrNotExist) {
		return Info{}, "", errors.New("no previous version: this is the first installed version")
	}
	prev, err := Probe(in.Prev())
	if err != nil {
		return Info{}, "", fmt.Errorf("the previous version: %w", err)
	}
	restored, err := state.RestoreFor(in.Dir, prev.StateSchema)
	if err != nil {
		return Info{}, "", err
	}
	tmp := in.Bin() + ".swap"
	os.Remove(tmp)
	if err := os.Rename(in.Bin(), tmp); err != nil {
		return Info{}, restored, err
	}
	if err := os.Rename(in.Prev(), in.Bin()); err != nil {
		os.Rename(tmp, in.Bin())
		return Info{}, restored, err
	}
	if err := os.Rename(tmp, in.Prev()); err != nil {
		return Info{}, restored, err
	}
	return prev, restored, syncDir(in.BinDir())
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
