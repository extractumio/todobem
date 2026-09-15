package insights

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/extractumio/todobem/internal/model"
)

// Period selects the sessions of a report: closed sessions whose last activity lies inside
// [From, To] (docs/INSIGHTS-SPEC.md §6 "Period"). Relative kinds are resolved at build time.
type Period struct {
	Kind    string `json:"kind"` // 1d | 7d | 30d | 90d | all | custom | session
	From    int64  `json:"from"` // ms, inclusive
	To      int64  `json:"to"`   // ms, inclusive
	Session string `json:"session,omitempty"`
}

const day = 24 * 3600e3

// Resolve fills From/To for the relative kinds at time now; custom and session keep theirs.
func (p Period) Resolve(now int64) Period {
	switch p.Kind {
	case "1d":
		p.From, p.To = now-day, now
	case "7d":
		p.From, p.To = now-7*day, now
	case "90d":
		p.From, p.To = now-90*day, now
	case "all":
		p.From, p.To = 0, now
	case "custom", "session":
	default:
		p.Kind = "30d"
		p.From, p.To = now-30*day, now
	}
	return p
}

// Params identifies one report.
type Params struct {
	CWD         string   `json:"cwd"`
	Sources     []string `json:"sources,omitempty"` // session sources in scope ("codex", "claude"); empty = all
	Period      Period   `json:"period"`
	IncludeLive bool     `json:"include_live,omitempty"`
}

// HasSource reports whether sessions of this source are in the report's scope.
func (p Params) HasSource(name string) bool {
	if len(p.Sources) == 0 {
		return true
	}
	for _, s := range p.Sources {
		if s == name {
			return true
		}
	}
	return false
}

// Source is one session a report was built from; the fingerprint lets the server tell when the
// report no longer matches the disk.
type Source struct {
	ID          string `json:"id"`
	Fingerprint string `json:"fp"`
	Title       string `json:"title"`
	Ended       int64  `json:"ended"`
	Live        bool   `json:"live,omitempty"`
}

// Input is one session offered to Build: its facts and the fingerprint they were made from.
type Input struct {
	Facts       Facts
	Fingerprint string
}

type Report struct {
	GeneratedAt int64       `json:"generated_at"`
	Params      Params      `json:"params"`
	Scope       Scope       `json:"scope"`
	TopTime     []string    `json:"top_time"`   // rule ids, best first, on the time axis
	TopTokens   []string    `json:"top_tokens"` // rule ids, best first, on the tokens axis
	Groups      []Group     `json:"groups"`     // ordered by time exposure; not_measured last
	NoData      []NoDataRow `json:"no_data"`    // per rule: sessions that could not carry the signal
	Fallback    string      `json:"fallback,omitempty"`
	Sources     []Source    `json:"sources"`
	SourcesHash string      `json:"sources_hash"`
}

// NoDataRow says, for one rule, how many sessions could not be measured and why, plus the
// occurrences inside measurable sessions that had no usable record.
type NoDataRow struct {
	Rule     string `json:"rule"`
	Title    string `json:"title"`
	Sessions int    `json:"sessions"`
	Reason   string `json:"reason,omitempty"`
	Items    int    `json:"items,omitempty"`
}

type Scope struct {
	Sessions     int              `json:"sessions"` // in the period and used
	LiveExcluded int              `json:"live_excluded"`
	Pending      []Source         `json:"pending"` // in the period, no facts yet (set by the server)
	RootElapsed  int64            `json:"root_elapsed_ms"`
	RootInTurn   int64            `json:"root_in_turn_ms"`
	WaitUser     int64            `json:"wait_user_ms"`
	Tokens       model.TokenUsage `json:"tokens"`
	CLIs         map[string]int   `json:"clis"`
}

type Group struct {
	ID          string           `json:"id"`
	Cards       []Card           `json:"cards"`
	TimeMs      int64            `json:"time_ms"`
	Tokens      model.TokenUsage `json:"tokens"`
	OrderTime   int              `json:"order_time"`
	OrderTokens int              `json:"order_tokens"`
}

