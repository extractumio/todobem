// Package classify maps shell commands to phases using a deterministic rule table.
// It never looks at durations. Anything that matches no rule is Unknown.
package classify

import (
	"regexp"
	"strconv"
	"strings"
)

// Phase is the coarse activity category of an operation.
type Phase string

const (
	LLM         Phase = "llm"
	Code        Phase = "code"
	Build       Phase = "build"
	Test        Phase = "test"
	Release     Phase = "release"
	Infra       Phase = "infra"
	WaitWorker  Phase = "wait_worker"
	WaitUser    Phase = "wait_user"
	Idle        Phase = "idle"
	Compaction  Phase = "compaction"
	NoTelemetry Phase = "no_telemetry"
	Unknown     Phase = "unknown"
)

// Priority decides (a) which segment wins when parallel commands in one lane overlap and
// (b) which phase a multi-segment command gets. Higher wins. Code (shown as Development) covers
// every tool call around the code short of build, test and release: reading/searching sources,
// edits, local VCS, formatting, lookups and probes — never model output.
var Priority = map[Phase]int{
	Release: 90, Test: 80, Build: 70, WaitWorker: 65, Infra: 60, Code: 50,
	Compaction: 35, Unknown: 30, LLM: 20, WaitUser: 10, Idle: 10, NoTelemetry: 0,
}

// Result of classifying one command.
type Result struct {
	Phase    Phase  `json:"phase"`
	Kind     string `json:"kind"`     // finer label: e.g. "git push", "pytest", "sleep-loop"
	Remote   bool   `json:"remote"`   // targets a remote host (ssh / *_REMOTE_HOST=…)
	Queued   bool   `json:"queued"`   // a polling loop (while … sleep) precedes the main work
	Identity string `json:"identity"` // normalized command for retry grouping ("" if not groupable)
	Title    string `json:"title"`    // short human label
	Rule     string `json:"rule"`     // which rule matched (for the inspector)
	// Segment is the top-level simple command that decided the phase (its env prefix
	// stripped); "" when a regex rule, a heredoc body or Codex's own kind decided.
	Segment string `json:"segment,omitempty"`
	// Lifecycle is the SDLC stage pinned by the winning rule ("" = PhaseLifecycle(Phase)).
	Lifecycle Lifecycle `json:"lifecycle,omitempty"`
	// Parts are the command's segments that did work of their own, in order: the categories a
	// compound command chained (`go build && go test` → build, test). model.Derive shares the
	// op's wall clock among the distinct categories (docs/ARCHITECTURE.md §5.4). Glue — cd,
	// echo, reads, searches, edits, git, formatters, a pipe stage that only filters — is not
	// a part. A `sleep N` part carries its literal seconds in Ms.
	Parts []Part `json:"parts,omitempty"`
}

// Part is one working segment of a compound command.
type Part struct {
	Phase   Phase  `json:"phase"`
	Kind    string `json:"kind"`
	Segment string `json:"segment"`      // the segment text, clipped
	Ms      int64  `json:"ms,omitempty"` // literal evidence of its duration (sleep N), else 0
}

// partPhase reports whether a segment's phase is a category of its own in a compound command:
// the work phases, a wait (sleep, a poll loop) and an unknown command that ran something.
func partPhase(p Phase) bool {
	return p == Build || p == Test || p == Release || p == Infra || p == WaitWorker || p == Unknown
}

// unknownPartOK: an unknown segment no rule named is a part only when it reads as a command —
// a head with a path, an extension, a variable or a dash (`./app`, `"$CLI" health`,
// `some-tool run`) or a short line; a line of prose from a quoted block is not a command.
func unknownPartOK(kind string, fields []string) bool {
	if kind != "unknown" {
		return true // a rule named it (python, node, npm run, make <target>, deno run …)
	}
	if strings.ContainsAny(fields[0], "/.$-_:") || len(fields) <= 4 {
		return true
	}
	return len(fields) > 1 && strings.ContainsAny(fields[1], "/.$")
}

// partText is a part's segment for display and for the unknown-command loop: the shell
// keywords that introduced it (`do`, `then`, `if`) and its env assignments dropped.
func partText(seg string) string {
	fields := shellFields(seg)
	for len(fields) > 1 && (shellKeywords[fields[0]] || reAssignment.MatchString(fields[0])) {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return seg
	}
	if i := strings.Index(seg, fields[0]); i > 0 && len(fields) < len(shellFields(seg)) {
		if seg[i-1] == '"' || seg[i-1] == '\'' {
			i-- // a quoted head keeps its quote: `"/Applications/X.app/Contents/MacOS/X" --flag`
		}
		return seg[i:]
	}
	return seg
}

// PartCategories counts the distinct phases among parts.
func PartCategories(parts []Part) int {
	seen := map[Phase]bool{}
	for _, p := range parts {
		seen[p.Phase] = true
	}
	return len(seen)
}

// Rule is one row of the classification table. Match forms:
//
//	"word", "word sub" … up to four words  — exact head + subcommand match
//	"head:<re>"     — regexp on the executable's base name
//	"headpath:<re>" — regexp on the executable as written (with directories)
//	"seg:<re>"      — regexp on one top-level segment (env prefixes stripped)
//	"re:<re>"       — regexp on the whole command (heredoc bodies masked)
type Rule struct {
	Match string `json:"match"`
	Phase Phase  `json:"phase"`
	Kind  string `json:"kind"`
	Note  string `json:"note,omitempty"`
}

type reRule struct {
	re *regexp.Regexp
	r  Rule
}

var (
	wordRules     = map[string]Rule{}
	regexRules    []reRule // whole command
	segRules      []reRule // one segment
	headRules     []reRule // executable base name
	headPathRules []reRule // executable with path
)

// builtinRuleCount is the number of rules shipped in the binary; everything at or after this
// index in Rules was appended from a user config. Set once at init, before any user load.
var builtinRuleCount int

// BuiltinRuleCount lets the API label which served rules are built-in vs user-added.
func BuiltinRuleCount() int { return builtinRuleCount }

func init() {
	builtinRuleCount = len(Rules)
	compileRules()
}

// compileRules rebuilds the lookup structures from the current Rules slice. It is called once at
// init and again after user rules are appended (LoadUserConfig). Later entries win: a user rule
// with the same word key overrides the built-in, and among regex rules the highest-priority phase
// wins regardless of order, so a user rule can only ever add or raise, never silently mask.
func compileRules() {
	wordRules = map[string]Rule{}
	regexRules, segRules, headRules, headPathRules = nil, nil, nil, nil
	for _, r := range Rules {
		switch {
		case strings.HasPrefix(r.Match, "re:"):
			regexRules = append(regexRules, reRule{regexp.MustCompile(r.Match[3:]), r})
		case strings.HasPrefix(r.Match, "seg:"):
			segRules = append(segRules, reRule{regexp.MustCompile(r.Match[4:]), r})
		case strings.HasPrefix(r.Match, "head:"):
			headRules = append(headRules, reRule{regexp.MustCompile(r.Match[5:]), r})
		case strings.HasPrefix(r.Match, "headpath:"):
			headPathRules = append(headPathRules, reRule{regexp.MustCompile(r.Match[9:]), r})
		default:
			wordRules[r.Match] = r
		}
	}
}

