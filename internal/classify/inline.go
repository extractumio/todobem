package classify

import "strings"

// inlineSources reads the inline programs of a command's segments — `python3 -c '…'`, `node -e
// '…'`, `ruby -e '…'`, `perl -pe '…'`, `bash -c '…'` — as sources for the same literal signals
// a heredoc body gets (command): the program is right there in the call, in the interpreter's
// own syntax, so a `subprocess.run(['go','test'])`, an `assert`, a `write_text` or a
// `json.load(open(…))` says what the call did exactly as it would from stdin. A shell body is
// classified as the compound command it is (`bash -c 'go build && go test'` has two parts).
func inlineSources(segs []string) []heredocSource {
	var out []heredocSource
	for _, seg := range segs {
		fields, _, base, ok := unwrapHead(seg)
		if !ok || len(fields) < 3 {
			continue
		}
		body, interpreter := "", ""
		switch base {
		case "python", "python3":
			for i := 1; i+1 < len(fields); i++ {
				if fields[i] == "-c" {
					body, interpreter = fields[i+1], "script"
					break
				}
				if !strings.HasPrefix(fields[i], "-") || fields[i] == "-m" {
					break // a script file or a module: the body is not on the line
				}
			}
		case "node":
			for i := 1; i+1 < len(fields); i++ {
				a := fields[i]
				if a == "-e" || a == "--eval" || a == "-p" || a == "--print" {
					body, interpreter = fields[i+1], "script"
					break
				}
				if !strings.HasPrefix(a, "-") {
					break
				}
			}
		case "ruby", "perl":
			// `-e code`, `-E code`, a bundle whose last letter is e (`-pe`, `-0pi -e` …); `-r lib`
			// and `-I dir` take a value of their own
			for i := 1; i+1 < len(fields); i++ {
				a := fields[i]
				if a == "-r" || a == "-I" || a == "-M" {
					i++
					continue
				}
				if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && (strings.HasSuffix(a, "e") || strings.HasSuffix(a, "E")) {
					body, interpreter = fields[i+1], "script"
					break
				}
				if !strings.HasPrefix(a, "-") {
					break
				}
			}
		case "bash", "sh", "zsh":
			for i := 1; i+1 < len(fields); i++ {
				a := fields[i]
				if a == "-o" || a == "-O" {
					i++
					continue
				}
				if strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "c") {
					body, interpreter = fields[i+1], "shell"
					break
				}
				if !strings.HasPrefix(a, "-") {
					break
				}
			}
		}
		if body == "" || body == "-" || interpreter == "" {
			continue
		}
		out = append(out, heredocSource{body: body, interpreter: interpreter, inline: true})
	}
	return out
}
