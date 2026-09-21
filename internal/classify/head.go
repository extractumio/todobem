package classify

import (
	"regexp"
	"strings"
)

// unwrapHead reduces a simple command to the fields a rule is matched on: a leading env
// assignment, shell keywords (`if`, `while`, `!`) and wrapper words (sudo, nohup, exec, env,
// timeout, …) with their flags are stripped; `node_modules/.bin/<tool>` and `node …/cli.js`
// become the tool; a package runner (`npx`, `bunx`, `npm exec`, `pnpm dlx` / `exec`, `yarn dlx`
// / `exec`, `bun x`, `bundle exec`, `poetry run`, `uv run`, `pipenv run`) becomes the tool it
// runs; `git` / `npm` / `pnpm` / `yarn` lose their option prefix so the subcommand is the second
// field, `make` / `ninja` their options so the targets follow (targetFields); an npm script
// variant (`build:prod`, `test-e2e`) is its base script. head is the executable as written
// (path kept), base its last path element. ok is false when nothing runs (`for x in …`, an
// empty command).
func unwrapHead(seg string) (fields []string, head, base string, ok bool) {
	seg = strings.TrimSpace(stripSubstitutions(seg))
	seg = strings.TrimSpace(stripEnvPrefix(seg))
unwrap:
	for {
		fields := shellFields(seg)
		if len(fields) == 0 {
			return nil, "", "", false
		}
		h := fields[0]
		if i := strings.LastIndex(h, "/"); i >= 0 && wrapperWords[h[i+1:]] {
			h = h[i+1:] // `/usr/bin/time -l cmd`, `/usr/bin/env FOO=1 cmd`: the wrapper by its name
		}
		if shellKeywords[h] && !wrapperWords[h] { // `time` is a keyword with flags: the wrapper case below
			// `for x in …`, `select x in …` and `case $x in` are headers with no command in
			// them: the words after the keyword are a variable and a word list (`for log in
			// a.log b.log` must not classify as the macOS `log` tool). The body follows in
			// later segments (`do …`). `while` / `until` / `if` are followed by a command.
			if h == "for" || h == "select" || h == "case" || len(fields) == 1 {
				return nil, "", "", false
			}
			seg = strings.Join(fields[1:], " ")
			continue unwrap
		}
		switch h {
		case "sudo", "nohup", "exec", "command", "builtin", "nice", "timeout", "gtimeout", "env", "xargs", "caffeinate", "time":
			rest := fields[1:]
			if h == "command" && len(rest) > 0 && (rest[0] == "-v" || rest[0] == "-V") {
				break unwrap // `command -v x` is a probe: matched as its own word rule by the caller
			}
			for len(rest) > 0 && (strings.HasPrefix(rest[0], "-") || (h == "timeout" && isNumberish(rest[0])) || (h == "env" && strings.Contains(rest[0], "="))) {
				if h == "env" && (rest[0] == "-u" || rest[0] == "--unset") && len(rest) > 1 {
					rest = rest[2:]
					continue
				}
				rest = rest[1:]
			}
			if len(rest) > 0 {
				seg = strings.Join(rest, " ")
				continue unwrap
			}
			if h == "caffeinate" {
				break unwrap // a bare `caffeinate` is the command itself
			}
			return nil, "", "", false
		}
		// npx vite build → vite build; pnpm dlx / bundle exec / uv run … alike: the runner only
		// resolves the tool, the tool is what ran (`pnpm --filter web exec tsc` sheds its option
		// prefix first). A runner with nothing after its flags runs nothing.
		if rest, ok := runnerArgs(npmFields(fields)); ok {
			if len(rest) == 0 {
				return nil, "", "", false
			}
			seg = strings.Join(rest, " ")
			continue unwrap
		}
		break unwrap
	}
	fields = shellFields(seg)
	if reAssignment.MatchString(fields[0]) {
		return nil, "", "", false // a bare assignment (`R=/tmp/out.log`): nothing runs
	}
	if strings.HasPrefix(fields[0], "#") || strings.HasPrefix(fields[0], "-") {
		return nil, "", "", false // a comment, or a continuation fragment (`-t page.yml | head -1)`): nothing runs
	}
	if strings.HasSuffix(fields[0], "()") || strings.TrimSpace(fields[0]) == "…" || fields[0] == "\\" || len(fields) > 1 && fields[1] == "in" || sqlWords[fields[0]] {
		// a function definition header (`retry() {`) runs nothing; nor does a segment that is
		// only the `…` of a clipped record (shortTitle / Part.Segment clip the text this package
		// is later asked to name), a lone line continuation, or a `for` header whose keyword
		// was clipped away (`e in a b c`: `in` is reserved, no command reads so)
		return nil, "", "", false
	}
	head = fields[0]
	base = head
	if i := strings.LastIndex(head, "/"); i >= 0 {
		base = head[i+1:]
	}
	base = interpreterAlias(base) // python3.12 → python3: the rows are keyed by the plain name
	// node_modules/.bin/<tool> → <tool>; node … node_modules/<pkg>/…/cli.js <sub> → <pkg> <sub>
	if strings.Contains(head, "node_modules/.bin/") {
		head = base
		fields[0] = base
	}
	if base == "node" || base == "npx" {
		for i, a := range fields[1:] {
			if strings.HasPrefix(a, "-") {
				continue
			}
			if m := reNodePkgCLI.FindStringSubmatch(a); m != nil {
				fields = append([]string{m[1]}, fields[i+2:]...)
				head, base = m[1], m[1]
			}
			break
		}
	}
	// git [-C dir] [-c k=v] [--git-dir=…] [--no-pager] <sub> → git <sub>
	if base == "git" {
		rest := fields[1:]
		for len(rest) > 0 {
			switch {
			case (rest[0] == "-C" || rest[0] == "-c") && len(rest) > 1:
				rest = rest[2:]
			case strings.HasPrefix(rest[0], "--git-dir") || strings.HasPrefix(rest[0], "--work-tree") || rest[0] == "--no-pager" || strings.HasPrefix(rest[0], "-c"):
				rest = rest[1:]
			default:
				goto gitDone
			}
		}
	gitDone:
		fields = append([]string{"git"}, rest...)
	}
	fields = npmFields(fields)
	// npm run build:prod / pnpm test-e2e → the base script (build / test): a `<name>:<variant>`,
	// `<name>-<variant>` or `<name>_<variant>` script is that script's variant by the package.json
	// convention, and the rule table lists the base names (`build:watch` stays a build: a watcher
	// that compiles).
	if base == "npm" || base == "pnpm" || base == "yarn" || base == "bun" {
		for i := 1; i < len(fields) && i <= 2; i++ {
			if fields[i] == "run" {
				continue
			}
			if m := reScriptVariant.FindStringSubmatch(fields[i]); m != nil {
				fields[i] = m[1]
				if m[2] != "" {
					fields[i] = "test"
				}
			}
			break
		}
	}
	// make [-C dir] [-f file] [-jN] [-k] [VAR=val] <target>… → make <target>…; ninja alike.
	switch base {
	case "make":
		fields = targetFields("make", fields[1:], makeSpec)
	case "ninja":
		fields = targetFields("ninja", fields[1:], ninjaSpec)
	}
	return fields, head, base, true
}