var (
	reWS          = regexp.MustCompile(`\s+`)
	reHeredoc     = regexp.MustCompile(`<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)
	reRemote      = regexp.MustCompile(`(^|\s)(ssh|scp|rsync)\s|\b\w*REMOTE_HOST=|--remote\b|run-remote`)
	reScriptWrite = regexp.MustCompile(`write_text\(|write_bytes\(|open\([^)]*['"][wa]b?['"]|fs\.writeFile|writeFileSync|json\.dump\(|shutil\.|os\.rename|os\.remove|Path\([^)]*\)\.unlink`)
	reNodePkgCLI  = regexp.MustCompile(`node_modules/(?:@[\w.-]+/)?([\w.-]+)/(?:dist/|bin/|lib/)?[\w.-]*(?:cli|bin|index)[\w.-]*\.(?:m?js|cjs)$`)
	reScriptRead  = regexp.MustCompile(`read_text\(|read_bytes\(|json\.load\(|open\([^)]*\)|sqlite3\.connect\(|\.glob\(|os\.listdir|os\.walk|readFileSync|readdirSync|\breadFile\(|\.execute\(|\bfetch\(|new WebSocket\(|https?\.get\(|urlopen\(|requests\.get\(|YAML\.(?:load|parse|safe_load)_file\(|Psych\.(?:load|safe_load)_file\(|File\.read(?:lines)?\(|JSON\.parse\(File|Dir\[|Dir\.glob\(|importlib\.metadata|\.__version__`)
	reScriptTest  = regexp.MustCompile(`(?m)^\s*assert\b|\bassert\.(?:ok|equal|strictEqual|deepEqual|deepStrictEqual|match|throws)\(|\bassert\(|\bexpect\(|from ['"][^'"]*(?:playwright|puppeteer)[^'"]*['"]|require\(['"](?:playwright|puppeteer)|import pytest|import unittest`)
	reScriptDML   = regexp.MustCompile(`(?i)\b(insert\s+into|update\s+\w+\s+set|delete\s+from|drop\s+table|alter\s+table|create\s+table|replace\s+into)\b|\.commit\(`)
	reSubprocList = regexp.MustCompile(`(?:subprocess\.(?:run|call|check_call|check_output|Popen)|spawnSync|spawn|execFileSync|execFile)\(\s*\[([^\]]*)\]`)
	reArgvAssign  = regexp.MustCompile(`(?m)^\s*\w+\s*=\s*\[([^\]]*)\]`)
	reSubprocStr  = regexp.MustCompile(`(?:subprocess\.(?:run|call|check_call|check_output|Popen|getoutput|getstatusoutput)|os\.system|os\.popen|execSync|exec)\(\s*(?:f?r?)(?:"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)')`)
	reStrLit      = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"|'((?:[^'\\]|\\.)*)'`)
)

var (
	reWriteBody = regexp.MustCompile(`(?s)\.write_text\(\s*(?:r|f|rf|fr)?(?:'''(.*?)'''|"""(.*?)""")`)
	rePathLit   = regexp.MustCompile(`Path\(\s*['"]([^'"]+)['"]\s*\)|open\(\s*['"]([^'"]+)['"]\s*,\s*['"][wa]`)
	reCatHead   = regexp.MustCompile(`^cat\s*>{1,2}\s*(\S+)\s*<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?`)
)

// writtenScripts uses executable interpreter sources and actual cat heredoc writes.
// Data inside a file being written cannot itself supply evidence of another write.
func writtenScripts(sources []heredocSource) map[string]string {
	out := map[string]string{}
	for _, source := range sources {
		if source.writePath != "" {
			out[source.writePath] = source.body
		}
		if source.interpreter != "script" {
			continue
		}
		bodies := reWriteBody.FindAllStringSubmatch(source.body, -1)
		if len(bodies) == 0 {
			continue
		}
		var paths []string
		for _, m := range rePathLit.FindAllStringSubmatch(source.body, -1) {
			p := m[1]
			if p == "" {
				p = m[2]
			}
			paths = append(paths, p)
		}
		for _, m := range bodies {
			body := m[1]
			if body == "" {
				body = m[2]
			}
			if strings.TrimSpace(body) == "" {
				continue
			}
			for _, p := range paths {
				if _, dup := out[p]; !dup {
					out[p] = body
				}
			}
		}
	}
	return out
}

// executedPath returns the script path a segment executes (bash f, sh f, ./f, f), or "".
func executedPath(seg string) string {
	f := strings.Fields(strings.TrimSpace(stripEnvPrefix(strings.TrimSpace(seg))))
	if len(f) == 0 {
		return ""
	}
	h := f[0]
	if h == "bash" || h == "sh" || h == "zsh" || h == "source" || h == "." {
		for _, a := range f[1:] {
			if !strings.HasPrefix(a, "-") {
				return a
			}
		}
		return ""
	}
	if strings.Contains(h, "/") || strings.HasSuffix(h, ".sh") {
		return h
	}
	return ""
}

func samePath(a, b string) bool {
	a, b = strings.TrimPrefix(a, "./"), strings.TrimPrefix(b, "./")
	return a == b || strings.HasSuffix(a, "/"+b) || strings.HasSuffix(b, "/"+a)
}

// scriptCommands extracts literal shell commands from a script body: argv lists and string
// commands given to subprocess/os.system/execSync. Non-literal elements are ignored.
func scriptCommands(body string) []string {
	var out []string
	for _, m := range reSubprocList.FindAllStringSubmatch(body, -1) {
		var argv []string
		for _, lit := range reStrLit.FindAllStringSubmatch(m[1], -1) {
			v := lit[1]
			if v == "" {
				v = lit[2]
			}
			argv = append(argv, v)
		}
		if len(argv) > 0 {
			out = append(out, strings.Join(argv, " "))
		}
	}
	for _, m := range reSubprocStr.FindAllStringSubmatch(body, -1) {
		v := m[1]
		if v == "" {
			v = m[2]
		}
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	// argv lists bound to a variable and passed to subprocess later (cmd=[…]; subprocess.run(cmd))
	if strings.Contains(body, "subprocess.") || strings.Contains(body, "spawn") || strings.Contains(body, "execFile") {
		for _, m := range reArgvAssign.FindAllStringSubmatch(body, -1) {
			var argv []string
			for _, lit := range reStrLit.FindAllStringSubmatch(m[1], -1) {
				v := lit[1]
				if v == "" {
					v = lit[2]
				}
				argv = append(argv, v)
			}
			if len(argv) >= 2 && !strings.ContainsAny(argv[0], " \t") {
				out = append(out, strings.Join(argv, " "))
			}
		}
	}
	return out
}

// bodyVerdict is one thing a script body says: a rule to consider, the label the inspector
// shows, the segment it names (a literal inner command), the parts it adds and whether a
// poll loop precedes the work.
type bodyVerdict struct {
	rule    Rule
	label   string
	segment string
	parts   []Part
	queued  bool
}

// judgeBody reads a heredoc body or an inline program (`-c` / `-e`) for its literal signals,
// one level deep: a shell body is the compound command it is; a script's literal commands
// (subprocess argv, os.system, execSync) are classified with the same table; an assertion or
// a browser driver makes it a check, a file write a write, reads only a read. Anything else
// says nothing (an empty result), and the body stays whatever the line says.
func judgeBody(source heredocSource, depth int) []bodyVerdict {
	if source.interpreter == "" {
		return nil
	}
	b := source.body
	origin := "heredoc"
	if source.inline {
		origin = "inline program"
	}
	var out []bodyVerdict
	if source.interpreter == "shell" {
		r := command(b, "", depth+1)
		if r.Phase != Unknown {
			v := bodyVerdict{rule: Rule{Phase: r.Phase, Kind: "script→" + r.Kind}, label: "shell " + origin, queued: r.Queued}
			for _, pt := range r.Parts {
				v.parts = append(v.parts, Part{Phase: pt.Phase, Kind: "script→" + pt.Kind, Segment: pt.Segment, Ms: pt.Ms})
			}
			out = append(out, v)
		}
		return out
	}
	inner := scriptCommands(b)
	for _, ic := range inner {
		r := command(ic, "", depth+1)
		if r.Phase != Unknown {
			out = append(out, bodyVerdict{rule: Rule{Phase: r.Phase, Kind: "script→" + r.Kind}, label: origin + " runs: " + shortTitle(ic), segment: ic, queued: r.Queued,
				parts: []Part{{Phase: r.Phase, Kind: "script→" + r.Kind, Segment: shortTitle(ic)}}})
		}
	}
	if reScriptTest.MatchString(b) {
		out = append(out, bodyVerdict{rule: Rule{Phase: Test, Kind: "script-check"}, label: origin + " asserts / drives a browser"})
	}
	if reScriptWrite.MatchString(b) {
		out = append(out, bodyVerdict{rule: Rule{Phase: Code, Kind: "script-write"}, label: origin + " writes files"})
	} else if len(inner) == 0 && reScriptRead.MatchString(b) && !reScriptDML.MatchString(b) {
		out = append(out, bodyVerdict{rule: Rule{Phase: Code, Kind: "script-read"}, label: origin + " only reads/queries/probes"})
	}
	return out
}

// Command classifies a shell command string. codexKind is Codex's own parsed_cmd type
// ("read", "search", "list_files") when present; it short-circuits to Code.
func Command(cmd string, codexKind string) Result { return command(cmd, codexKind, 0) }

// command is the depth-limited worker: script bodies and written-then-executed files are
// classified one level down, never deeper.
func command(cmd string, codexKind string, depth int) Result {
	res := Result{Phase: Unknown, Kind: "unknown", Title: shortTitle(cmd)}
	masked, bodies := maskHeredocs(cmd)
	// heredoc bodies are data, not commands: a report that mentions ssh is not a remote run
	res.Remote = reRemote.MatchString(masked)
	switch codexKind {
	case "read", "search", "list_files":
		res.Phase, res.Kind, res.Rule = Code, codexKind, "codex:parsed_cmd"
		return res
	}
	best := -1
	bestSeg := ""
	// The highest-priority phase wins; at equal priority the first segment does, except that a
	// `shell` segment (`cd`, `echo`, `set`) yields to a substantive one: `cd x && rg foo` is
	// the search, and its exit code is the search's answer (a query miss, not a failed step).
	consider := func(r Rule, label string, seg string) {
		p := Priority[r.Phase]
		if p > best || p == best && res.Kind == "shell" && r.Kind != "shell" {
			best, res.Phase, res.Kind, res.Rule, bestSeg = p, r.Phase, r.Kind, label, seg
		}
	}
	loop := false
	// addPart records a working segment of the compound command (Result.Parts).
	addPart := func(ph Phase, kind, text string, ms int64) {
		if !partPhase(ph) {
			return
		}
		res.Parts = append(res.Parts, Part{Phase: ph, Kind: kind, Segment: shortTitle(text), Ms: ms})
	}
	for _, rr := range regexRules {
		if rr.re.MatchString(masked) {
			if rr.r.Kind == "poll-loop" {
				loop = true
			}
			consider(rr.r, "re:"+rr.r.Match[3:], "")
		}
	}
	segs, seps := segmentsWithSeparators(masked)
	segs = resolveVars(segs) // `CLI=…/app; "$CLI" health`: the head named by this very command
	// Every segment gets its own verdict (the op's is the best over all of them); the parts are
	// then read per pipeline and per loop block: `a | b | c` is one job named by its strongest
	// stage, `for … done` / `while … done` is one job named by its body (a body of sleeps is a
	// wait), and the heredoc bodies below add the literal commands they run.
	type verdict struct {
		rule       Rule
		fields     []string
		runs       bool
		text       string
		sleepMs    int64
		loopHeader bool
		loopEnd    bool
	}
	verdicts := make([]verdict, len(segs))
	for i, seg := range segs {
		seg = strings.TrimSpace(stripEnvPrefix(strings.TrimSpace(seg)))
		v := verdict{rule: Rule{Phase: Unknown, Kind: "unknown"}, text: seg}
		// a seg rule matches the segment as written or as unwrapped (`npx -y tsc --noEmit` and
		// `./node_modules/.bin/tsc --noEmit` are `tsc --noEmit`), so a runner or a path never
		// hides a verdict the word rule alone would get wrong
		norm := ""
		v.fields, _, _, v.runs = unwrapHead(seg)
		if v.runs {
			norm = strings.Join(v.fields, " ")
		}
		segBest := -1
		segConsider := func(r Rule) {
			if p := Priority[r.Phase]; p > segBest {
				segBest, v.rule = p, r
			}
		}
		ruled := false // a seg rule named this segment: the fallback tier has no say
		for _, rr := range segRules {
			if rr.re.MatchString(seg) || norm != "" && rr.re.MatchString(norm) {
				consider(rr.r, rr.r.Match, seg)
				segConsider(rr.r)
				ruled = ruled || rr.r.Phase != Unknown
			}
		}
		if r, label, ok := matchHeadOpt(seg, !ruled); ok {
			consider(r, label, seg)
			segConsider(r)
		}
		if depth <= 1 {
			// an inline program on this segment (`python3 -c '…'`) is judged here for the
			// segment's own verdict (the op-level pass below reads the same sources)
			for _, src := range inlineSources([]string{seg}) {
				for _, v := range judgeBody(src, depth) {
					segConsider(v.rule)
				}
			}
		}
		if v.rule.Kind == "sleep" && len(v.fields) > 1 {
			v.sleepMs = sleepMs(v.fields[1])
		}
		first := strings.SplitN(seg, " ", 2)[0]
		v.loopHeader = first == "for" || first == "while" || first == "until"
		v.loopEnd = seg == "done" || strings.HasPrefix(seg, "done ") || strings.HasPrefix(seg, "done;")
		verdicts[i] = v
	}
	// partOf names the category of one verdict, or "" for glue
	partOf := func(v verdict, piped bool) (Rule, bool) {
		r := v.rule
		switch {
		case !v.runs, v.text == "<<heredoc>>", strings.Contains(v.text, "<<") && r.Phase == Unknown:
			return r, false // nothing ran, or a heredoc consumer judged by its body below
		case piped && r.Phase == Unknown:
			return r, false // an unknown pipe stage (`| xcbeautify`) consumes the output
		case r.Phase == Infra && !infraAttemptKinds[r.Kind]:
			return r, false // a chmod, a kill, an open, a cleanup: glue around the work
		case r.Phase == Unknown && !unknownPartOK(r.Kind, v.fields):
			return r, false
		}
		return r, partPhase(r.Phase)
	}
	for i := 0; i < len(segs); i++ {
		if verdicts[i].loopHeader {
			// the block up to its `done` is one job: the strongest non-sleep body verdict, else a
			// wait (a poll loop; how many rounds it ran is not in the log, so no literal seconds)
			depth, end := 0, len(segs)
			for j := i; j < len(segs); j++ {
				if verdicts[j].loopHeader {
					depth++
				}
				if verdicts[j].loopEnd {
					depth--
					if depth == 0 {
						end = j
						break
					}
				}
			}
			// the part is named by the body segment that decided, not by the header: `for e in
			// a b c` says nothing about what ran, `./sim $e` does
			best, bestP, found, bestText := Rule{}, -1, false, ""
			for j := i + 1; j < end; j++ {
				v := verdicts[j]
				if v.rule.Kind == "sleep" || v.loopHeader || v.loopEnd {
					continue
				}
				if r, ok := partOf(v, seps[j] == '|'); ok && Priority[r.Phase] > bestP {
					best, bestP, found, bestText = r, Priority[r.Phase], true, partText(v.text)
				}
			}
			if found {
				addPart(best.Phase, best.Kind, bestText, 0)
			} else {
				addPart(WaitWorker, "poll-loop", partText(segs[i]), 0)
			}
			i = end
			continue
		}
		// a pipeline: the segments joined by `|` from here, one job named by its strongest stage
		j := i
		for j+1 < len(segs) && seps[j+1] == '|' {
			j++
		}
		best, bestP, found := Rule{}, -1, false
		text, ms := partText(verdicts[i].text), int64(0)
		for k := i; k <= j; k++ {
			if r, ok := partOf(verdicts[k], k > i); ok && Priority[r.Phase] > bestP {
				best, bestP, found = r, Priority[r.Phase], true
				text, ms = partText(verdicts[k].text), verdicts[k].sleepMs
			}
		}
		if found {
			addPart(best.Phase, best.Kind, text, ms)
		}
		i = j
	}
	// Opaque interpreter scripts (heredoc bodies). Only explicit, literal signals are used:
	//  - literal commands passed to subprocess/os.system/execSync are classified with this
	//    same table (one level deep); the script takes the strongest of them;
	//  - bodies that write files → develop; bodies that only read/query → explore;
	//  - anything else stays unknown.
	if depth > 1 {
		bodies = nil
	} else {
		bodies = append(bodies, inlineSources(segs)...) // `python3 -c '…'`, `bash -c '…'`: the same signals
	}
	for _, source := range bodies {
		for _, v := range judgeBody(source, depth) {
			consider(v.rule, v.label, v.segment)
			loop = loop || v.queued
			for _, pt := range v.parts {
				addPart(pt.Phase, pt.Kind, pt.Segment, pt.Ms)
			}
		}
	}
	// A file written in this command with a literal body (Path(f).write_text('''…''') or
	// cat > f <<EOF) and executed by a later segment (bash f / ./f) is classified by the
	// content that was written — the text is right there in the command.
	if depth <= 1 {
		if written := writtenScripts(bodies); len(written) > 0 { // the inline programs are in bodies too
			for _, seg := range segments(masked) {
				path := executedPath(seg)
				if path == "" {
					continue
				}
				for wp, content := range written {
					if samePath(wp, path) {
						r := command(content, "", depth+1)
						if r.Phase != Unknown {
							consider(Rule{Phase: r.Phase, Kind: "script→" + r.Kind}, "runs a script written in this command", seg)
							loop = loop || r.Queued || r.Phase == WaitWorker
							for _, pt := range r.Parts {
								addPart(pt.Phase, "script→"+pt.Kind, pt.Segment, pt.Ms)
							}
						}
					}
				}
			}
		}
	}
	if best < 0 {
		res.Phase, res.Kind, res.Rule = Unknown, "unknown", ""
	}
	res.Lifecycle = LifecyclePins[res.Kind]
	// A polling loop followed by real work: the work wins, but remember the queue.
	if loop && res.Phase != WaitWorker {
		res.Queued = true
	}
	res.Segment = bestSeg
	// Title: the segment that decided the phase, so "x=1; while …; done; run-tests.sh" reads as the test.
	if bestSeg != "" && !strings.HasPrefix(cmd, bestSeg) {
		res.Title = shortTitle(bestSeg)
		if len(segments(masked)) > 1 {
			res.Title += "  (+" + strconv.Itoa(len(segments(masked))-1) + " more)"
		}
	}
	// Attempt phases: a repeated identical command is a rerun or a retry (model.assignGroups).
	// Infra joins only for kinds whose exit code is a verdict (infraAttemptKinds), so a failed
	// `docker run …` and its identical retry form a group while a `pkill` that exits 1 because
	// nothing matched never reads as a failed attempt.
	if res.Phase == Test || res.Phase == Build || res.Phase == Release || (res.Phase == Infra && infraAttemptKinds[res.Kind]) {
		res.Identity = Identity(cmd)
	}
	return res
}

// infraAttemptKinds are the infra kinds whose exit code is a verdict on the command, so an
// identical repeat after a failure is a retry worth grouping. Routine kinds are left out on
// purpose: kill/pkill exit 1 when nothing matched, chmod/open/osascript/caffeinate/cleanup/
// diagnostics/media/service commands are repeated as a matter of course, systemctl/launchctl/
// journalctl are queried, not attempted. Grow the set from cmd/dump evidence.
var infraAttemptKinds = map[string]bool{"docker": true, "docker compose": true, "kubectl": true, "ssh": true, "scp": true, "rsync": true, "brew": true, "apt": true, "rustup": true, "setup-script": true, "remote vm": true, "devicectl": true}

// queryKinds are the code-phase kinds whose command only reads state, so a non-zero exit is an
// answer to the agent (no match, not there, not installed, has changes, bad path) rather than a
// step that failed. Ops of these kinds keep the harness's literal status and exit but are not
// counted in Totals.Failed (model.Operation.QueryMiss). Edits, patches, scripts, `shell` and
// everything outside the code phase stay verdicts. Grow the set from cmd/dump evidence.
var queryKinds = map[string]bool{
	"read": true, "search": true, "list": true, "list_files": true, "probe": true, "inspect": true, "process": true, "system": true, "version": true,
	"git": true, "git diff": true, "git show": true, "git status": true, "git log": true, "git blame": true,
	"docker": true, "docker logs": true, "kubectl": true, "kubectl describe": true, "kubectl logs": true,
	"gh": true, "glab": true, "glab mr": true, "glab ci": true, "go list": true, "go doc": true, "go env": true, "npm": true, "cargo": true,
	"devicectl": true, "simulator": true, "xcode": true, "codesign": true, "dns": true, "net": true,
	"help": true, "keychain": true, "ssh-agent": true, "pip": true, "linear query": true,
}

// QueryKind reports whether a non-zero exit of an op of this phase and kind is an answer rather
// than a failure: the code-phase query kinds (queryKinds), and a CI status wait (`gh pr checks`,
// `gh run watch`: kind `ci` in the wait_worker phase) whose exit code says the checks are still
// pending or failing — the state of somebody else's run, not a step of this agent that failed.
// Kinds carry a "|role" suffix once grouped; only the base counts.
func QueryKind(phase Phase, kind string) bool {
	switch phase {
	case Code:
		return queryKinds[BaseKind(kind)]
	case WaitWorker:
		return BaseKind(kind) == "ci"
	}
	return false
}

// cosmeticFilters are pipe stages that only shape output; they never change what ran.
var cosmeticFilters = map[string]bool{"tee": true, "tail": true, "head": true, "grep": true, "rg": true, "wc": true, "cat": true, "sed": true, "awk": true, "cut": true, "sort": true, "uniq": true, "xcbeautify": true, "xcpretty": true, "jq": true, "tr": true, "less": true, "column": true}

// Identity removes supported output redirections and trailing cosmetic pipe stages.
// Words retain their original quoting/whitespace. Complex shell input stays verbatim.
func Identity(cmd string) string {
	tokens, ok := shellTokens(cmd)
	if !ok {
		return cmd
	}
	for _, t := range tokens {
		if t.op && (strings.Contains(t.text, "<<") || strings.HasPrefix(t.text, "#")) {
			return cmd
		}
	}
	tokens = stripOutputRedirects(tokens)
	// Only a final simple pipe stage is cosmetic; never remove later &&/;/newline work.
	for {
		pipe := -1
		for i := len(tokens) - 1; i >= 0; i-- {
			if tokens[i].op {
				if tokens[i].text == "|" {
					pipe = i
				}
				break
			}
		}
		if pipe < 0 || pipe+1 >= len(tokens) || !cosmeticFilters[tokens[pipe+1].text] {
			break
		}
		tokens = tokens[:pipe]
	}
	return joinShellTokens(tokens)
}

// subshell reports a segment that is a parenthesised group `( … )` with its inside and what
// follows the closing paren (a redirection, or nothing); ok is false for anything else.
func subshell(s string) (inner, rest string, ok bool) {
	if !strings.HasPrefix(s, "(") {
		return "", "", false
	}
	depth, inS, inD := 0, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s):
			i++
		case inS:
			inS = c != '\''
		case inD:
			inD = c != '"'
		case c == '\'':
			inS = true
		case c == '"':
			inD = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				inner, rest = strings.TrimSpace(s[1:i]), strings.TrimSpace(s[i+1:])
				return inner, rest, inner != ""
			}
		}
	}
	return "", "", false
}

// segments splits a (heredoc-masked) command into top-level simple commands.
func segments(cmd string) []string {
	out, _ := segmentsWithSeparators(cmd)
	return out
}

// segmentsWithSeparators is segments plus, per segment, the operator that introduced it:
// '|' for a pipe stage, '&' for `&&`, ';' for `;` / a newline, 'o' for `||`, 0 for the first.
func segmentsWithSeparators(cmd string) ([]string, []byte) {
	var out []string
	var seps []byte
	var cur strings.Builder
	depth := 0
	inS, inD := false, false
	sep := byte(0)
	flush := func() {
		s := strings.TrimSpace(cur.String())
		cur.Reset()
		if s == "" {
			return
		}
		// a subshell `(cd x && node y) 2>&1` is its inner commands; what follows the closing
		// paren (a redirection) rides on the last of them; a keyword or `time` before the paren
		// (`do (nc -z h $p)`, `time (node x; tail y)`) introduces the same commands
		for _, kw := range []string{"do ", "then ", "else ", "time ", "! "} {
			if strings.HasPrefix(s, kw) && strings.HasPrefix(strings.TrimSpace(s[len(kw):]), "(") {
				s = strings.TrimSpace(s[len(kw):])
			}
		}
		if inner, rest, ok := subshell(s); ok {
			segs, innerSeps := segmentsWithSeparators(inner)
			for i, sg := range segs {
				if i == len(segs)-1 && rest != "" {
					sg += " " + rest
				}
				out = append(out, sg)
				if i == 0 {
					seps = append(seps, sep)
				} else {
					seps = append(seps, innerSeps[i])
				}
			}
			return
		}
		out = append(out, s)
		seps = append(seps, sep)
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == '#' && !inS && !inD && depth == 0 && (i == 0 || cmd[i-1] == ' ' || cmd[i-1] == '\t' || cmd[i-1] == '\n' || cmd[i-1] == ';'):
			// a comment runs to the end of its line and is not a command: an apostrophe in it
			// (`# the build's seed`) must not open a quote over the lines that follow
			for i < len(cmd) && cmd[i] != '\n' {
				i++
			}
			i--
			continue
		case c == '\\' && i+1 < len(cmd) && cmd[i+1] == '\n' && !inS:
			cur.WriteByte(' ') // a line continuation joins the lines
			i++
			continue
		case c == '\\' && i+1 < len(cmd):
			cur.WriteByte(c)
			i++
			cur.WriteByte(cmd[i])
			continue
		case inS:
			if c == '\'' {
				inS = false
			}
		case c == '$' && i+1 < len(cmd) && cmd[i+1] == '(':
			// a $(…) substitution is copied whole, its own quotes and parentheses balanced
			// (scanParens): `X="$(python -c "print(1)")"` must not close the outer quote early
			end := scanParens(cmd, i+2)
			cur.WriteString(cmd[i:end])
			i = end - 1
			continue
		case inD:
			if c == '"' {
				inD = false
			}
		case c == '\'':
			inS = true
		case c == '"':
			inD = true
		case c == '(' || c == '{':
			depth++
		case c == ')' || c == '}':
			if depth > 0 {
				depth--
			}
		case depth == 0 && (c == '\n' || c == ';'):
			flush()
			sep = ';'
			continue
		case depth == 0 && c == '&' && i+1 < len(cmd) && cmd[i+1] == '&':
			flush()
			sep = '&'
			i++
			continue
		case depth == 0 && c == '|':
			flush()
			sep = '|'
			if i+1 < len(cmd) && cmd[i+1] == '|' {
				sep = 'o'
				i++
			} else if i+1 < len(cmd) && cmd[i+1] == '&' {
				i++
			}
			continue
		}
		cur.WriteByte(c)
	}
	flush()
	return out, seps
}

