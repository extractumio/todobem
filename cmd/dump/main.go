// dump parses one session and prints a summary (development aid).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/extractumio/todobem/internal/classify"
	"github.com/extractumio/todobem/internal/claude"
	"github.com/extractumio/todobem/internal/codex"
	"github.com/extractumio/todobem/internal/insights"
	"github.com/extractumio/todobem/internal/model"
	"github.com/extractumio/todobem/internal/settings"
	"github.com/extractumio/todobem/internal/source"
)

func fmtd(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	return d.Round(time.Second).String()
}

func firstField(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func main() {
	insightsOnly := flag.Bool("insights", false, "print the Insights facts summary and per-rule findings for the session (TSV lines prefixed INSIGHT); nothing else")
	opsOnly := flag.Bool("ops", false, "print every operation (all lanes) as TSV: phase, kind, rule, lifecycle, lifecycle rule, duration, status, lane, command; nothing else")
	rules := flag.String("rules", "", "path to a user rules overlay (JSON); also loads $TODOBEM_RULES and ~/.todobem/rules.json")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: dump [-ops] <root-thread-id> [export.json]")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}
	if _, err := classify.LoadUserConfigDefaults(*rules); err != nil {
		fmt.Fprintln(os.Stderr, "loading user rules:", err)
		os.Exit(2)
	}
	id := flag.Arg(0)
	// the same folders the server reads: the settings file when it exists, else the defaults
	cfg, _, err := settings.Load(settings.DefaultPath())
	if err != nil {
		fmt.Fprintln(os.Stderr, "settings:", err)
		os.Exit(2)
	}
	_, homes, err := settings.Resolve(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "settings:", err)
		os.Exit(2)
	}
	ix := source.NewMulti(codex.NewIndex(homes.Codex...), claude.NewIndex(homes.Claude...))
	t0 := time.Now()
	ix.Scan()
	scanTime := time.Since(t0)
	s, err := ix.Open(id)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	t1 := time.Now()
	if _, err := s.Refresh(); err != nil {
		panic(err)
	}
	m := s.Model
	if *opsOnly {
		printOps(m)
		return
	}
	if *insightsOnly {
		printInsights(m)
		return
	}
	fmt.Println("scan", scanTime)
	fmt.Println("parse", time.Since(t1))
	rd, dec := s.IOStats()
	fmt.Printf("bytes read %d MB, JSON-decoded %d MB (%.0f%%)\n", rd/1e6, dec/1e6, float64(dec)*100/float64(rd))
	for _, l := range m.Lanes {
		var part int64
		for _, sg := range l.Segments {
			part += sg.End - sg.Start
		}
		var raw int64
		for _, v := range l.RawByPhase {
			raw += v
		}
		if part != l.Ended-l.Started {
			fmt.Printf("  !! partition mismatch %s: %d vs %d\n", l.Path, part, l.Ended-l.Started)
		}
		var lcSum int64
		for _, v := range l.ByLifecycle {
			lcSum += v
		}
		if lcSum != part {
			fmt.Printf("  !! lifecycle partition mismatch %s: %d vs %d\n", l.Path, lcSum, part)
		}
		var shared int64
		for k, v := range l.ByPhaseShared {
			shared += v
			if v > l.ByPhase[k] {
				fmt.Printf("  !! shared estimate %s: %s %d > by_phase %d\n", l.Path, k, v, l.ByPhase[k])
			}
		}
		for _, o := range l.Ops {
			if len(o.Shares) == 0 {
				continue
			}
			var sum int64
			for _, sh := range o.Shares {
				sum += sh.Ms
			}
			end := o.End
			if o.Open && m.Now > end {
				end = m.Now
			}
			if wall := max(end-o.Start, 1); sum != wall {
				fmt.Printf("  !! shares of %s sum to %d, wall clock %d\n", o.ID, sum, wall)
			}
		}
		fmt.Printf("  lane %-40s partition-elapsed=%d raw-ops=%s in-turn=%s shared-estimate=%s\n", l.Path, part-(l.Ended-l.Started), fmtd(raw), fmtd(l.InTurnMs), fmtd(shared))
	}
	// Token reconstruction check: the sum of the turns' counted calls must equal the lane's
	// (every token_count sits inside a turn in the corpus; a difference is a record outside one).
	var contexts, rereads []int64
	for _, l := range m.Lanes {
		var turns model.TokenUsage
		responses := 0
		for _, t := range l.Turns {
			turns.Add(t.Tokens)
			responses += t.Responses
		}
		var lane int64
		if l.Tokens != nil {
			lane = l.Tokens.Total
		}
		flag := ""
		if turns.Total != lane {
			flag = "  !! turn tokens != lane tokens"
		}
		fmt.Printf("  tokens %-40s lane=%d turns=%d responses=%d%s\n", l.Path, lane, turns.Total, responses, flag)
		for _, o := range l.Ops {
			if o.Phase == classify.Compaction && o.Context > 0 {
				contexts = append(contexts, o.Context)
				if o.Tokens != nil {
					rereads = append(rereads, o.Tokens.Input-o.Tokens.Cached)
				}
			}
		}
	}
	if len(contexts) > 0 {
		sort.Slice(contexts, func(i, j int) bool { return contexts[i] < contexts[j] })
		sort.Slice(rereads, func(i, j int) bool { return rereads[i] < rereads[j] })
		var reread int64
		if len(rereads) > 0 {
			reread = rereads[len(rereads)/2]
		}
		fmt.Printf("  compactions with context: %d, context median=%d min=%d max=%d, uncached re-read median=%d\n", len(contexts), contexts[len(contexts)/2], contexts[0], contexts[len(contexts)-1], reread)
	}
	heads := map[string]int{}
	for _, l := range m.Lanes {
		for _, o := range l.Ops {
			if o.Phase == "unknown" {
				// the command word as `todobem unknown` names it: env assignments and wrappers stripped;
				// an unmapped tool (kind tool:<name>) has JSON in Detail, its title is the name
				h := o.Title
				if !strings.HasPrefix(o.Kind, "tool:") {
					h = classify.Head(o.Detail)
				}
				if i := strings.IndexAny(h, " \n"); i > 0 {
					h = h[:i]
				}
				heads[h]++
			}
		}
	}
	type hc struct {
		h string
		n int
	}
	var hcs []hc
	for h, n := range heads {
		hcs = append(hcs, hc{h, n})
	}
	sort.Slice(hcs, func(i, j int) bool { return hcs[i].n > hcs[j].n })
	fmt.Print("  unknown heads:")
	for i, x := range hcs {
		if i >= 20 {
			break
		}
		fmt.Printf(" %s×%d", x.h, x.n)
	}
	fmt.Println()
	fmt.Printf("title=%q lanes=%d ops=%d groups=%d elapsed=%s live=%v bytes=%dMB\n", m.Title, len(m.Lanes), m.Totals.Ops, len(m.Groups), fmtd(m.Totals.ElapsedMs), m.Live, m.Bytes/1e6)
	var sum int64
	type kv struct {
		k model.Phase
		v int64
	}
	var kvs []kv
	for k, v := range m.Totals.ByPhase {
		kvs = append(kvs, kv{k, v})
		sum += v
	}
	sort.Slice(kvs, func(i, j int) bool { return kvs[i].v > kvs[j].v })
	for _, x := range kvs {
		fmt.Printf("  %-14s %10s %5.1f%%\n", x.k, fmtd(x.v), float64(x.v)*100/float64(m.Totals.ElapsedMs))
	}
	fmt.Println("  partition sum", fmtd(sum), "elapsed", fmtd(m.Totals.ElapsedMs))
	fmt.Println("  by_lifecycle (root, SDLC order):")
	var lcSum int64
	for _, lc := range classify.WorkLifecycles {
		if v := m.Totals.ByLifecycle[lc]; v > 0 {
			fmt.Printf("    %-14s %10s %5.1f%%\n", lc, fmtd(v), float64(v)*100/float64(m.Totals.ElapsedMs))
		}
	}
	for lc, v := range m.Totals.ByLifecycle {
		lcSum += v
		if !classify.IsWorkLifecycle(lc) {
			fmt.Printf("    %-14s %10s %5.1f%%  (pass-through)\n", lc, fmtd(v), float64(v)*100/float64(m.Totals.ElapsedMs))
		}
	}
	fmt.Println("  lifecycle partition sum", fmtd(lcSum))
	for _, l := range m.Lanes {
		for _, t := range l.Turns {
			if t.Lifecycle != "" {
				fmt.Printf("  TURN-LC %s %-10s %-8s skill=%q mode=%q rule=%q\n", time.UnixMilli(t.Start).UTC().Format("01-02 15:04"), l.Path, t.Lifecycle, t.Skill, t.Mode, t.LifecycleRule)
			}
		}
	}
	fmt.Println("  by_kind:", m.Totals.ByKind)
	fmt.Println("  parallel:", m.Parallel.Agents, "agents", fmtd(m.Parallel.AgentMs), "agent-time", fmtd(m.Parallel.WallMs), "wall")
	fmt.Println("  users:", m.Totals.UserMessages, "questions:", m.Totals.Questions, "compactions:", m.Totals.Compactions, "failed:", m.Totals.Failed, "query misses:", m.Totals.QueryMisses, "tokens:", m.Totals.Tokens.Total)
	fmt.Println("  reviews:", m.Totals.Reviews, "review time (root, by_lifecycle):", fmtd(m.Totals.ByLifecycle[classify.LcReview]))
	for _, l := range m.Lanes {
		for _, mk := range l.Markers {
			if mk.Kind == "skill" {
				fmt.Printf("  SKILL %s %-10s %s\n", time.UnixMilli(mk.T).UTC().Format("01-02 15:04"), l.Path, firstField(mk.Text))
			}
		}
	}
	for _, l := range m.Lanes {
		fmt.Printf("LANE %-45s depth=%d turns=%d ops=%d segs=%d markers=%d live=%v span=%s\n", l.Path, l.Depth, len(l.Turns), len(l.Ops), len(l.Segments), len(l.Markers), l.Live, fmtd(l.Ended-l.Started))
	}
	root := m.Lanes[0]
	// stage runs: consecutive segments of one lifecycle stage, as the timeline's stage band draws them
	fmt.Println("ROOT STAGE RUNS (first 40):")
	type run struct {
		lc          model.Lifecycle
		start, end  int64
		model, tool int64
	}
	var runs []run
	for _, sg := range root.Segments {
		if n := len(runs); n > 0 && runs[n-1].lc == sg.Lifecycle && runs[n-1].end == sg.Start {
			runs[n-1].end = sg.End
		} else {
			runs = append(runs, run{lc: sg.Lifecycle, start: sg.Start, end: sg.End})
		}
		if sg.Phase == model.Phase("llm") {
			runs[len(runs)-1].model += sg.End - sg.Start
		} else {
			runs[len(runs)-1].tool += sg.End - sg.Start
		}
	}
	for i, r := range runs {
		if i >= 40 {
			break
		}
		fmt.Printf("  %s  %-12s %8s  model=%s tools=%s\n", time.UnixMilli(r.start).UTC().Format("01-02 15:04:05"), r.lc, fmtd(r.end-r.start), fmtd(r.model), fmtd(r.tool))
	}
	fmt.Println("GROUPS:")
	for _, g := range m.Groups {
		fmt.Printf("  %s %-8s attempts=%d failed=%d %s  %s\n", g.ID, g.Phase, g.Attempts, g.Failed, fmtd(g.End-g.Start), g.Title)
	}
	fmt.Println("LONGEST OPS:")
	ops := append([]*model.Operation{}, root.Ops...)
	sort.Slice(ops, func(i, j int) bool { return ops[i].End-ops[i].Start > ops[j].End-ops[j].Start })
	for i, o := range ops {
		if i >= 25 {
			break
		}
		fmt.Printf("  %8s %-11s %-18s %-9s %s\n", fmtd(o.End-o.Start), o.Phase, o.Kind, o.Status, o.Title)
	}
	fmt.Println("UNKNOWN OPS (sample):")
	n := 0
	for _, o := range root.Ops {
		if o.Phase == "unknown" && n < 15 {
			n++
			fmt.Printf("  %8s %s\n", fmtd(o.End-o.Start), o.Title)
		}
	}
	fmt.Println("USER MESSAGES:")
	for _, mk := range root.Markers {
		if mk.Kind == "user_message" || mk.Kind == "question" {
			t := mk.Text
			if len(t) > 90 {
				t = t[:90]
			}
			fmt.Printf("  %s %-12s %q\n", time.UnixMilli(mk.T).UTC().Format("01-02 15:04"), mk.Kind, t)
		}
	}
	if flag.NArg() > 1 {
		b, _ := json.Marshal(m)
		fmt.Println("json bytes", len(b))
		os.WriteFile(flag.Arg(1), b, 0644)
	}
}