// sqlWords are the upper-case SQL keywords a line of a quoted statement starts with once a
// nested quote broke the segmenter (a psql statement quoted inside an ssh command): data.
var sqlWords = set("SELECT", "WITH", "FROM", "WHERE", "GROUP", "ORDER", "LIMIT", "INSERT", "UPDATE", "DELETE", "CREATE", "ALTER", "DROP", "JOIN", "LEFT", "INNER", "UNION", "VALUES", "AND", "OR", "ON", "AS", "HAVING")

// wrapperWords are the words unwrapHead strips with their flags (a path form included).
var wrapperWords = set("sudo", "nohup", "exec", "command", "builtin", "nice", "timeout", "gtimeout", "env", "xargs", "caffeinate", "time")

// reScriptVariant is an npm script name that is a variant of a base script the table lists
// (`build:prod`, `e2e:install`, `bench:stop`, `lint:fix` → build, e2e, bench, lint).
var reScriptVariant = regexp.MustCompile(`^(build|test|e2e|bench|benchmark|lint|typecheck|check|verify|format|fmt|dev|start|serve|preview)(?:[:_-]\S*)?$|^(tests)$`)

// npmFields strips the option prefix of npm / pnpm / yarn / uv so the subcommand is the second
// field: [--prefix DIR | --cwd DIR | -C DIR | -w PKG | --workspace PKG | --filter G | --project
// DIR | --opt=value] <sub> → <tool> <sub>.
func npmFields(fields []string) []string {
	base := fields[0]
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	if base != "npm" && base != "pnpm" && base != "yarn" && base != "uv" {
		return fields
	}
	rest := fields[1:]
	for len(rest) > 0 {
		switch {
		case (rest[0] == "--prefix" || rest[0] == "--cwd" || rest[0] == "-C" || rest[0] == "-w" || rest[0] == "--workspace" || rest[0] == "--filter" || rest[0] == "-F" ||
			rest[0] == "--userconfig" || rest[0] == "--globalconfig" || rest[0] == "--registry" || rest[0] == "--project" || rest[0] == "--directory") && len(rest) > 1:
			rest = rest[2:]
		case strings.HasPrefix(rest[0], "--") && strings.Contains(rest[0], "="), // --prefix=DIR, --userconfig=/dev/null, --registry=URL: an option with its value attached
			rest[0] == "--silent" || rest[0] == "-s" || rest[0] == "-q" || rest[0] == "--quiet" || rest[0] == "--no-audit" || rest[0] == "--no-fund" || rest[0] == "--offline" || rest[0] == "--frozen":
			rest = rest[1:]
		default:
			return append([]string{base}, rest...)
		}
	}
	return []string{base}
}