var shellKeywords = map[string]bool{"if": true, "then": true, "else": true, "elif": true, "fi": true, "for": true, "while": true, "until": true, "do": true, "done": true, "case": true, "esac": true, "in": true, "function": true, "select": true, "time": true, "!": true, "{": true, "}": true}

var readOnlySub = map[string]bool{"status": true, "list": true, "show": true, "info": true, "help": true, "--help": true, "-h": true, "--version": true, "version": true, "describe": true, "check": true, "validate": true, "plan": true, "diff": true, "doctor": true, "env": true, "config": true, "ps": true, "print": true}

// interpreters run a script named by their first positional argument (matchHead judges the
// script name); `pythonX.Y` and `python3.Y` are `python3` (interpreterAlias).
var interpreters = map[string]bool{"python": true, "python3": true, "node": true, "ruby": true, "perl": true, "bash": true, "sh": true, "zsh": true, "deno": true, "bun": true, "swift": true, "luau": true, "lua": true, "Rscript": true, "php": true}

// rePythonVersion is a versioned python executable name (python3.12, python2.7, python3).
var rePythonVersion = regexp.MustCompile(`^python([23])(?:\.\d+)?$`)

// interpreterAlias maps a versioned interpreter name to the name the rule table lists.
func interpreterAlias(base string) string {
	if m := rePythonVersion.FindStringSubmatch(base); m != nil {
		if m[1] == "3" {
			return "python3"
		}
		return "python"
	}
	return base
}

