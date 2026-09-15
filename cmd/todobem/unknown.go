package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/server"
	"github.com/extractumio/todobem/internal/settings"
)

// `todobem unknown` is the agent-facing view of what the classifier could not name: unmatched
// commands (phase unknown) grouped by head word across recent sessions, and the no-telemetry
// intervals (turns that never closed). It answers "what should a user rule cover next" and,
// with -explain, "what does the current rule set say about this command" — the loop the
// resolve-unknown skill runs. Reads sessions the way the UI does (cache when current).

type unknownHead struct {
	Head     string   `json:"head"`
	Sources  []string `json:"sources"` // session sources the head appears in ("codex", "claude")
	Ops      int      `json:"ops"`
	Ms       int64    `json:"ms"`
	Sessions []string `json:"sessions"`
	Sample   string   `json:"sample"` // the longest op's command, clipped
	sessions map[string]bool
	sources  map[string]bool
}

type unknownOp struct {
	Session string `json:"session"`
	Source  string `json:"source"`
	Lane    string `json:"lane"`
	Op      string `json:"op"`
	Head    string `json:"head"`
	Ms      int64  `json:"ms"`
	Start   int64  `json:"start"`
	Status  string `json:"status"`
	Exit    *int   `json:"exit,omitempty"`
	Command string `json:"command"` // clipped to 400 chars
}

type telemetryGap struct {
	Session string `json:"session"`
	Lane    string `json:"lane"`
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Ms      int64  `json:"ms"`
	Reason  string `json:"reason"` // open_turn | orphaned_turn
}

type unknownReport struct {
	Sessions    int            `json:"sessions"`
	Scanned     []string       `json:"scanned"`
	UnknownOps  int            `json:"unknown_ops"`
	UnknownMs   int64          `json:"unknown_ms"`
	ToolMs      int64          `json:"tool_ms"` // all tool phases, all lanes, for the share
	Heads       []unknownHead  `json:"heads"`
	Ops         []unknownOp    `json:"ops,omitempty"`
	NoTelemetry []telemetryGap `json:"no_telemetry"`
	NoTelemMs   int64          `json:"no_telemetry_ms"`
	Errors      []string       `json:"errors,omitempty"`
}

func runUnknown(args []string) int {
	fs := flag.NewFlagSet("todobem unknown", flag.ContinueOnError)
	home, _ := os.UserHomeDir()
	codexHome := fs.String("codex", "", codexFlagHelp)
	claudeHome := fs.String("claude", "", claudeFlagHelp)
	settingsPath := fs.String("settings", settings.DefaultPath(), settingsFlagHelp)
	rules := fs.String("rules", "", "user rules overlay (JSON); $TODOBEM_RULES and ~/.todobem/rules.json load anyway — the merged set is what classifies")
	cacheDir := fs.String("cache", "", "parsed-session cache (default ~/.todobem/cache or $TODOBEM_CACHE; \"off\" re-parses everything)")
	last := fs.Int("last", 20, "the newest N root sessions")
	since := fs.String("since", "", "only sessions updated within this period (e.g. 7d, 48h); overrides -last")
	session := fs.String("session", "", "one session: its root thread id or a unique prefix")
	ops := fs.Bool("ops", false, "list every unknown operation, not only the heads")
	asJSON := fs.Bool("json", false, "machine-readable output")
	explain := fs.String("explain", "", "classify this one command with the current rules and exit")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: todobem unknown [flags]        unmatched commands and telemetry gaps across sessions\n       todobem unknown -explain CMD   what the current rules say about one command\n\nflags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	loaded, err := classify.LoadUserConfigDefaults(*rules)
	if err != nil {
		fmt.Fprintln(os.Stderr, "loading user rules:", err)
		return 2
	}
	if *explain != "" {
		return explainCommand(*explain, loaded, *asJSON)
	}
	var period time.Duration
	if *since != "" {
		d, err := parsePeriod(*since)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		period = d
	}
	homes, err := resolveHomes(*codexHome, *claudeHome, *settingsPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "session folders:", err)
		return 2
	}
	srv := server.NewWithCache(homes.Homes, nil, resolveCacheDir(*cacheDir, home))
	srv.Scan()
	roots := srv.Roots()
	var ids []string
	for _, fm := range roots {
		switch {
		case *session != "":
			if fm.ID == *session || strings.HasPrefix(fm.ID, *session) {
				ids = append(ids, fm.ID)
			}
		case period > 0:
			if time.Since(fm.ModTime) <= period {
				ids = append(ids, fm.ID)
			}
		default:
			if len(ids) < *last {
				ids = append(ids, fm.ID)
			}
		}
	}
	if *session != "" && len(ids) != 1 {
		fmt.Fprintf(os.Stderr, "session %q: %d matches\n", *session, len(ids))
		return 2
	}
	rep := collectUnknown(srv, ids, *ops)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", " ")
		_ = enc.Encode(rep)
		return 0
	}
	printUnknown(rep, loaded, *ops)
	return 0
}