// runners are the package runners that only resolve a tool and hand it the rest of the line,
// by their head word or word pair.
var runners = set("npx", "bunx", "npm exec", "npm x", "pnpm dlx", "pnpm exec", "yarn dlx", "yarn exec", "bun x", "bundle exec", "poetry run", "uv run", "pipenv run", "rokit run")

// runnerValueFlags are the runner options whose value is the next word (the union over the
// runners: npx -p / --package / -w, pnpm --filter / -C, poetry -C / -P, uv --with / --python /
// --project / --directory / --group / --extra / --env-file …); every other option is a switch.
var runnerValueFlags = set("-p", "--package", "-w", "--workspace", "--filter", "-F", "-C", "--directory", "-P", "--project",
	"--with", "--with-requirements", "--with-editable", "--python", "--group", "--only-group", "--no-group", "--extra", "--env-file", "--index", "--find-links", "--exclude-newer")

// runnerArgs strips a package-runner prefix (`npx -y vite build`, `pnpm dlx vite build`,
// `bundle exec rspec`, `uv run --python 3.12 pytest`) and reports what it runs, the tool's
// `@version` removed (`npx tsc@5`, `pnpm dlx create-vite@latest`, `@scope/name@1`); ok is false
// when the command is not a runner. `npx -c '…'` and `uv run -m mod` are opaque here (no tool
// word) and are left to their own head.
func runnerArgs(fields []string) (rest []string, ok bool) {
	switch {
	case len(fields) > 1 && runners[fields[0]+" "+fields[1]]:
		rest = fields[2:]
	case runners[fields[0]]:
		rest = fields[1:]
	default:
		return nil, false
	}
	for len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
		if rest[0] == "--" {
			rest = rest[1:]
			break
		}
		if rest[0] == "-c" || rest[0] == "-m" {
			return nil, false
		}
		if runnerValueFlags[rest[0]] && len(rest) > 1 {
			rest = rest[2:]
			continue
		}
		rest = rest[1:]
	}
	if len(rest) > 0 {
		rest = append([]string{stripVersion(rest[0])}, rest[1:]...)
	}
	return rest, true
}

// stripVersion drops a package spec's `@version` (`tsc@5` → `tsc`, `@scope/name@1` → `@scope/name`).
func stripVersion(pkg string) string {
	from := 0
	if strings.HasPrefix(pkg, "@") {
		from = 1
	}
	if i := strings.Index(pkg[from:], "@"); i >= 0 {
		return pkg[:from+i]
	}
	return pkg
}

// HeadWords names a simple command for a cross-session key: its normalized head and the
// subcommand after it (a flag is not a subcommand). A `bash -c '…'` wrapper is named by the
// inner command, an interpreter by its script or `-m` module. Empty when nothing runs.
func HeadWords(seg string) (head, sub string) {
	fields, head, base, ok := unwrapHead(seg)
	if !ok {
		return "", ""
	}
	args := fields[1:]
	if interpreters[base] {
		if base == "bash" || base == "sh" || base == "zsh" {
			for i, a := range args {
				if strings.HasPrefix(a, "-") && strings.Contains(a, "c") && !strings.HasPrefix(a, "--") && i+1 < len(args) {
					return HeadWords(strings.Join(args[i+1:], " "))
				}
			}
		}
		if len(args) > 1 && args[0] == "-m" {
			return head, "-m " + args[1]
		}
		for len(args) > 0 && strings.HasPrefix(args[0], "-") {
			args = args[1:]
		}
	}
	if len(args) > 0 {
		if w := strings.TrimRight(args[0], ";"); subcommandWord(w) { // a text not yet split at `;`
			sub = w
		}
	}
	return head, sub
}

// subcommandWord reports whether a word after the head can be a subcommand: not a flag and
// not a shell operator or redirection (`>`, `2>&1`, `|`).
func subcommandWord(w string) bool {
	if w == "" || strings.HasPrefix(w, "-") {
		return false
	}
	c := w[0]
	return c == '_' || c == '.' || c == '/' || c == '~' || c == '$' || c == '@' || c >= '0' && c <= '9' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}
