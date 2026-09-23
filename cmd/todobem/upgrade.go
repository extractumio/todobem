package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/extractumio/todobem/internal/buildinfo"
	"github.com/extractumio/todobem/internal/settings"
	"github.com/extractumio/todobem/internal/state"
	"github.com/extractumio/todobem/internal/update"
)

// Exit codes of `todobem upgrade`.
const (
	exitOK           = 0
	exitFailed       = 1
	exitUsage        = 2
	exitIncompatible = 4
	exitBusy         = 5
	exitChecksum     = 6
)

const upgradeUsage = `usage: todobem upgrade check                 the installed and the latest version
       todobem upgrade [-version vX.Y.Z]       install the latest release, or that one
       todobem upgrade -file ARCHIVE           install a downloaded archive (its SHA256SUMS beside it)
       todobem upgrade -reinstall ...          the same version again (the whole path runs)
       todobem upgrade rollback                back to the previous version (stop todobem first)`

// upgradeFail prints `error[code]: message` and the next action, and returns the exit code.
func upgradeFail(exit int, code, msg, action string) int {
	fmt.Fprintf(os.Stderr, "error[%s]: %s\n", code, msg)
	if action != "" {
		fmt.Fprintln(os.Stderr, "  "+action)
	}
	return exit
}

// runUpgrade is `todobem upgrade`. It never runs the state gate: its recovery commands must
// work exactly when the state is newer than this binary.
func runUpgrade(args []string) int {
	in := update.Install{Dir: settings.Dir()}
	if in.Dir == "" {
		return upgradeFail(exitFailed, "no_home", "the home directory is unknown", "")
	}
	exe, err := os.Executable()
	if err != nil {
		return upgradeFail(exitFailed, "no_executable", err.Error(), "")
	}
	sub := ""
	if len(args) > 0 && len(args[0]) > 0 && args[0][0] != '-' {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "check":
		return upgradeCheck()
	case "rollback":
		return upgradeRollback(in, exe)
	case "apply":
		return upgradeApply(in, exe, args)
	case "":
		return upgradeInstall(in, exe, args)
	}
	fmt.Fprintln(os.Stderr, upgradeUsage)
	return exitUsage
}

func upgradeCheck() int {
	latest, err := update.NewRemote(buildinfo.ReleasesRepo).Latest()
	if err != nil {
		return upgradeFail(exitFailed, "network", err.Error(), "")
	}
	cur := buildinfo.Current()
	note := "run `todobem upgrade` to install it"
	if v, err := update.ParseVersion(cur); err == nil && v.Compare(latest) >= 0 {
		note = "up to date"
	}
	fmt.Printf("Installed %s; latest %s — %s.\n", cur, latest, note)
	return exitOK
}