// matchHead finds the rule for a simple command by its executable and optional subcommand:
// the rule chain (matchHeadRules), then — only while the phase is still Unknown after every
// rule, the `go run` / `python3` / app-bundle rows included — the literal signals of the
// fallback tier (unknownFallback).
func matchHead(seg string) (Rule, string, bool) { return matchHeadOpt(seg, true) }

// matchHeadOpt is matchHead with the fallback tier switched off when a `seg:` rule (an
// overlay's, typically) already named the segment: "after every rule" includes those.
func matchHeadOpt(seg string, fallback bool) (Rule, string, bool) {
	fields, head, base, ok := unwrapHead(seg)
	if !ok {
		return Rule{}, "", false
	}
	r, label, found := matchHeadRules(fields, head, base)
	if fallback && (!found || r.Phase == Unknown) {
		if fr, flabel, ok := unknownFallback(fields); ok {
			return fr, flabel, true
		}
	}
	return r, label, found
}

// withoutRedirections drops the redirection words of an argument list (`2>&1`, `> log`).
func withoutRedirections(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if isRedirection(args[i]) {
			op := strings.TrimLeft(args[i], "0123456789")
			if (op == ">" || op == ">>" || op == "<") && i+1 < len(args) {
				i++ // the target of a bare `>` / `>>` / `<`
			}
			continue
		}
		out = append(out, args[i])
	}
	return out
}