// toolPhase: the phases a command can be classified into (the denominator of the unknown share).
var toolPhase = map[model.Phase]bool{classify.Code: true, classify.Build: true, classify.Test: true, classify.Release: true, classify.Infra: true, classify.WaitWorker: true, classify.Unknown: true}

func collectUnknown(srv *server.Server, ids []string, withOps bool) unknownReport {
	rep := unknownReport{Scanned: ids}
	heads := map[string]*unknownHead{}
	longest := map[string]int64{}
	for _, id := range ids {
		err := srv.Model(id, func(m *model.Session) {
			rep.Sessions++
			for _, l := range m.Lanes {
				for _, o := range l.Ops {
					if o.Background {
						continue
					}
					if toolPhase[o.Phase] {
						rep.ToolMs += o.End - o.Start
					}
					if o.Phase != classify.Unknown {
						continue
					}
					cmd := o.Detail
					if cmd == "" {
						cmd = o.Title
					}
					head := classify.Head(cmd)
					if head == "" {
						head = o.Title
					}
					ms := o.End - o.Start
					rep.UnknownOps++
					rep.UnknownMs += ms
					h := heads[head]
					if h == nil {
						h = &unknownHead{Head: head, sessions: map[string]bool{}, sources: map[string]bool{}}
						heads[head] = h
					}
					h.Ops++
					h.Ms += ms
					h.sessions[id] = true
					h.sources[m.Source] = true
					if ms >= longest[head] {
						longest[head] = ms
						h.Sample = clipLine(cmd, 200)
					}
					if withOps {
						rep.Ops = append(rep.Ops, unknownOp{Session: id, Source: m.Source, Lane: l.Path, Op: o.ID, Head: head, Ms: ms, Start: o.Start, Status: o.Status, Exit: o.Exit, Command: clipLine(cmd, 400)})
					}
				}
				for _, sg := range l.Segments {
					if sg.Phase != classify.NoTelemetry || sg.End-sg.Start < 1000 {
						continue // sub-second gaps are turn-boundary jitter, not missing telemetry
					}
					reason := "orphaned_turn"
					for _, t := range l.Turns {
						if t.Status == "open" && sg.End >= t.End {
							reason = "open_turn"
						}
					}
					rep.NoTelemetry = append(rep.NoTelemetry, telemetryGap{Session: id, Lane: l.Path, Start: sg.Start, End: sg.End, Ms: sg.End - sg.Start, Reason: reason})
					rep.NoTelemMs += sg.End - sg.Start
				}
			}
		})
		if err != nil {
			rep.Errors = append(rep.Errors, id+": "+err.Error())
		}
	}
	for _, h := range heads {
		for s := range h.sessions {
			h.Sessions = append(h.Sessions, s)
		}
		sort.Strings(h.Sessions)
		for s := range h.sources {
			h.Sources = append(h.Sources, s)
		}
		sort.Strings(h.Sources)
		rep.Heads = append(rep.Heads, *h)
	}
	sort.Slice(rep.Heads, func(i, j int) bool {
		if rep.Heads[i].Ms != rep.Heads[j].Ms {
			return rep.Heads[i].Ms > rep.Heads[j].Ms
		}
		return rep.Heads[i].Ops > rep.Heads[j].Ops
	})
	sort.Slice(rep.Ops, func(i, j int) bool { return rep.Ops[i].Ms > rep.Ops[j].Ms })
	sort.Slice(rep.NoTelemetry, func(i, j int) bool { return rep.NoTelemetry[i].Ms > rep.NoTelemetry[j].Ms })
	return rep
}