// upgradeInstall is the installed binary's part: fetch, check, stage, hand over. What it does is
// frozen for as long as anyone runs this version, so it does no more than that.
func upgradeInstall(in update.Install, exe string, args []string) int {
	fs := flag.NewFlagSet("todobem upgrade", flag.ContinueOnError)
	want := fs.String("version", "", "install this release instead of the latest")
	file := fs.String("file", "", "install this downloaded archive; its SHA256SUMS must sit beside it")
	reinstall := fs.Bool("reinstall", false, "install even when the version is the one installed")
	fs.Usage = func() { fmt.Fprintln(os.Stderr, upgradeUsage) }
	if err := fs.Parse(args); err != nil || fs.NArg() > 0 || (*want != "" && *file != "") {
		fmt.Fprintln(os.Stderr, upgradeUsage)
		return exitUsage
	}
	if !buildinfo.IsRelease() || !in.Owns(exe, update.BinName) {
		return upgradeFail(exitIncompatible, "not_managed", "this todobem is not the installed release in "+in.BinDir(),
			"install one with the install command in the README; a development build is never replaced")
	}
	cur, err := update.ParseVersion(buildinfo.Version)
	if err != nil {
		return upgradeFail(exitIncompatible, "not_managed", err.Error(), "")
	}
	lock, err := in.Lock()
	if err != nil {
		return upgradeFail(exitBusy, "busy", err.Error(), "wait for it to finish")
	}
	defer lock.Close()

	remote := update.NewRemote(buildinfo.ReleasesRepo)
	var target update.Version
	var archive string
	var sums map[string]string
	switch {
	case *file != "":
		v, goos, goarch, err := update.ParseArchiveName(filepath.Base(*file))
		if err != nil {
			return upgradeFail(exitIncompatible, "incompatible", err.Error(), "")
		}
		if goos != runtime.GOOS || goarch != runtime.GOARCH {
			return upgradeFail(exitIncompatible, "incompatible", fmt.Sprintf("the archive is for %s/%s, this host is %s/%s", goos, goarch, runtime.GOOS, runtime.GOARCH), "")
		}
		b, err := os.ReadFile(filepath.Join(filepath.Dir(*file), update.SumsName))
		if err != nil {
			return upgradeFail(exitFailed, "no_sums", err.Error(), "put the release's SHA256SUMS next to the archive")
		}
		if sums, err = update.ParseSums(b); err != nil {
			return upgradeFail(exitChecksum, "verification_failed", err.Error(), "")
		}
		target, archive = v, *file
	case *want != "":
		if target, err = update.ParseVersion(*want); err != nil {
			return upgradeFail(exitUsage, "usage", err.Error(), "")
		}
	default:
		if target, err = remote.Latest(); err != nil {
			return upgradeFail(exitFailed, "network", err.Error(), "")
		}
	}
	switch c := target.Compare(cur); {
	case c < 0:
		return upgradeFail(exitIncompatible, "older", fmt.Sprintf("%s is older than the installed %s", target, cur), "`todobem upgrade rollback` goes back to the previous version")
	case c == 0 && !*reinstall:
		fmt.Printf("Already current: %s.\n", cur)
		return exitOK
	}

	name := update.ArchiveName(target.String(), runtime.GOOS, runtime.GOARCH)
	if archive == "" {
		if sums, err = remote.Sums(target); err != nil {
			return upgradeFail(exitFailed, "network", err.Error(), "")
		}
		dl, err := os.CreateTemp(in.BinDir(), ".download-*")
		if err != nil {
			return upgradeFail(exitFailed, "disk", err.Error(), "")
		}
		dl.Close()
		defer os.Remove(dl.Name())
		fmt.Fprintf(os.Stderr, "Downloading %s…\n", name)
		if err := remote.Download(target, name, dl.Name(), update.MaxArchive); err != nil {
			return upgradeFail(exitFailed, "network", err.Error(), "")
		}
		archive = dl.Name()
	}
	if err := update.CheckSum(archive, name, sums); err != nil {
		code := exitFailed
		if errors.Is(err, update.ErrChecksum) {
			code = exitChecksum
		}
		return upgradeFail(code, "verification_failed", err.Error(), "the installation is unchanged")
	}
	if _, err := in.Stage(archive, target, runtime.GOOS, runtime.GOARCH); err != nil {
		return upgradeFail(exitIncompatible, "incompatible", err.Error(), "the installation is unchanged")
	}
	lock.Close() // the incoming binary takes it again
	cmd := exec.Command(in.New(), "upgrade", "apply", "-from", cur.String())
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		return upgradeFail(exitFailed, "apply", err.Error(), "the installation is unchanged; retry `todobem upgrade`")
	}
	return exitOK
}

// upgradeApply is the incoming binary's part (it runs as bin/.todobem.new): switch itself in.
// This is where every later release may do more — restart a service, check it came up.
func upgradeApply(in update.Install, exe string, args []string) int {
	fs := flag.NewFlagSet("todobem upgrade apply", flag.ContinueOnError)
	from := fs.String("from", "", "the version being replaced")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if !in.Owns(exe, update.NewName) {
		return upgradeFail(exitUsage, "usage", "`upgrade apply` is run by `todobem upgrade`, not by hand", "")
	}
	lock, err := in.Lock()
	if err != nil {
		return upgradeFail(exitBusy, "busy", err.Error(), "")
	}
	defer lock.Close()
	if err := in.Apply(); err != nil {
		return upgradeFail(exitFailed, "apply", err.Error(), "")
	}
	fmt.Printf("Installed %s (previous %s). Restart todobem to use it.\n", buildinfo.Current(), *from)
	if f, ok, _ := state.Read(in.Dir); ok && f.Schema < state.Schema() {
		fmt.Printf("On its first start it migrates the state (schema %d → %d) after a backup; `todobem upgrade rollback` restores it.\n", f.Schema, state.Schema())
	}
	return exitOK
}

// upgradeRollback swaps the installed and the previous version back. It runs from either file,
// so a new version that cannot start is rolled back with ~/.todobem/bin/todobem.prev.
func upgradeRollback(in update.Install, exe string) int {
	if !in.Owns(exe, update.BinName, update.PrevName) {
		return upgradeFail(exitIncompatible, "not_managed", "this todobem is not the installed release in "+in.BinDir(), "")
	}
	lock, err := in.Lock()
	if err != nil {
		return upgradeFail(exitBusy, "busy", err.Error(), "")
	}
	defer lock.Close()
	prev, restored, err := in.Rollback()
	if err != nil {
		return upgradeFail(exitFailed, "rollback", err.Error(), "")
	}
	msg := "Restored " + prev.Version
	if restored != "" {
		msg += " and the state backup " + filepath.Join(in.Dir, restored) + " (changes made since are lost)"
	}
	fmt.Println(msg + ". Restart todobem to use it.")
	return exitOK
}
