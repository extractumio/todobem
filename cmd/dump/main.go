// dump parses one session and prints a summary (development aid).
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/extractumio/todobem/internal/codex"
	"github.com/extractumio/todobem/internal/model"
)

func fmtd(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	return d.Round(time.Second).String()
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: dump <root-thread-id> [export.json]")
		os.Exit(2)
	}
	id := os.Args[1]
	ix := codex.NewIndex(os.Getenv("HOME") + "/.codex")
	t0 := time.Now()
	ix.Scan()
	fmt.Println("scan", time.Since(t0))
	s, err := codex.Open(ix, id)
	if err != nil {
		panic(err)
	}
	t1 := time.Now()
	if _, err := s.Refresh(); err != nil {
		panic(err)
	}
	fmt.Println("parse", time.Since(t1))
	rd, dec := s.IOStats()
	fmt.Printf("bytes read %d MB, JSON-decoded %d MB (%.0f%%)\n", rd/1e6, dec/1e6, float64(dec)*100/float64(rd))
	m := s.Model
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
		fmt.Printf("  lane %-40s partition-elapsed=%d raw-ops=%s in-turn=%s\n", l.Path, part-(l.Ended-l.Started), fmtd(raw), fmtd(l.InTurnMs))
	}
	heads := map[string]int{}
	for _, l := range m.Lanes {
		for _, o := range l.Ops {
			if o.Phase == "unknown" {
				h := o.Title
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
	fmt.Println("  by_kind:", m.Totals.ByKind)
	fmt.Println("  parallel:", m.Parallel.Agents, "agents", fmtd(m.Parallel.AgentMs), "agent-time", fmtd(m.Parallel.WallMs), "wall")
	fmt.Println("  users:", m.Totals.UserMessages, "questions:", m.Totals.Questions, "compactions:", m.Totals.Compactions, "failed:", m.Totals.Failed)
	for _, l := range m.Lanes {
		fmt.Printf("LANE %-45s depth=%d turns=%d ops=%d segs=%d stages=%d markers=%d live=%v span=%s\n", l.Path, l.Depth, len(l.Turns), len(l.Ops), len(l.Segments), len(l.Stages), len(l.Markers), l.Live, fmtd(l.Ended-l.Started))
	}
	root := m.Lanes[0]
	fmt.Println("ROOT STAGES (first 40):")
	for i, st := range root.Stages {
		if i >= 40 {
			break
		}
		fmt.Printf("  %s  %-12s %8s  ops=%d\n", time.UnixMilli(st.Start).UTC().Format("01-02 15:04:05"), st.Phase, fmtd(st.End-st.Start), st.Ops)
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
	if len(os.Args) > 2 {
		b, _ := json.Marshal(m)
		fmt.Println("json bytes", len(b))
		os.WriteFile(os.Args[2], b, 0644)
	}
}