func printUnknown(rep unknownReport, loaded []string, withOps bool) {
	if len(loaded) > 0 {
		fmt.Printf("user rules: %s\n", strings.Join(loaded, ", "))
	}
	share := 0.0
	if rep.ToolMs > 0 {
		share = float64(rep.UnknownMs) * 100 / float64(rep.ToolMs)
	}
	fmt.Printf("sessions: %d · unknown ops: %d · %s of %s tool time (%.1f%%) · %d heads\n", rep.Sessions, rep.UnknownOps, fmtDur(rep.UnknownMs), fmtDur(rep.ToolMs), share, len(rep.Heads))
	if len(rep.Heads) > 0 {
		fmt.Printf("\n%-34s %-7s %5s %8s %5s  %s\n", "HEAD", "SOURCE", "OPS", "TIME", "SESS", "SAMPLE (longest op)")
		for _, h := range rep.Heads {
			fmt.Printf("%-34s %-7s %5d %8s %5d  %s\n", clipLine(h.Head, 34), clipLine(strings.Join(h.Sources, ","), 7), h.Ops, fmtDur(h.Ms), len(h.Sessions), h.Sample)
		}
	}
	if withOps && len(rep.Ops) > 0 {
		fmt.Printf("\n%-8s %8s %-8s %-24s %s\n", "SESSION", "TIME", "STATUS", "LANE", "COMMAND")
		for _, o := range rep.Ops {
			st := o.Status
			if o.Exit != nil {
				st += fmt.Sprintf("/%d", *o.Exit)
			}
			fmt.Printf("%-8s %8s %-8s %-24s %s\n", o.Session[:8], fmtDur(o.Ms), st, clipLine(o.Lane, 24), o.Command)
		}
	}
	fmt.Printf("\nno telemetry: %d intervals · %s — turns without a close event (a crash, a killed CLI, a resumed thread); no rule covers these, they are reported as gaps\n", len(rep.NoTelemetry), fmtDur(rep.NoTelemMs))
	for i, g := range rep.NoTelemetry {
		if i >= 15 {
			fmt.Printf("  … %d more\n", len(rep.NoTelemetry)-i)
			break
		}
		fmt.Printf("  %s  %s → %s  %8s  %-20s %s\n", g.Session[:8], time.UnixMilli(g.Start).Format("02 Jan 15:04"), time.UnixMilli(g.End).Format("15:04"), fmtDur(g.Ms), clipLine(g.Lane, 20), g.Reason)
	}
	for _, e := range rep.Errors {
		fmt.Fprintln(os.Stderr, "error:", e)
	}
}

func explainCommand(cmd string, loaded []string, asJSON bool) int {
	r := classify.Command(cmd, "")
	if asJSON {
		out := map[string]any{"command": cmd, "phase": r.Phase, "kind": r.Kind, "rule": r.Rule, "lifecycle": r.Lifecycle, "identity": r.Identity != "", "remote": r.Remote, "queued": r.Queued, "head": classify.Head(cmd), "word_key": filepath.Base(classify.Head(cmd)), "user_rules": loaded}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", " ")
		_ = enc.Encode(out)
		return 0
	}
	head := classify.Head(cmd)
	fmt.Printf("head:      %s\nword key:  %s   (a word rule matches the head's basename, optionally + sub-words)\nphase:     %s\nkind:      %s\nrule:      %s\nlifecycle: %s\n", head, filepath.Base(head), r.Phase, r.Kind, orDash(r.Rule), orDash(string(r.Lifecycle)))
	if r.Identity != "" {
		fmt.Println("retry id:  yes (identical repeats form a retry group)")
	}
	if len(loaded) > 0 {
		fmt.Printf("user rules: %s\n", strings.Join(loaded, ", "))
	}
	if r.Phase == classify.Unknown {
		return 1
	}
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func clipLine(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " ⏎ "))
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

func fmtDur(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	switch {
	case d >= time.Hour:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// parsePeriod reads "7d", "48h", "90m".
func parsePeriod(s string) (time.Duration, error) {
	if strings.HasSuffix(s, "d") {
		var n int
		if _, err := fmt.Sscanf(s, "%dd", &n); err != nil || n <= 0 {
			return 0, fmt.Errorf("period %q: want e.g. 7d or 48h", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("period %q: want e.g. 7d or 48h", s)
	}
	return d, nil
}
