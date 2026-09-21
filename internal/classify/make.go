package classify

import (
	"regexp"
	"strings"
)

// make and ninja are judged by their targets. unwrapHead reduces the command line to
// `<tool> <target>…` (targetFields), matchHead resolves the targets against the rule table
// (matchTargets): the listed target of the highest priority decides (`make clean all` builds,
// `make build test` tests), a dry run runs nothing, a bare tool runs its default goal (the
// tool's own row), an artifact target (a path or a file name: build/app, libx.a, main.o — the
// CMake / autotools C and C++ case) builds, and any other unlisted target is unknown under its
// own name so `todobem unknown` lists it and the overlay can pin it — the dispatcher-verb
// precedent: an honest unknown beats a wrong build.

// toolSpec describes a build tool's command line for targetFields.
type toolSpec struct {
	valueOpts  map[string]bool // options whose value is the next word (-C dir, -f file)
	numberOpts map[string]bool // options whose value is an optional following number (-j 4)
	dryOpts    map[string]bool // long options after which nothing is built
	attached   string          // short letters whose value may be attached (-Cdir, -j8)
	dryLetters string          // short letters after which nothing is built (-n, -q)
	toolMode   string          // an option that switches the tool into a sub-tool (ninja -t)
}

var makeSpec = toolSpec{
	valueOpts:  set("-C", "-f", "-I", "-o", "-W", "-O", "--directory", "--file", "--makefile", "--include-dir", "--old-file", "--assume-old", "--new-file", "--what-if", "--assume-new", "--eval", "--output-sync", "--debug"),
	numberOpts: set("-j", "-l", "--jobs", "--load-average", "--max-load"),
	dryOpts:    set("--dry-run", "--just-print", "--recon", "--question", "--version", "--help"),
	attached:   "CfIoWOjl",
	dryLetters: "nqh",
}

var ninjaSpec = toolSpec{
	valueOpts:  set("-C", "-f", "-d", "-w"),
	numberOpts: set("-j", "-k", "-l"),
	dryOpts:    set("--version", "--help"),
	attached:   "Cfdwjkl",
	dryLetters: "nh",
	toolMode:   "-t",
}

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// reMakeAssign is a make variable assignment on the command line (VAR=1, CC:=clang, X+=y).
var reMakeAssign = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*[:+?!]?=`)

// reArtifactTarget is a target that names a file or a path rather than a phony goal.
var reArtifactTarget = regexp.MustCompile(`/|\.[A-Za-z0-9]+$`)

// targetFields reduces `<tool> [options] [VAR=val] <target>…` to `<tool> <target>…`; a dry run,
// a question or a version / help request becomes `<tool> -n`, ninja's `-t <sub-tool>` is kept
// as `<tool> -t <sub-tool>`, a redirection ends the target list, `--` ends the options.
func targetFields(tool string, args []string, spec toolSpec) []string {
	out := []string{tool}
	dry := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			for _, t := range args[i+1:] {
				if isRedirection(t) {
					break
				}
				if !reMakeAssign.MatchString(t) {
					out = append(out, t)
				}
			}
			i = len(args)
		case spec.toolMode != "" && a == spec.toolMode:
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				return []string{tool, a, args[i+1]}
			}
			return []string{tool, a}
		case spec.valueOpts[a]:
			i++
		case spec.numberOpts[a]:
			if i+1 < len(args) && isNumberish(args[i+1]) {
				i++
			}
		case strings.HasPrefix(a, "--"):
			if spec.dryOpts[strings.SplitN(a, "=", 2)[0]] {
				dry = true
			}
		case strings.HasPrefix(a, "-") && len(a) > 1:
			if strings.ContainsRune(spec.attached, rune(a[1])) {
				continue // -Cdir / -fMakefile / -j8: the value travels attached
			}
			if strings.ContainsAny(a[1:], spec.dryLetters) {
				dry = true
			}
		case reMakeAssign.MatchString(a):
		case isRedirection(a):
			i = len(args)
		default:
			out = append(out, a)
		}
	}
	if dry {
		return append([]string{tool, "-n"}, out[1:]...)
	}
	return out
}

// isRedirection reports a shell redirection word (`>`, `>>`, `2>&1`, `&>`, `<`).
func isRedirection(w string) bool {
	w = strings.TrimLeft(w, "0123456789")
	return strings.HasPrefix(w, ">") || strings.HasPrefix(w, "<") || strings.HasPrefix(w, "&>")
}

// matchTargets resolves `<tool> <target>…` (targetFields' output) against the word rules, the
// user overlay included: `<tool> -n` and `<tool> -t [<sub-tool>]` have their own rows, a bare
// tool is its own row, an artifact target is the tool's row, the listed target of the highest
// priority wins, and one unlisted phony target makes the whole call unknown under
// `<tool> <target>`.
func matchTargets(tool string, fields []string) (Rule, string, bool) {
	if len(fields) > 1 && fields[1] == "-n" {
		return wordRules[tool+" -n"], tool + " -n", true
	}
	if len(fields) > 1 && fields[1] == "-t" {
		if len(fields) > 2 {
			if r, ok := wordRules[tool+" -t "+fields[2]]; ok {
				return r, tool + " -t " + fields[2], true
			}
		}
		return wordRules[tool+" -t"], tool + " -t", true
	}
	best, label, found := wordRules[tool], tool, false
	for _, target := range fields[1:] {
		r, ok := wordRules[tool+" "+target]
		if !ok && reArtifactTarget.MatchString(target) {
			r, ok = wordRules[tool], true
		}
		if !ok {
			// a verb-first target (`test-one`, `deploy-prod`, `build-ios`, `ci-e2e`) is the listed
			// verb's row — the dispatcher-verb convention, read left to right like an npm script
			// variant; a verb anywhere else (`ios-test`, `clean-all`) is not read, the overlay
			// pins it
			if verb, ok2 := targetVerb(target); ok2 {
				if vr, ok3 := wordRules[tool+" "+verb]; ok3 {
					r, ok = vr, true
				}
			}
		}
		if !ok {
			return Rule{Match: tool + " " + target, Phase: Unknown, Kind: tool + " " + target, Note: "an unlisted target"}, tool + " " + target + " (unlisted target)", true
		}
		if !found || Priority[r.Phase] > Priority[best.Phase] {
			best, label, found = r, tool+" "+target, true
		}
	}
	return best, label, true
}

// targetVerb reads the first word of a hyphenated / underscored / dotted target when it is a
// dispatcher verb (`test-one` → test, `ci-deploy-prod` → deploy); false for anything else.
func targetVerb(target string) (string, bool) {
	w := strings.ToLower(target)
	w = strings.TrimPrefix(w, "ci-")
	w = strings.TrimPrefix(w, "ci_")
	if i := strings.IndexAny(w, "-_:."); i > 0 {
		w = w[:i]
	}
	if w == strings.ToLower(target) {
		return "", false // a single word was already looked up as a listed target
	}
	if _, ok := dispatcherVerbs[w]; !ok {
		return "", false
	}
	return w, true
}