// unknownFallback reads the literal signals an otherwise-unknown command carries: a server-only
// listen flag (`--listen`, `--bind`, `--addr`; a `--port` is on both sides of a socket and is
// not one) makes it a service; an SQL statement as an argument (`"$DB" "SELECT …"`) makes it a
// query of the code phase. Nothing else is inferred.
func unknownFallback(fields []string) (Rule, string, bool) {
	for _, a := range fields[1:] {
		w := strings.ToLower(a)
		if i := strings.IndexByte(w, '='); i >= 0 {
			w = w[:i]
		}
		switch w {
		case "--listen", "--bind", "--addr", "-addr", "-listen", "-bind":
			return Rule{Match: "flag:" + w, Phase: Infra, Kind: "service"}, "flag " + w, true
		}
	}
	for _, a := range fields[1:] {
		if reSQLStatement.MatchString(a) {
			return Rule{Match: "arg:sql", Phase: Code, Kind: "sql"}, "an SQL statement as an argument", true
		}
	}
	return Rule{}, "", false
}

// reSQLStatement is a literal SQL statement passed as one argument.
var reSQLStatement = regexp.MustCompile(`(?is)^\s*(select|with|pragma|insert\s+into|update|delete\s+from|create\s+table|alter\s+table|drop\s+table|explain)\s`)

