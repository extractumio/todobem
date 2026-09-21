package codex

import (
	"regexp"
	"strings"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/source"
)

// Function calls that are not shell commands, mapped by their name and namespace (the harness
// records both): `run` in the `web` namespace is the browsing tool; a call in an `mcp__<server>`
// namespace is a connector (an MCP server: `mcp__codex_apps__github`, `mcp__xcodebuildmcp`),
// like the McpToolCall item; among the GitHub connector's `_`-prefixed tools the pull-request
// WRITES are release work, its review calls are review work, and every other call is a read.
// Nothing here reads the arguments beyond a title (docs/ARCHITECTURE.md §2.1).

// webRunTitle names a web tool call by its action: search_query / open / find / click.
func webRunTitle(arguments string) string {
	for _, action := range []string{"search_query", "open", "find", "click", "screenshot"} {
		if strings.Contains(arguments, `"`+action+`"`) {
			return "web " + action
		}
	}
	return "web run"
}

var (
	rePRWrite  = regexp.MustCompile(`^_?(create|update|merge|close|reopen|mark)_pull_request|_pull_request_ready|update_pull_request_branch|^_?merge_pull`)
	rePRReview = regexp.MustCompile(`pull_request_review|_pr_review|review_comment`)
)

// connectorOp maps one function call in an `mcp__*` namespace to its phase, kind and title.
func connectorOp(fc funcCall) (classify.Phase, string, string) {
	server := strings.TrimPrefix(fc.Namespace, "mcp__")
	title := "mcp " + server + "." + strings.TrimPrefix(fc.Name, "_")
	switch {
	case rePRReview.MatchString(fc.Name):
		return classify.Release, "pr review", title
	case rePRWrite.MatchString(fc.Name):
		return classify.Release, "pr", title
	}
	return classify.Code, "mcp", title
}

// The `exec` envelope's JavaScript is literal text; the commands it hands to `exec_command` are
// recovered from the literal forms only — cmd: "…" / cmd: '…', a cmd: template literal (kept
// verbatim, a ${x} hole left in), cmd = "…" / String.raw — and from a flat array of strings
// (const cmds = ["…", "…"]) when the envelope runs `exec_command`. A nested pair
// ([["label", "cmd"], …]) relies on the model's ad-hoc positional convention, which is not a
// literal: such an envelope is one named `unknown/exec-script` op with the JS as its detail.
var (
	reExecCmdLit  = regexp.MustCompile(`(?s)"?\bcmd"?\s*[:=]\s*(?:"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'|` + "`((?:[^`\\\\]|\\\\.)*)`" + `|String\.raw` + "`((?:[^`\\\\]|\\\\.)*)`" + `)`)
	reExecArray   = regexp.MustCompile(`(?s)(?:const|let|var)\s+\w+\s*=\s*\[(.*?)\]\s*;?`)
	reExecStrElem = regexp.MustCompile(`(?s)^\s*(?:"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'|` + "`((?:[^`\\\\]|\\\\.)*)`" + `)\s*$`)
)

// execCommands returns the commands an exec envelope runs, in order, or nil when none is
// literal. Template texts that still carry a `${…}` are returned (they classify by their
// literal words) but the caller must not key a provisional op on them.
func execCommands(input string) []string {
	var out []string
	for _, m := range reExecCmdLit.FindAllStringSubmatch(input, -1) {
		if s := firstGroup(m[1:]); strings.TrimSpace(s) != "" {
			out = append(out, unescapeJSON(s))
		}
	}
	if len(out) > 0 || !strings.Contains(input, "exec_command") {
		return out
	}
	for _, m := range reExecArray.FindAllStringSubmatch(input, -1) {
		elems := splitTopLevel(m[1])
		var cmds []string
		for _, e := range elems {
			sm := reExecStrElem.FindStringSubmatch(e)
			if sm == nil {
				cmds = nil
				break // a nested array or an expression: not a flat list of commands
			}
			if s := firstGroup(sm[1:]); strings.TrimSpace(s) != "" {
				cmds = append(cmds, unescapeJSON(s))
			}
		}
		if len(cmds) > 0 {
			return cmds
		}
	}
	return nil
}

func firstGroup(groups []string) string {
	for _, g := range groups {
		if g != "" {
			return g
		}
	}
	return ""
}

// splitTopLevel splits a JS array body at its top-level commas (brackets and quotes respected).
func splitTopLevel(body string) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case quote != 0:
			if c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'' || c == '`':
			quote = c
		case c == '[' || c == '{' || c == '(':
			depth++
		case c == ']' || c == '}' || c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, body[start:i])
			start = i + 1
		}
	}
	if rest := strings.TrimSpace(body[start:]); rest != "" {
		out = append(out, rest)
	}
	return out
}

// execScriptTitle names an envelope whose commands were not literal.
func execScriptTitle(input string) string {
	return "exec script (no command literal): " + source.Clip(source.FirstLine(strings.TrimSpace(input)), 80)
}
