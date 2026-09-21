package classify

import "strings"

// shellToken retains the original spelling of words, including quotes and escapes.
// Unsupported expansions/grouping return false so callers can preserve the input.
type shellToken struct {
	text string
	op   bool
}

func shellTokens(s string) ([]shellToken, bool) {
	var out []shellToken
	for i := 0; i < len(s); {
		if s[i] == ' ' || s[i] == '\t' {
			i++
			continue
		}
		start := i
		if s[i] == '#' {
			for i < len(s) && s[i] != '\n' {
				i++
			}
			out = append(out, shellToken{s[start:i], true})
			continue
		}
		// A descriptor is part of a redirection only when adjacent to its operator.
		j := i
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		if j < len(s) && (s[j] == '<' || s[j] == '>') {
			i = j
		}
		if strings.ContainsRune(";\n&|<>", rune(s[i])) {
			c := s[i]
			i++
			if i < len(s) {
				next := s[i]
				switch {
				case c == '&' && next == '>':
					i++
					if i < len(s) && s[i] == '>' {
						i++
					}
				case c == '<' && next == '<':
					i++
					if i < len(s) && (s[i] == '-' || s[i] == '<') {
						i++
					}
				case c == '>' && strings.ContainsRune(">|&", rune(next)),
					c == '<' && strings.ContainsRune(">&", rune(next)),
					c == '|' && (next == '|' || next == '&'),
					c == '&' && next == '&':
					i++
				}
			}
			out = append(out, shellToken{s[start:i], true})
			continue
		}
		var quote byte
		for i < len(s) {
			c := s[i]
			if quote != '\'' && (c == '`' || c == '$' && i+1 < len(s) && (s[i+1] == '(' || s[i+1] == '{')) {
				return nil, false
			}
			if c == '\\' && quote != '\'' {
				if i+1 >= len(s) || s[i+1] == '\n' {
					return nil, false
				}
				i += 2
				continue
			}
			if quote != 0 {
				if c == quote {
					quote = 0
				}
				i++
				continue
			}
			if c == '\'' || c == '"' {
				quote = c
				i++
				continue
			}
			if strings.ContainsRune("(){}", rune(c)) {
				return nil, false
			}
			if strings.ContainsRune(" \t\n;&|<>", rune(c)) {
				break
			}
			i++
		}
		if quote != 0 {
			return nil, false
		}
		out = append(out, shellToken{s[start:i], false})
	}
	return out, true
}