// matchHeadRules is the rule chain for one simple command.
func matchHeadRules(fields []string, head, base string) (Rule, string, bool) {
	if base == "command" && len(fields) > 1 && (fields[1] == "-v" || fields[1] == "-V") {
		return wordRules["command -v"], "command -v", true
	}
	// go run <pkg> / cargo run --bin <name>: judge by the package or binary name
	if (base == "go" || base == "cargo") && len(fields) > 2 && fields[1] == "run" {
		for _, a := range fields[2:] { // a server flag describes the runtime behaviour best
			w := strings.TrimLeft(strings.ToLower(a), "-")
			if w == "serve" || w == "server" {
				return Rule{Match: "flag:serve", Phase: Infra, Kind: "service"}, "flag --serve", true
			}
		}
		for i := 2; i < len(fields); i++ {
			a := fields[i]
			if a == "--bin" && i+1 < len(fields) {
				a = fields[i+1]
			} else if strings.HasPrefix(a, "-") {
				continue
			}
			if r, label, ok := matchScript(strings.TrimSuffix(a, "/")); ok {
				return r, label, true
			}
			break
		}
	}
	if base == "make" || base == "ninja" {
		return matchTargets(base, fields)
	}
	// `<tool> --version` alone prints and exits: a lookup, whatever the tool builds; so does
	// `--help` as the last argument (`gh pr create --help` creates nothing) and a lone `-h` on a
	// tool the table does not know (`df -h` is a listing). Redirections do not count (`cargo
	// --version 2>&1`).
	args := withoutRedirections(fields[1:])
	if len(args) == 1 && (args[0] == "--version" || args[0] == "-version" || args[0] == "-V" || args[0] == "version") {
		return wordRules["--version"], "--version", true
	}
	if len(args) > 0 && args[len(args)-1] == "--help" || len(args) == 1 && args[0] == "-h" && wordRules[base].Match == "" {
		return wordRules["--help"], "--help", true
	}
	// exact word rules first: four-word (xcrun devicectl device install) … down to one-word
	for n := 5; n >= 2; n-- {
		if len(fields) >= n {
			key := base + " " + strings.Join(fields[1:n], " ")
			if r, ok := wordRules[key]; ok {
				return r, key, true
			}
		}
	}
	// interpreters: "python3 - <<PY" is opaque; "python3 script.py" is judged by the script name;
	// "bash -lc '…'" is judged by the inner command.
	if interpreters[base] {
		if len(fields) > 1 && fields[1] == "-" {
			return Rule{}, "", false
		}
		args := fields[1:]
		if base == "bash" || base == "sh" || base == "zsh" {
			// bash -n script → syntax check only, nothing runs
			for _, a := range args {
				if a == "-n" {
					return Rule{Match: "bash -n", Phase: Test, Kind: "syntax-check"}, "bash -n", true
				}
			}
			// bash [-o pipefail] [-e] -c '<cmd>' / -lc '<cmd>' → judge the inner command
			for i, a := range args {
				if strings.HasPrefix(a, "-") && strings.Contains(a, "c") && !strings.HasPrefix(a, "--") && i+1 < len(args) {
					return matchHead(strings.Join(args[i+1:], " "))
				}
			}
		}
		for len(args) > 0 && strings.HasPrefix(args[0], "-") && args[0] != "-m" {
			args = args[1:]
		}
		if len(args) > 1 && args[0] == "-m" {
			// `python3 -m ruff format` before `python3 -m ruff`: the module's own subcommand first
			if len(args) > 2 && !strings.HasPrefix(args[2], "-") {
				if r, ok := wordRules[base+" -m "+args[1]+" "+args[2]]; ok {
					return r, base + " -m " + args[1] + " " + args[2], true
				}
			}
			key := base + " -m " + args[1]
			if r, ok := wordRules[key]; ok {
				return r, key, true
			}
			return Rule{}, "", false
		}
		// Interpreter scripts are judged by the script name even when it is extensionless
		// (`python3 check_tests`). Known bare subcommands (`bun test`, `deno lint`) matched the
		// exact word rules above before reaching this script-name fallback.
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			if r, label, ok := matchScript(args[0]); ok {
				return r, label, true
			}
		}
		if r, ok := wordRules[base]; ok {
			return r, base, true
		}
		return Rule{}, "", false
	}
	// `source x.sh` / `. x.sh` run the file in this shell: judged by its name like `bash x.sh`
	// (`source ./deploy.sh` is the deploy); a venv activation or an env file is shell glue.
	if (base == "source" || base == ".") && len(fields) > 1 {
		if r, label, ok := matchScript(fields[1]); ok {
			return r, label, true
		}
		return wordRules["source"], "source", true
	}
	// `eval '<literal command>'` is that command; `eval "$(…)"` runs text this call never shows
	if base == "eval" && len(fields) > 1 {
		if inner := strings.Join(fields[1:], " "); !strings.ContainsAny(inner, "$`") {
			if r, label, ok := matchHead(inner); ok {
				return r, label, true
			}
		}
		return Rule{Match: "eval", Phase: Unknown, Kind: "eval"}, "eval (opaque)", true
	}
	if r, ok := wordRules[base]; ok {
		return r, base, true
	}
	if r, label, ok := matchScript(head); ok {
		// deploy/test scripts invoked with a read-only subcommand are lookups, not work
		if len(fields) > 1 && readOnlySub[fields[1]] {
			return Rule{Match: label, Phase: Code, Kind: r.Kind + " " + fields[1]}, label + " (read-only subcommand)", true
		}
		return r, label, true
	}
	// A local dispatcher script (./dev, ./x.sh, docker/run.sh) named by its FIRST positional
	// subcommand: ./dev ci-build, ./dev ci-e2e, ./dev preflight. Only the first positional word
	// is read, only for a clearly-local script, only against an unambiguous verb set, and never
	// when that word is a read-only subcommand (status/list/…) — so a lookup stays code, not work.
	if isLocalScript(head) && len(fields) > 1 && !strings.HasPrefix(fields[1], "-") && !readOnlySub[fields[1]] {
		if r, ok := dispatcherVerb(fields[1]); ok {
			return r, "subcommand:" + fields[1], true
		}
	}
	// unknown executable: literal flags / subcommands say what it does (./run.sh --build-only).
	// A bare positional word is only trusted for a local script; on a system binary an argument
	// that merely reads like "test" or "build" is data, not a subcommand, and stays unknown.
	local := isLocalScript(head)
	for _, a := range fields[1:] {
		if !strings.HasPrefix(a, "-") && !local {
			continue
		}
		w := strings.TrimLeft(strings.ToLower(a), "-")
		if i := strings.IndexByte(w, '='); i >= 0 {
			w = w[:i]
		}
		switch w {
		case "build", "rebuild", "build-only", "compile", "bundle":
			return Rule{Match: "flag:" + w, Phase: Build, Kind: "build-flag"}, "flag --" + w, true
		case "test", "tests", "test-only", "run-tests", "check", "verify", "lint":
			return Rule{Match: "flag:" + w, Phase: Test, Kind: "test-flag"}, "flag --" + w, true
		case "deploy", "release", "publish", "ship", "install-device":
			return Rule{Match: "flag:" + w, Phase: Release, Kind: "release-flag"}, "flag --" + w, true
		case "serve", "server", "dev-server", "watch":
			return Rule{Match: "flag:" + w, Phase: Infra, Kind: "service"}, "flag --" + w, true
		}
	}
	return Rule{}, "", false
}

