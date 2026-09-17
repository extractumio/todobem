package classify

import "strings"

// A harness stop hook is recorded as a wait_worker/hook op (harness time inside the turn), but
// its command is classified like any shell call; the verdict travels on the op's Rule in one
// format the adapter writes and the Insights read, so a hook that runs the project's tests is a
// recorded verification and a hook that runs something else is not.
const hookRulePrefix = "hook · "

// HookRule formats the classification of a stop hook's command for Operation.Rule.
func HookRule(phase Phase, kind string) string {
	return hookRulePrefix + string(phase) + "/" + kind
}

// HookPhase reads the phase a stop hook's command classified as back from Operation.Rule; ok is
// false for any op that is not a stop hook.
func HookPhase(rule string) (Phase, bool) {
	if !strings.HasPrefix(rule, hookRulePrefix) {
		return "", false
	}
	rest := rule[len(hookRulePrefix):]
	if i := strings.IndexByte(rest, '/'); i > 0 {
		return Phase(rest[:i]), true
	}
	return Phase(rest), true
}

// HookKind reads the kind a stop hook's command classified as back from Operation.Rule ("" for
// any op that is not a stop hook).
func HookKind(rule string) string {
	if !strings.HasPrefix(rule, hookRulePrefix) {
		return ""
	}
	rest := rule[len(hookRulePrefix):]
	if i := strings.IndexByte(rest, '/'); i > 0 {
		return rest[i+1:]
	}
	return ""
}

// StaticCheckKinds are the test-phase kinds that run no test: a linter, a type check, a shell
// syntax check. They verify a change the way the rule table says (the test phase), and the
// Insights name them apart so "verified" can be read as "verified by a lint only".
var StaticCheckKinds = map[string]bool{"lint": true, "typecheck": true, "syntax-check": true}