func stripOutputRedirects(tokens []shellToken) []shellToken {
	out := make([]shellToken, 0, len(tokens))
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t.op && i+1 < len(tokens) && !tokens[i+1].op {
			op := strings.TrimLeft(t.text, "0123456789")
			fd := strings.TrimSuffix(t.text, op)
			output := fd == "" || fd == "1" || fd == "2"
			if output && (op == ">" || op == ">>" || op == ">|" || op == "&>" || op == "&>>" ||
				op == ">&" && (tokens[i+1].text == "1" || tokens[i+1].text == "2")) {
				i++
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

func joinShellTokens(tokens []shellToken) string {
	var b strings.Builder
	prevNewline := true
	for _, t := range tokens {
		if t.text == "\n" && t.op {
			b.WriteByte('\n')
			prevNewline = true
			continue
		}
		if !prevNewline {
			b.WriteByte(' ')
		}
		b.WriteString(t.text)
		prevNewline = false
	}
	return b.String()
}

func shellControl(t shellToken) bool {
	return t.op && (strings.HasPrefix(t.text, "#") || t.text == ";" || t.text == "\n" || t.text == "&" || t.text == "&&" || t.text == "|" || t.text == "||" || t.text == "|&")
}

// heredocInterpreter recognizes interpreters reading source from stdin. Unknown
// option forms are conservative; a positional script or inline expression is data.
func heredocInterpreter(tokens []shellToken) string {
	tokens = stripOutputRedirects(tokens)
	var words []string
	for i := 0; i < len(tokens); i++ {
		t := tokens[i]
		if t.op {
			op := strings.TrimLeft(t.text, "0123456789")
			if (op == "<<" || op == "<<-") && i+1 < len(tokens) && !tokens[i+1].op {
				i++
				continue
			}
			return ""
		}
		if len(words) == 0 && isEnvAssignment(t.text) {
			continue
		}
		fields := shellFields(t.text)
		if len(fields) != 1 {
			return ""
		}
		words = append(words, fields[0])
	}
	words = unwrapStdinWrappers(words)
	if len(words) == 0 {
		return ""
	}
	head := words[0]
	if i := strings.LastIndexByte(head, '/'); i >= 0 {
		head = head[i+1:]
	}
	shell := head == "bash" || head == "sh" || head == "zsh"
	if !shell && head != "python" && head != "python3" && head != "node" && head != "ruby" && head != "perl" {
		return ""
	}
	stdin := false
	for i := 1; i < len(words); i++ {
		a := words[i]
		if a == "-" {
			break
		}
		if shell && a == "-s" {
			stdin = true
			continue
		}
		if a == "--" {
			if i+1 < len(words) && words[i+1] != "-" {
				return ""
			}
			break
		}
		if !strings.HasPrefix(a, "-") {
			if stdin {
				break
			}
			return ""
		}
		if a == "--help" || a == "--version" || a == "-h" || a == "-V" {
			return ""
		}
		if shell && !strings.HasPrefix(a, "--") && strings.ContainsAny(a[1:], "cn") {
			return ""
		}
		if (head == "python" || head == "python3") && (strings.HasPrefix(a, "-c") || strings.HasPrefix(a, "-m")) {
			return ""
		}
		if !shell && head != "python" && head != "python3" && (strings.HasPrefix(a, "-e") || strings.HasPrefix(a, "-E") || strings.HasPrefix(a, "-p") || strings.HasPrefix(a, "--eval") || strings.HasPrefix(a, "--print")) {
			return ""
		}
		// Options with a separate value do not name a script.
		if (head == "python" || head == "python3") && (a == "-W" || a == "-X") ||
			shell && (a == "-o" || a == "-O") ||
			head == "node" && (a == "--input-type" || a == "--require" || a == "--import" || a == "-r") ||
			head == "ruby" && a == "-r" {
			i++
			if i >= len(words) {
				return ""
			}
			continue
		}
		// Do not infer execution from options we do not understand (for example,
		// combined inline-code flags or modes that exit without reading stdin).
		known := false
		switch head {
		case "python", "python3":
			known = strings.HasPrefix(a, "-W") || strings.HasPrefix(a, "-X") ||
				!strings.HasPrefix(a, "--") && strings.Trim(a[1:], "bBdEiIOqPRsSuvx") == ""
		case "bash", "sh", "zsh":
			known = a == "--noprofile" || a == "--norc" || a == "--posix" ||
				!strings.HasPrefix(a, "--") && strings.Trim(a[1:], "abefhkmptuvxBCHPT") == ""
		case "node":
			known = strings.HasPrefix(a, "--input-type=") || strings.HasPrefix(a, "--require=") ||
				strings.HasPrefix(a, "--import=") || a == "--no-warnings" || a == "--enable-source-maps"
		case "ruby":
			known = a == "-w" || strings.HasPrefix(a, "-W") || strings.HasPrefix(a, "-r")
		case "perl":
			known = a == "-w" || a == "-W" || a == "-T" || a == "-t"
		}
		if !known {
			return ""
		}
	}
	if shell {
		return "shell"
	}
	return "script"
}

// These wrapper modes preserve stdin and argument boundaries. Lookup-only command
// modes and env's argument-splitting modes are deliberately not transparent.
func unwrapStdinWrappers(words []string) []string {
	for len(words) > 0 {
		switch words[0] {
		case "command":
			words = words[1:]
			if len(words) > 0 && words[0] == "-p" {
				words = words[1:]
			}
			if len(words) > 0 && strings.HasPrefix(words[0], "-") {
				return nil
			}
		case "env":
			words = words[1:]
			for len(words) > 0 && strings.HasPrefix(words[0], "-") {
				a := words[0]
				words = words[1:]
				if a == "--" {
					break
				}
				switch {
				case a == "-i" || a == "--ignore-environment" || strings.HasPrefix(a, "--unset="):
				case (a == "-u" || a == "--unset") && len(words) > 0:
					words = words[1:]
				default:
					return nil
				}
			}
			for len(words) > 0 && reAssignment.MatchString(words[0]) { // fields: quotes already removed
				words = words[1:]
			}
		default:
			return words
		}
	}
	return nil
}

// Follow only a plain cat's literal stdin directly into an interpreter. File
// arguments, transformations, output redirections, and replaced pipe input are data.
func pipedHeredocInterpreter(tokens []shellToken, lo, hi int) string {
	if hi >= len(tokens) || tokens[hi].text != "|" || tokens[lo].text != "cat" {
		return ""
	}
	for i := lo + 1; i < hi; i++ {
		op := strings.TrimLeft(tokens[i].text, "0123456789")
		if !tokens[i].op || (op != "<<" && op != "<<-") || i+1 >= hi || tokens[i+1].op {
			return ""
		}
		i++
	}
	end := hi + 1
	for end < len(tokens) && !shellControl(tokens[end]) {
		if tokens[end].op && strings.HasPrefix(strings.TrimLeft(tokens[end].text, "0123456789"), "<") {
			return "" // the interpreter's stdin is no longer the pipe
		}
		end++
	}
	return heredocInterpreter(tokens[hi+1 : end])
}