// isLocalScript reports whether an executable is a repo-local script (a path, or a *.sh/*.bash
// file) rather than a system binary. Positional-subcommand inference is limited to these so an
// unknown system tool with an argument that happens to read like a verb is never reclassified.
func isLocalScript(head string) bool {
	if strings.HasPrefix(head, "@") {
		return false // a scoped package name (@scope/tool) unwrapped from a runner
	}
	return strings.Contains(head, "/") || strings.HasSuffix(head, ".sh") || strings.HasSuffix(head, ".bash")
}

// dispatcherVerbs maps the first positional subcommand of a local dispatcher script to a phase.
// Only unambiguous verbs are listed; read-ish words (check/verify/status/list/integration) are
// deliberately absent — an honest unknown beats a wrong build/test.
var dispatcherVerbs = map[string]Rule{
	"build": {Phase: Build, Kind: "build-flag"}, "compile": {Phase: Build, Kind: "build-flag"}, "bundle": {Phase: Build, Kind: "build-flag"}, "rebuild": {Phase: Build, Kind: "build-flag"}, "package": {Phase: Build, Kind: "build-flag"}, "typecheck": {Phase: Test, Kind: "test-flag"},
	"test": {Phase: Test, Kind: "test-flag"}, "tests": {Phase: Test, Kind: "test-flag"}, "e2e": {Phase: Test, Kind: "test-flag"}, "smoke": {Phase: Test, Kind: "test-flag"}, "preflight": {Phase: Test, Kind: "test-flag"}, "bench": {Phase: Test, Kind: "test-flag"}, "benchmark": {Phase: Test, Kind: "test-flag"}, "lint": {Phase: Test, Kind: "test-flag"},
	"deploy": {Phase: Release, Kind: "release-flag"}, "release": {Phase: Release, Kind: "release-flag"}, "publish": {Phase: Release, Kind: "release-flag"}, "ship": {Phase: Release, Kind: "release-flag"}, "push": {Phase: Release, Kind: "release-flag"},
	"worktree": {Phase: Code, Kind: "vcs-subcommand"}, "checkout": {Phase: Code, Kind: "vcs-subcommand"}, "branch": {Phase: Code, Kind: "vcs-subcommand"},
}

// dispatcherVerb resolves a first positional subcommand, stripping a leading ci-/ci_ prefix
// (ci-build → build, ci-e2e → e2e, ci-push → push) so CI wrappers land in the right phase.
func dispatcherVerb(sub string) (Rule, bool) {
	w := strings.ToLower(sub)
	w = strings.TrimPrefix(w, "ci-")
	w = strings.TrimPrefix(w, "ci_")
	r, ok := dispatcherVerbs[w]
	return r, ok
}

