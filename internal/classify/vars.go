package classify

import (
	"regexp"
	"strings"
)

// A command often names its own tools through variables assigned a few segments earlier:
// `CLI=.build/debug/app; "$CLI" health`, `AB="agent-browser --session x"; $AB open …`,
// `S=/tmp/scratch; "$S/drag" 100 980`. The value is literal text of the same tool call, so the
// head is resolved from it before classification — never from an earlier call, a shell
// profile or a guess. Only a plain value is used: a `$(…)` substitution stays opaque, except
// the four lookups that name a tool (`$(which x)`, `$(command -v x)`, `$(xcrun -f x)`,
// `$(xcrun --find x)`), whose answer is the tool.

var (
	reVarAssign = regexp.MustCompile(`^([A-Za-z_][A-Za-z0-9_]*)=(.*)$`)
	reToolFind  = regexp.MustCompile(`^\$\((?:which|command -v|xcrun -f|xcrun --find)\s+([^\s)]+)\)$`)
	reVarHead   = regexp.MustCompile(`^"?\$(?:\{([A-Za-z_][A-Za-z0-9_]*)\}|([A-Za-z_][A-Za-z0-9_]*))((?:/[^\s"]*)?)"?(\s|$)`)
)

// resolveVars rewrites the head of every segment whose first word is a variable assigned by a
// bare-assignment segment before it. The env prefix of a segment is kept; the assignment
// segments themselves are left as they are (they run nothing).
func resolveVars(segs []string) []string {
	vars := map[string]string{}
	out := make([]string, len(segs))
	for i, seg := range segs {
		trimmed := strings.TrimSpace(seg)
		if m := reVarAssign.FindStringSubmatch(trimmed); m != nil && envAssignmentLen(trimmed) == len(trimmed) {
			if v, ok := literalValue(m[2]); ok {
				vars[m[1]] = v
			}
			out[i] = seg
			continue
		}
		out[i] = substituteHead(seg, vars)
	}
	return out
}

// literalValue reads an assignment's value as literal text: quotes removed, a tool lookup
// resolved to the tool, anything with another expansion rejected.
func literalValue(v string) (string, bool) {
	if m := reToolFind.FindStringSubmatch(v); m != nil {
		return m[1], true
	}
	if strings.HasPrefix(v, "\"$(") {
		if m := reToolFind.FindStringSubmatch(strings.Trim(v, "\"")); m != nil {
			return m[1], true
		}
	}
	if strings.ContainsAny(v, "`") || strings.Contains(v, "$(") || strings.Contains(v, "${") {
		return "", false
	}
	fields := shellFields(v)
	if len(fields) == 0 {
		return "", false
	}
	return strings.Join(fields, " "), true
}

// substituteHead replaces a leading `$NAME`, `"$NAME"`, `${NAME}` or `"$NAME/path"` with the
// variable's value when it is known; the rest of the segment is untouched.
func substituteHead(seg string, vars map[string]string) string {
	if len(vars) == 0 {
		return seg
	}
	lead := strings.TrimSpace(seg)
	prefix := lead[:len(lead)-len(strings.TrimSpace(stripEnvPrefix(lead)))]
	rest := lead[len(prefix):]
	m := reVarHead.FindStringSubmatchIndex(rest)
	if m == nil {
		return seg
	}
	name := ""
	if m[2] >= 0 {
		name = rest[m[2]:m[3]] // ${NAME}
	} else {
		name = rest[m[4]:m[5]] // $NAME
	}
	value, ok := vars[name]
	if !ok || value == "" {
		return seg
	}
	path := rest[m[6]:m[7]]
	if path != "" && strings.ContainsAny(value, " \t") {
		return seg // `$S/drag` with a multi-word S is not a path
	}
	if strings.HasPrefix(rest, "\"") && strings.ContainsAny(value, " \t") {
		value = "\"" + value + "\"" // `"$C"` with a path that holds a space stays one word
	}
	return prefix + value + path + rest[m[8]:]
}