type Card struct {
	Rule         string           `json:"rule"`
	Group        string           `json:"group"`
	Title        string           `json:"title"`
	Info         bool             `json:"info,omitempty"` // a measurement: never ranked, not in totals
	Exposure     Exposure         `json:"exposure"`
	Share        *Share           `json:"share,omitempty"`
	Distribution []Row            `json:"distribution,omitempty"`
	Sessions     int              `json:"sessions"` // sessions with at least one finding
	Of           int              `json:"of"`       // sessions where the signal could be measured
	NoData       int              `json:"no_data"`  // sessions where it could not
	Reason       string           `json:"reason,omitempty"`
	NoDataItems  int              `json:"no_data_items,omitempty"` // occurrences without a usable record
	Conventions  []string         `json:"conventions,omitempty"`
	Evidence     []Evidence       `json:"evidence"`
	Stats        map[string]int64 `json:"stats,omitempty"`
}

type Exposure struct {
	TimeMs int64             `json:"time_ms"`
	Tokens *model.TokenUsage `json:"tokens,omitempty"`
	Count  int               `json:"count"`
}

// Share is an exposure over a printed denominator.
type Share struct {
	Pct  float64 `json:"pct"`
	OfMs int64   `json:"of_ms"`
	Of   string  `json:"of"` // "in_turn" | "elapsed": the main thread's time in turns, or its elapsed time (the UI phrases it)
}

type Row struct {
	Label    string            `json:"label"`
	N        int               `json:"n"`
	TimeMs   int64             `json:"time_ms"`
	Tokens   *model.TokenUsage `json:"tokens,omitempty"`
	Sessions int               `json:"sessions"`
}

type Evidence struct {
	Session string            `json:"session"`
	Title   string            `json:"title"`
	Lane    string            `json:"lane"`
	LaneID  string            `json:"lane_id"`
	A       int64             `json:"a"`
	B       int64             `json:"b"`
	Op      string            `json:"op,omitempty"`
	TimeMs  int64             `json:"time_ms"`
	Tokens  *model.TokenUsage `json:"tokens,omitempty"`
	Note    string            `json:"note,omitempty"`
}

// GroupOrder is the page order of the groups when exposures tie; not_measured is always last.
var GroupOrder = []string{GroupYou, GroupAgents, GroupFailures, GroupLongRuns, GroupContext, GroupModels, GroupUnseen}

// InPeriod reports whether a session with this last activity and live state belongs to the
// period. Live sessions never count unless asked for (their numbers move on every refresh).
func (p Params) InPeriod(id string, ended int64, live bool) bool {
	if p.Period.Kind == "session" {
		return id == p.Period.Session
	}
	if live && !p.IncludeLive {
		return false
	}
	return ended >= p.Period.From && ended <= p.Period.To
}

