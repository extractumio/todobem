package classify

import "strings"

// unwrapHead reduces a simple command to the fields a rule is matched on: a leading env
// assignment, shell keywords (`if`, `while`, `!`) and wrapper words (sudo, nohup, exec, env,
// timeout, …) with their flags are stripped; `node_modules/.bin/<tool>` and `node …/cli.js`
// become the tool; `git` / `npm` / `pnpm` / `yarn` lose their option prefix so the subcommand
// is the second field. head is the executable as written (path kept), base its last path
// element. ok is false when nothing runs (`for x in …`, an empty command).
func unwrapHead(seg string) (fields []string, head, base string, ok bool) {
	seg = strings.TrimSpace(reSubst.ReplaceAllString(seg, ""))
	seg = strings.TrimSpace(reEnvPrefix.ReplaceAllString(seg, ""))
unwrap:
	for {
		fields := shellFields(seg)
		if len(fields) == 0 {
			return nil, "", "", false
		}
		h := fields[0]
		if shellKeywords[h] {
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
		break unwrap
	}
	fields = shellFields(seg)
	if reAssignment.MatchString(fields[0]) {
		return nil, "", "", false // a bare assignment (`R=/tmp/out.log`): nothing runs
	}
	head = fields[0]
	base = head
	if i := strings.LastIndex(head, "/"); i >= 0 {
		base = head[i+1:]
	}
	// node_modules/.bin/<tool> → <tool>; node … node_modules/<pkg>/…/cli.js <sub> → <pkg> <sub>
	if strings.Contains(head, "node_modules/.bin/") {
		head = base
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
	// npm/pnpm/yarn [--prefix DIR | -C DIR | -w PKG | --workspace PKG | --filter G] <sub> → <tool> <sub>
	if base == "npm" || base == "pnpm" || base == "yarn" {
		rest := fields[1:]
		for len(rest) > 0 {
			switch {
			case (rest[0] == "--prefix" || rest[0] == "-C" || rest[0] == "-w" || rest[0] == "--workspace" || rest[0] == "--filter" || rest[0] == "-F") && len(rest) > 1:
				rest = rest[2:]
			case strings.HasPrefix(rest[0], "--prefix=") || strings.HasPrefix(rest[0], "--workspace=") || strings.HasPrefix(rest[0], "--filter=") || rest[0] == "--silent" || rest[0] == "-s" || rest[0] == "--no-audit" || rest[0] == "--no-fund":
				rest = rest[1:]
			default:
				goto npmDone
			}
		}
	npmDone:
		fields = append([]string{base}, rest...)
	}
	return fields, head, base, true
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