// printOps lists every operation with the command that produced it, one per line, so a
// classification pass over real sessions can be audited with grep/sort (output stays local).
func printOps(m *model.Session) {
	for _, l := range m.Lanes {
		for _, o := range l.Ops {
			if o.Phase == model.Phase("llm") {
				continue
			}
			detail := strings.ReplaceAll(strings.ReplaceAll(o.Detail, "\t", " "), "\n", "⏎")
			if detail == "" {
				detail = o.Title
			}
			fmt.Printf("%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s\t%s\n", o.Phase, o.Kind, o.Rule, o.Lifecycle, o.LifecycleRule, (o.End-o.Start)/1000, o.Status, l.Path, detail)
			for _, sh := range o.Shares { // a compound command's shares, one line each, marked
				fmt.Printf("  share\t%s\t%s\t\t%s\t\t%d\t%s\t%s\t%s\n", sh.Phase, sh.Kind, sh.Lifecycle, sh.Ms/1000, map[bool]string{true: "literal", false: "equal"}[sh.Literal], l.Path, sh.Segment)
			}
		}
	}
}

// printInsights runs the detector catalogue on one session and prints, per rule, the exposure
// and every finding as a TSV line (INSIGHT, rule, key, time_ms, uncached input, output, lane,
// start, note) so a run over many sessions can be aggregated with sort/uniq (output stays local).
func printInsights(m *model.Session) {
	f := insights.Extract(m)
	fmt.Printf("FACTS\t%s\t%s\tcli=%s\telapsed=%s\tin_turn=%s\tagents=%d\tturns=%d\tgaps=%d\twaits=%d\tgroups=%d\tcompactions=%d\tunknown_heads=%d\tcells=%d\n",
		f.ID, f.CWD, f.CLI, fmtd(f.Root.ElapsedMs), fmtd(f.Root.InTurnMs), len(f.Agents), len(f.Turns), len(f.Gaps), len(f.Waits), len(f.Groups), len(f.Compactions), len(f.Unknown), len(f.Cells))
	d := f.Delivery
	fmt.Printf("DELIVERY\tchanges=%d\tlast_change=%s\tverified=%v\tby=%s\ttests=%d\tlast_verdict_failed=%v\treviewed_at=%s\tchanges_after_review=%d\tblind_after_last_change=%s\tedit_turns=%d\tunverified=%d\tunknown_in_windows=%s\n",
		d.Changes, stampOrNone(d.LastChangeAt), d.Verified, d.VerifiedBy, d.Tests, d.LastVerdictFailed, stampOrNone(d.ReviewedAt), d.ChangesAfterReview, fmtd(d.BlindAfterLastChangeMs), d.EditTurns, d.EditTurnsUnverified, fmtd(d.UnknownInWindowMs))
	results := insights.RunAll(&f)
	for _, d := range insights.Catalogue {
		r := results[d.ID]
		sum := insights.Summarize(r)
		class := d.Class
		if class == "" {
			class = insights.ClassExposure
		}
		// the stats are the numbers the page's headline reads (sorted, so runs diff)
		keys := make([]string, 0, len(r.Stats))
		for k := range r.Stats {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		stats := ""
		for _, k := range keys {
			stats += fmt.Sprintf("\t%s=%d", k, r.Stats[k])
		}
		fmt.Printf("RULE\t%s\t%s\tclass=%s\tmeasurable=%v\tnot_applicable=%v\tfindings=%d\tno_data=%d\ttime=%s\tuncached_in=%d\toutput=%d\t%s%s\n", d.ID, d.Title, class, r.Measurable, r.NotApplicable, sum.Count, r.NoData, fmtd(sum.TimeMs), sum.Tokens.Input-sum.Tokens.Cached, sum.Tokens.Output, r.Reason, stats)
		for _, x := range r.Findings {
			var unc, out int64
			if x.Tokens != nil {
				unc, out = x.Tokens.Input-x.Tokens.Cached, x.Tokens.Output
			}
			fmt.Printf("INSIGHT\t%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\n", x.Rule, x.Key, x.TimeMs, unc, out, x.Lane, time.UnixMilli(x.A).UTC().Format("01-02 15:04"), x.Note)
		}
	}
}

// stampOrNone formats a millisecond timestamp for the DELIVERY line, or "-" for zero.
func stampOrNone(ms int64) string {
	if ms == 0 {
		return "-"
	}
	return time.UnixMilli(ms).UTC().Format("01-02 15:04:05")
}
