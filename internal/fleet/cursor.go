package fleet

import (
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/extractumio/todobem/internal/model"
)

// Tracker is the agent's side of the delta list (docs/AGENT-MODE.md §6.5): a generation counter
// that advances on every scan in which a row was added or changed, the generation each row
// last changed in, and the count and hash of the current ids. There are no tombstones: a
// removed row and a restart (a new boot nonce) are both caught by the hub through the hash.
type Tracker struct {
	mu   sync.Mutex
	boot string
	gen  uint64
	rows map[string]tracked
}

// tracked is what the tracker remembers per row: the signature it last saw (the file set's
// size and time, the title, the question the tail found — totals and live state of an opened
// session derive from the same files) and the generation it changed in.
type tracked struct {
	sig rowSig
	gen uint64
}

type rowSig struct {
	updated, bytes, question, started int64
	title, source, cwd, branch        string
	cli, model, lastAnswer            string
	totals                            [sha256.Size]byte
	agents                            int
	live                              bool
}

func totalsSig(t *model.Totals) [sha256.Size]byte {
	if t == nil {
		return [sha256.Size]byte{}
	}
	b, _ := json.Marshal(t) // model.Totals contains only JSON-safe scalar maps
	return sha256.Sum256(b)
}

// NewTracker starts a tracker with a fresh boot nonce.
func NewTracker() *Tracker {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return &Tracker{boot: hex.EncodeToString(b[:]), rows: map[string]tracked{}}
}

// Page takes the rows of the latest scan and answers GET sessions?since=: the generation
// advances when any row is new or changed, rows that disappeared are forgotten (the hash
// changes; no tombstone), and the page holds every row when since is absent or from another
// boot, else the rows whose generation is past since's.
func (t *Tracker) Page(since string, rows []model.SessionSummary) SessionsPage {
	t.mu.Lock()
	defer t.mu.Unlock()
	seen := make(map[string]bool, len(rows))
	ids := make([]string, 0, len(rows))
	bumped := false
	for _, r := range rows {
		seen[r.ID] = true
		ids = append(ids, r.ID)
		sig := rowSig{
			updated: r.Updated, bytes: r.Bytes, question: r.Question, started: r.Started,
			title: r.Title, source: r.Source, cwd: r.CWD, branch: r.Branch,
			cli: r.CLI, model: r.Model, lastAnswer: r.LastAnswer,
			totals: totalsSig(r.Totals), agents: r.Agents, live: r.Live,
		}
		if old, ok := t.rows[r.ID]; ok && old.sig == sig {
			continue
		}
		if !bumped {
			t.gen++
			bumped = true
		}
		t.rows[r.ID] = tracked{sig: sig, gen: t.gen}
	}
	for id := range t.rows {
		if !seen[id] {
			delete(t.rows, id)
		}
	}
	page := SessionsPage{Cursor: t.boot + ":" + strconv.FormatUint(t.gen, 10), Count: len(ids), IDsHash: IDsHash(ids), Rows: []model.SessionSummary{}}
	boot, genText, ok := strings.Cut(since, ":")
	gen, err := strconv.ParseUint(genText, 10, 64)
	if !ok || boot != t.boot || err != nil {
		page.Full = true
		page.Rows = append(page.Rows, rows...)
		return page
	}
	for _, r := range rows {
		if t.rows[r.ID].gen > gen {
			page.Rows = append(page.Rows, r)
		}
	}
	return page
}

// IDsHash is the reconciliation hash both sides compute: SHA-1 of the ids sorted bytewise and
// joined by newlines, the first 16 hex characters.
func IDsHash(ids []string) string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	h := sha1.New()
	for i, id := range sorted {
		if i > 0 {
			h.Write([]byte{'\n'})
		}
		h.Write([]byte(id))
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}