// Build assembles the report for params from the facts of the sessions in scope (the caller
// has already selected them with InPeriod and filtered by project).
func Build(params Params, inputs []Input, now int64) Report {
	r := Report{GeneratedAt: now, Params: params, Scope: Scope{CLIs: map[string]int{}}}
	sort.SliceStable(inputs, func(a, b int) bool { return inputs[a].Facts.Ended > inputs[b].Facts.Ended })
	titles := map[string]string{}
	for _, in := range inputs {
		f := &in.Facts
		r.Sources = append(r.Sources, Source{ID: f.ID, Fingerprint: in.Fingerprint, Title: f.Title, Ended: f.Ended, Live: f.Live})
		titles[f.ID] = f.Title
		r.Scope.Sessions++
		r.Scope.RootElapsed += f.Root.ElapsedMs
		r.Scope.RootInTurn += f.Root.InTurnMs
		r.Scope.WaitUser += f.Root.ByPhase["wait_user"]
		r.Scope.Tokens.Add(&f.Root.AllTokens)
		if f.CLI != "" {
			r.Scope.CLIs[f.CLI]++
		}
	}
	r.SourcesHash = SourcesHash(r.Sources)
	switch {
	case len(inputs) == 0:
		r.Fallback = "no_sessions"
	case len(inputs) < 3 && params.Period.Kind != "session":
		r.Fallback = "fewer_than_3_sessions"
	}
	byRule := map[string][]Result{}
	for _, in := range inputs {
		f := in.Facts
		for id, res := range RunAll(&f) {
			byRule[id] = append(byRule[id], res)
		}
	}
	groups := map[string]*Group{}
	r.NoData = []NoDataRow{}
	for _, d := range Catalogue {
		card, nd := buildCard(d, byRule[d.ID], &r.Scope, titles)
		if nd.Sessions > 0 || nd.Items > 0 {
			r.NoData = append(r.NoData, nd)
		}
		if card == nil {
			continue
		}
		card.Info = d.Info
		g := groups[d.Group]
		if g == nil {
			g = &Group{ID: d.Group}
			groups[d.Group] = g
		}
		g.Cards = append(g.Cards, *card)
		if !d.Info {
			g.TimeMs += card.Exposure.TimeMs
			if card.Exposure.Tokens != nil {
				g.Tokens.Add(card.Exposure.Tokens)
			}
		}
	}
	for _, id := range GroupOrder {
		if g := groups[id]; g != nil {
			sort.SliceStable(g.Cards, func(a, b int) bool {
				if g.Cards[a].Info != g.Cards[b].Info {
					return !g.Cards[a].Info // ranked cards first, measurements last
				}
				return g.Cards[a].Exposure.TimeMs > g.Cards[b].Exposure.TimeMs
			})
			r.Groups = append(r.Groups, *g)
		}
	}
	rank := func(key func(Group) int64) []int {
		idx := make([]int, len(r.Groups))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool {
			ga, gb := r.Groups[idx[a]], r.Groups[idx[b]]
			if ga.ID == GroupUnseen || gb.ID == GroupUnseen {
				return gb.ID == GroupUnseen && ga.ID != GroupUnseen
			}
			return key(ga) > key(gb)
		})
		return idx
	}
	for pos, i := range rank(func(g Group) int64 { return g.TimeMs }) {
		r.Groups[i].OrderTime = pos
	}
	for pos, i := range rank(func(g Group) int64 { return billable(&g.Tokens) }) {
		r.Groups[i].OrderTokens = pos
	}
	sort.SliceStable(r.Groups, func(a, b int) bool { return r.Groups[a].OrderTime < r.Groups[b].OrderTime })
	var cards []Card
	for _, g := range r.Groups {
		if g.ID == GroupUnseen {
			continue
		}
		for _, c := range g.Cards {
			if !c.Info {
				cards = append(cards, c)
			}
		}
	}
	sort.SliceStable(cards, func(a, b int) bool { return cards[a].Exposure.TimeMs > cards[b].Exposure.TimeMs })
	for i := 0; i < len(cards) && i < 3; i++ {
		if cards[i].Exposure.TimeMs > 0 {
			r.TopTime = append(r.TopTime, cards[i].Rule)
		}
	}
	sort.SliceStable(cards, func(a, b int) bool { return billable(cards[a].Exposure.Tokens) > billable(cards[b].Exposure.Tokens) })
	for i := 0; i < len(cards) && i < 3; i++ {
		if billable(cards[i].Exposure.Tokens) > 0 {
			r.TopTokens = append(r.TopTokens, cards[i].Rule)
		}
	}
	return r
}

// billable is the tokens axis: uncached input + output (the axis is named on the page; cached
// input and reasoning are shown separately and never weighted).
func billable(t *model.TokenUsage) int64 {
	if t == nil {
		return 0
	}
	return t.Input - t.Cached + t.Output
}

