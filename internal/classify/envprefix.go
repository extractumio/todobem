package classify

import "strings"

// The env prefix of a simple command — `NAME=value NAME2=value2 cmd …` — is read the way the
// shell reads it: a value is a "…" or '…' span, a $(…) / ${…} / `…` expansion (parentheses
// balanced, quotes inside them respected) or an unquoted run, and it ends only at unquoted
// whitespace. A regexp with a `\S*` alternative backtracked into a quoted value that held a
// space, so `LOG="/tmp/x_$(date +%Y%m%d)_y.log"; ./deploy.sh` left `+%Y%m%d)_y.log"` behind as
// "a command" and `q="SELECT a, b FROM t"` left `a, b FROM t"`: an unknown part with a share of
// the wall clock. This scanner is the one definition of an env assignment word.

// stripEnvPrefix drops the leading assignments of a segment when a command follows them. A
// segment that is only assignments (`R=/tmp/out.log`, `q="SELECT a, b"`) is returned as it is,
// so unwrapHead still recognises it as a bare assignment that runs nothing.
func stripEnvPrefix(seg string) string {
	i := 0
	for {
		n := envAssignmentLen(seg[i:])
		if n == 0 {
			return seg[i:]
		}
		j := i + n
		if j >= len(seg) || (seg[j] != ' ' && seg[j] != '\t') {
			return seg // the assignment is the whole segment (or ends it)
		}
		for j < len(seg) && (seg[j] == ' ' || seg[j] == '\t') {
			j++
		}
		if j >= len(seg) {
			return seg
		}
		i = j
	}
}

// isEnvAssignment reports whether one shell word is a `NAME=value` assignment.
func isEnvAssignment(word string) bool {
	return word != "" && envAssignmentLen(word) == len(word)
}

// envAssignmentLen returns the length of the `NAME=value` word at the start of s, or 0 when s
// does not start with one. The value ends at unquoted whitespace or at the end of s.
func envAssignmentLen(s string) int {
	i := 0
	for i < len(s) && (isIdentByte(s[i]) || s[i] >= '0' && s[i] <= '9') {
		i++
	}
	if i == 0 || i >= len(s) || s[i] != '=' || s[0] >= '0' && s[0] <= '9' {
		return 0
	}
	i++
	if i < len(s) && s[i] == '(' {
		i = scanParens(s, i+1) // an array assignment: COMMON=(-i "a b.wav" --score x)
	}
	return scanValue(s, i)
}

func isIdentByte(c byte) bool {
	return c == '_' || c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z'
}

// scanValue reads a shell word from i and returns the index just past it.
func scanValue(s string, i int) int {
	for i < len(s) {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			i += 2
		case c == '\'':
			i = skipTo(s, i+1, '\'')
		case c == '"':
			i = scanDoubleQuoted(s, i+1)
		case c == '`':
			i = skipTo(s, i+1, '`')
		case (c == '$' || c == '<' || c == '>') && i+1 < len(s) && s[i+1] == '(':
			i = scanParens(s, i+2) // a command or a process substitution
		case c == '$' && i+1 < len(s) && s[i+1] == '{':
			i = skipTo(s, i+2, '}')
		case c == ' ' || c == '\t' || c == '\n':
			return i
		default:
			i++
		}
	}
	return len(s)
}

// scanDoubleQuoted reads to the closing quote of a "…" span opened before i; an expansion
// inside it may carry quotes and parentheses of its own.
func scanDoubleQuoted(s string, i int) int {
	for i < len(s) {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			i += 2
		case c == '"':
			return i + 1
		case c == '`':
			i = skipTo(s, i+1, '`')
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			i = scanParens(s, i+2)
		default:
			i++
		}
	}
	return len(s)
}

// scanParens reads to the parenthesis that closes a $( opened before i, nesting respected.
func scanParens(s string, i int) int {
	depth := 1
	for i < len(s) {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			i += 2
		case c == '\'':
			i = skipTo(s, i+1, '\'')
		case c == '"':
			i = scanDoubleQuoted(s, i+1)
		case c == '(':
			depth++
			i++
		case c == ')':
			depth--
			i++
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return len(s)
}

// skipTo returns the index just past the next c at or after i (the end of s when absent).
func skipTo(s string, i int, c byte) int {
	if j := strings.IndexByte(s[i:], c); j >= 0 {
		return i + j + 1
	}
	return len(s)
}

// stripSubstitutions removes every `$(…)` and backtick expansion from a segment, nesting and
// quotes inside them respected (`ids=$( (ls a | sed 's/x//'; ls b) | grep -v c)` is one
// expansion), so the words a rule is matched on are the command's own.
func stripSubstitutions(seg string) string {
	var b strings.Builder
	for i := 0; i < len(seg); {
		switch c := seg[i]; {
		case c == '\\' && i+1 < len(seg):
			b.WriteString(seg[i : i+2])
			i += 2
		case c == '\'':
			end := skipTo(seg, i+1, '\'')
			b.WriteString(seg[i:end])
			i = end
		case c == '$' && i+1 < len(seg) && seg[i+1] == '(':
			i = scanParens(seg, i+2)
		case c == '`':
			i = skipTo(seg, i+1, '`')
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}