// matchScript applies head/headpath rules to an executable path.
func matchScript(path string) (Rule, string, bool) {
	base := path
	if i := strings.LastIndex(path, "/"); i >= 0 {
		base = path[i+1:]
	}
	// cargo's output directories: ./target/release/app is the built binary, not a release script
	if strings.Contains(path, "target/release/") || strings.Contains(path, "target/debug/") {
		return Rule{}, "", false
	}
	for _, rr := range headRules {
		if rr.re.MatchString(base) {
			return rr.r, rr.r.Match, true
		}
	}
	for _, rr := range headPathRules {
		if rr.re.MatchString(path) {
			return rr.r, rr.r.Match, true
		}
	}
	return Rule{}, "", false
}

// shellFields splits a simple command into words, keeping quoted spans together
// (quotes removed) so `-c key='a b'` is one argument.
func shellFields(s string) []string {
	var out []string
	var cur strings.Builder
	inS, inD, has := false, false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && !inS:
			i++
			cur.WriteByte(s[i])
			has = true
		case inS:
			if c == '\'' {
				inS = false
			} else {
				cur.WriteByte(c)
			}
		case inD:
			if c == '"' {
				inD = false
			} else {
				cur.WriteByte(c)
			}
		case c == '\'':
			inS, has = true, true
		case c == '"':
			inD, has = true, true
		case c == ' ' || c == '\t' || c == '\n':
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteByte(c)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}

// sleepMs reads a sleep argument (30, 0.5, 5s, 2m, 1h) as milliseconds; 0 when it is not a number.
func sleepMs(arg string) int64 {
	unit := 1000.0
	switch {
	case strings.HasSuffix(arg, "s"):
		arg = strings.TrimSuffix(arg, "s")
	case strings.HasSuffix(arg, "m"):
		arg, unit = strings.TrimSuffix(arg, "m"), 60000
	case strings.HasSuffix(arg, "h"):
		arg, unit = strings.TrimSuffix(arg, "h"), 3600000
	}
	v, err := strconv.ParseFloat(arg, 64)
	if err != nil || v < 0 {
		return 0
	}
	return int64(v * unit)
}

func isNumberish(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c == '.' || c == 's' || c == 'm' || c == 'h') {
			return false
		}
	}
	return true
}

type heredocSource struct {
	body, interpreter, writePath string
	inline                       bool // a `-c` / `-e` program on the line rather than a heredoc
}

// maskHeredocs keeps all heredoc data opaque. Only stdin consumed as interpreter
// source is returned for classification; a script filename/-c/-m receives data.
func maskHeredocs(cmd string) (string, []heredocSource) {
	var bodies []heredocSource
	lines := strings.Split(cmd, "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		out = append(out, lines[i])
		type declaration struct {
			tag, interpreter, writePath string
			tabs                        bool
		}
		var declarations []declaration
		tokens, ok := shellTokens(lines[i])
		if !ok {
			// Unsupported shell expressions remain opaque rather than inferred executable.
			for _, m := range reHeredoc.FindAllStringSubmatch(lines[i], -1) {
				declarations = append(declarations, declaration{tag: m[1], tabs: strings.HasPrefix(m[0], "<<-")})
			}
		} else {
			for k, token := range tokens {
				op := strings.TrimLeft(token.text, "0123456789")
				if !token.op || (op != "<<" && op != "<<-") || k+1 >= len(tokens) || tokens[k+1].op {
					continue
				}
				words := shellFields(tokens[k+1].text)
				if len(words) != 1 {
					continue
				}
				lo, hi := k, k+2
				for lo > 0 && !shellControl(tokens[lo-1]) {
					lo--
				}
				for hi < len(tokens) && !shellControl(tokens[hi]) {
					hi++
				}
				stdin := token.text == op || token.text == "0"+op
				for _, later := range tokens[k+2 : hi] {
					redir := strings.TrimLeft(later.text, "0123456789")
					if later.op && strings.HasPrefix(redir, "<") && (later.text == redir || later.text == "0"+redir) {
						stdin = false // a later input redirection replaces this heredoc
					}
				}
				interpreter, writePath := "", ""
				if stdin {
					interpreter = heredocInterpreter(tokens[lo:hi])
					if interpreter == "" {
						interpreter = pipedHeredocInterpreter(tokens, lo, hi)
					}
					header := stripEnvPrefix(joinShellTokens(tokens[lo:hi]))
					if m := reCatHead.FindStringSubmatch(header); m != nil {
						writePath = m[1]
					}
				}
				declarations = append(declarations, declaration{words[0], interpreter, writePath, op == "<<-"})
			}
		}
		for _, decl := range declarations {
			var body []string
			j := i + 1
			for ; j < len(lines); j++ {
				line := lines[j]
				if decl.tabs {
					line = strings.TrimLeft(line, "\t")
				}
				if line == decl.tag {
					break
				}
				body = append(body, line)
			}
			bodies = append(bodies, heredocSource{body: strings.Join(body, "\n"), interpreter: decl.interpreter, writePath: decl.writePath})
			out = append(out, "<<heredoc>>")
			i = j
		}
	}
	return strings.Join(out, "\n"), bodies
}

// Head names the command a rollout line ran, for grouping unmatched commands: the executable of
// the first top-level segment that runs one — env assignments, wrapper words (wrapperWords: env, sudo,
// nohup, setsid, timeout, …) and package runners stripped, the same way the rules see
// it (unwrapHead), a variable assigned by the command itself resolved (resolveVars). Segments
// that run nothing (an assignment, a comment, a `for …` header, a function definition) are
// skipped, so a loop is named by its body. A heredoc script is named by its interpreter.
func Head(cmd string) string {
	masked, _ := maskHeredocs(cmd)
	for _, seg := range resolveVars(segments(masked)) {
		if _, head, _, ok := unwrapHead(seg); ok {
			return head
		}
	}
	return ""
}

var reAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// shortTitle makes a compact one-line label from a command. Heredoc scripts are labelled
// by their interpreter and first meaningful body line.
func shortTitle(cmd string) string {
	s := strings.TrimSpace(cmd)
	if m := reHeredoc.FindStringIndex(s); m != nil {
		head := strings.TrimSpace(stripEnvPrefix(strings.TrimSpace(s[:m[0]])))
		lines := strings.Split(s, "\n")
		body := ""
		n := 0
		for _, ln := range lines[1:] {
			t := strings.TrimSpace(ln)
			if t == "" || strings.HasPrefix(t, "#") {
				continue
			}
			n++
			if body == "" && !strings.HasPrefix(t, "import ") && !strings.HasPrefix(t, "from ") && !strings.HasPrefix(t, "const ") && !strings.HasPrefix(t, "require(") {
				body = t
			}
		}
		if n > 1 {
			n-- // the terminator
		}
		s = head + " script (" + strconv.Itoa(n) + " lines): " + body
	} else if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i] + " …"
	}
	s = reWS.ReplaceAllString(s, " ")
	if len(s) > 110 {
		s = s[:107] + "…"
	}
	return s
}