func SourcesHash(src []Source) string {
	h := sha1.New()
	for _, s := range src {
		fmt.Fprintf(h, "%s=%s;", s.ID, s.Fingerprint)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// buildCard aggregates one rule over the sessions in scope. A card with no finding anywhere is
// not rendered (rule 3: evidence is mandatory); its "no data" counts still reach the
// not_measured group through the returned row.
func buildCard(d Detector, results []Result, scope *Scope, titles map[string]string) (*Card, NoDataRow) {
	c := &Card{Rule: d.ID, Group: d.Group, Title: d.Title, Stats: map[string]int64{}}
	nd := NoDataRow{Rule: d.ID, Title: d.Title}
	var all []Finding
	rows := map[string]*Row{}
	rowSessions := map[string]map[string]bool{}
	sessionsWith := map[string]bool{}
	for _, res := range results {
		if !res.Measurable {
			c.NoData++
			nd.Sessions++
			if c.Reason == "" {
				c.Reason = res.Reason
				nd.Reason = res.Reason
			}
			continue
		}
		c.Of++
		c.NoDataItems += res.NoData
		nd.Items += res.NoData
		for k, v := range res.Stats {
			c.Stats[k] += v
		}
		all = append(all, res.Findings...)
	}
	all = postFilter(d.ID, all)
	if len(all) == 0 {
		return nil, nd
	}
	{
		for _, x := range all {
			sessionsWith[x.Session] = true
			c.Exposure.TimeMs += x.TimeMs
			c.Exposure.Count++
			if x.Tokens != nil {
				if c.Exposure.Tokens == nil {
					c.Exposure.Tokens = &model.TokenUsage{}
				}
				c.Exposure.Tokens.Add(x.Tokens)
			}
			key := x.Key
			if key == "" {
				key = "all"
			}
			row := rows[key]
			if row == nil {
				row = &Row{Label: key}
				rows[key] = row
				rowSessions[key] = map[string]bool{}
			}
			row.N++
			row.TimeMs += x.TimeMs
			if x.Tokens != nil {
				if row.Tokens == nil {
					row.Tokens = &model.TokenUsage{}
				}
				row.Tokens.Add(x.Tokens)
			}
			rowSessions[key][x.Session] = true
		}
	}
	c.Sessions = len(sessionsWith)
	for key, row := range rows {
		row.Sessions = len(rowSessions[key])
		c.Distribution = append(c.Distribution, *row)
	}
	sortRows(d.ID, c.Distribution)
	c.Share = shareFor(d.ID, c, scope)
	c.Conventions = conventionsFor(d.ID)
	statsFor(d.ID, c, all)
	sort.SliceStable(all, func(a, b int) bool {
		if d.ID == "T1" { // the biggest re-reads first
			return billable(all[a].Tokens) > billable(all[b].Tokens)
		}
		return all[a].TimeMs > all[b].TimeMs
	})
	for i, x := range all {
		if i >= 10 {
			break
		}
		c.Evidence = append(c.Evidence, Evidence{Session: x.Session, Title: titles[x.Session], Lane: x.Lane, LaneID: x.LaneID, A: x.A, B: x.B, Op: x.Op, TimeMs: x.TimeMs, Tokens: x.Tokens, Note: x.Note})
	}
	return c, nd
}

// postFilter applies the cross-session conditions a single session cannot know: long tool runs
// count only for command shapes that ran at least twice in the period; invalid tool calls are
// shown from three occurrences.
func postFilter(rule string, all []Finding) []Finding {
	switch rule {
	case "D9":
		n := map[string]int{}
		for _, x := range all {
			n[x.Key]++
		}
		out := all[:0]
		for _, x := range all {
			if n[x.Key] >= 2 {
				out = append(out, x)
			}
		}
		return out
	case "D14":
		if len(all) < 3 {
			return nil
		}
	}
	return all
}

func sortRows(rule string, rows []Row) {
	var fixed []string
	switch rule {
	case "T1", "D1", "D2":
		fixed = GapBucketOrder()
	case "T2":
		fixed = ContextBucketOrder()
	}
	if fixed != nil {
		order := map[string]int{}
		for i, b := range fixed {
			order[b] = i
		}
		sort.SliceStable(rows, func(a, b int) bool { return order[rows[a].Label] < order[rows[b].Label] })
		return
	}
	sort.SliceStable(rows, func(a, b int) bool {
		if rows[a].Sessions != rows[b].Sessions && (rule == "D7") {
			return rows[a].Sessions > rows[b].Sessions
		}
		return rows[a].TimeMs > rows[b].TimeMs
	})
}

func shareFor(rule string, c *Card, scope *Scope) *Share {
	switch rule {
	case "D1", "D2", "D2b":
		if scope.RootElapsed > 0 {
			return &Share{Pct: pct(c.Exposure.TimeMs, scope.RootElapsed), OfMs: scope.RootElapsed, Of: "elapsed"}
		}
	case "D3", "D4", "D7", "D9", "M1":
		if scope.RootInTurn > 0 {
			return &Share{Pct: pct(c.Exposure.TimeMs, scope.RootInTurn), OfMs: scope.RootInTurn, Of: "in_turn"}
		}
	case "D11":
		var root int64
		for _, row := range c.Distribution {
			if row.Label == "main thread" {
				root = row.TimeMs
			}
		}
		if scope.RootElapsed > 0 {
			return &Share{Pct: pct(root, scope.RootElapsed), OfMs: scope.RootElapsed, Of: "elapsed"}
		}
	}
	return nil
}

func pct(n, of int64) float64 {
	if of <= 0 {
		return 0
	}
	return float64(n) * 100 / float64(of)
}

func conventionsFor(rule string) []string {
	switch rule {
	case "T1", "D1":
		return []string{"gap buckets: " + strings.Join(GapBucketOrder(), ", ")}
	case "D2", "D2b":
		return []string{"long break = 4 h", "gap buckets: " + strings.Join(GapBucketOrder(), ", ")}
	case "T2":
		return []string{"context buckets: " + strings.Join(ContextBucketOrder(), ", ")}
	case "D9":
		return []string{"command shapes that ran 2 or more times"}
	case "D14":
		return []string{"shown from 3 occurrences"}
	}
	return nil
}

// statsFor adds the rule-specific numbers a card headline needs beyond the exposure.
func statsFor(rule string, c *Card, all []Finding) {
	switch rule {
	case "D11":
		var contexts []int64
		for _, x := range all {
			var ctx int64
			if n, _ := fmt.Sscanf(x.Note, "context before: %d k tokens", &ctx); n == 1 {
				contexts = append(contexts, ctx*1000)
			}
		}
		if len(contexts) > 0 {
			sort.Slice(contexts, func(a, b int) bool { return contexts[a] < contexts[b] })
			c.Stats["context_median"] = contexts[len(contexts)/2]
			c.Stats["context_min"] = contexts[0]
			c.Stats["context_max"] = contexts[len(contexts)-1]
		}
		for _, row := range c.Distribution {
			k := "sub"
			if row.Label == "main thread" {
				k = "main"
			}
			c.Stats["time_"+k] = row.TimeMs
			c.Stats["count_"+k] = int64(row.N)
		}
	case "D7":
		var groups, attempts int64
		for _, x := range all {
			groups++
			var a, f int
			if n, _ := fmt.Sscanf(x.Note, "%d attempts, %d failed", &a, &f); n == 2 {
				attempts += int64(a)
			}
		}
		c.Stats["groups"] = groups
		c.Stats["attempts"] = attempts
	case "T1":
		var slow, slowUncached int64
		for _, x := range all {
			if x.TimeMs >= 15*60e3 {
				slow++
				slowUncached += billable(x.Tokens) - tokensOutput(x.Tokens)
			}
		}
		c.Stats["starts_after_15m"] = slow
		c.Stats["uncached_after_15m"] = slowUncached
	case "D2":
		times := make([]int64, 0, len(all))
		for _, x := range all {
			times = append(times, x.TimeMs)
		}
		sort.Slice(times, func(a, b int) bool { return times[a] < times[b] })
		if n := len(times); n > 0 {
			c.Stats["median"] = times[n/2]
			c.Stats["p90"] = times[min(n-1, n*9/10)]
		}
	case "D3":
		for _, row := range c.Distribution {
			k := "sub"
			if row.Label == "main thread" {
				k = "main"
			}
			c.Stats["count_"+k] = int64(row.N)
			c.Stats["time_"+k] = row.TimeMs
		}
	case "D9", "D12", "D13", "D15":
		for _, row := range c.Distribution {
			c.Stats["count_"+row.Label] = int64(row.N)
		}
	}
}

func tokensOutput(t *model.TokenUsage) int64 {
	if t == nil {
		return 0
	}
	return t.Output
}
