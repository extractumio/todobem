package insights

import (
	"sync"
	"time"

	"github.com/extractumio/todobem/internal/model"
)

// Loader parses one session on a path of the caller's choosing. The server's loader opens the
// rollout files into a private session, writes the model cache and drops the model after
// Extract, so a scan never evicts the sessions a user has open nor fills the model pool.
type Loader interface {
	Parse(id string) (*model.Session, error)
}

// Progress is what the page polls while a scan runs.
type Progress struct {
	Running    bool     `json:"running"`
	Done       int      `json:"done"`
	Total      int      `json:"total"`
	Errors     int      `json:"errors"`
	Current    []string `json:"current,omitempty"`
	StartedAt  int64    `json:"started_at,omitempty"`
	FinishedAt int64    `json:"finished_at,omitempty"`
	Cancelled  bool     `json:"cancelled,omitempty"`
	LastError  string   `json:"last_error,omitempty"`
}

// Scanner parses the sessions of a report that have no facts yet, a bounded number at a time,
// and hands each Facts to the sink. One scan at a time; Cancel stops after the sessions in
// flight. Nothing runs unless Start is called (an explicit Analyze).
type Scanner struct {
	loader  Loader
	sink    func(id string, f Facts)
	workers int

	mu       sync.Mutex
	progress Progress
	stop     chan struct{}
}

func NewScanner(loader Loader, workers int, sink func(id string, f Facts)) *Scanner {
	if workers < 1 {
		workers = 1
	}
	return &Scanner{loader: loader, sink: sink, workers: workers}
}

// Start begins a scan of ids; it returns false when one is already running.
func (sc *Scanner) Start(ids []string) bool {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.progress.Running {
		return false
	}
	sc.progress = Progress{Running: true, Total: len(ids), StartedAt: time.Now().UnixMilli()}
	sc.stop = make(chan struct{})
	queue := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < sc.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range queue {
				sc.parseOne(id)
			}
		}()
	}
	go func(stop chan struct{}) {
		for _, id := range ids {
			select {
			case <-stop:
				sc.mu.Lock()
				sc.progress.Cancelled = true
				sc.mu.Unlock()
				close(queue)
				wg.Wait()
				sc.finish()
				return
			case queue <- id:
			}
		}
		close(queue)
		wg.Wait()
		sc.finish()
	}(sc.stop)
	return true
}

func (sc *Scanner) parseOne(id string) {
	sc.mu.Lock()
	sc.progress.Current = append(sc.progress.Current, id)
	sc.mu.Unlock()
	m, err := sc.loader.Parse(id)
	var f Facts
	if err == nil {
		f = Extract(m)
	}
	sc.mu.Lock()
	for i, c := range sc.progress.Current {
		if c == id {
			sc.progress.Current = append(sc.progress.Current[:i], sc.progress.Current[i+1:]...)
			break
		}
	}
	sc.progress.Done++
	if err != nil {
		sc.progress.Errors++
		sc.progress.LastError = id + ": " + err.Error()
	}
	sc.mu.Unlock()
	if err == nil && sc.sink != nil {
		sc.sink(id, f)
	}
}

func (sc *Scanner) finish() {
	sc.mu.Lock()
	sc.progress.Running = false
	sc.progress.Current = nil
	sc.progress.FinishedAt = time.Now().UnixMilli()
	sc.mu.Unlock()
}

// Cancel asks a running scan to stop after the sessions in flight; a no-op otherwise.
func (sc *Scanner) Cancel() {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.progress.Running && sc.stop != nil {
		select {
		case <-sc.stop:
		default:
			close(sc.stop)
		}
	}
}

func (sc *Scanner) Progress() Progress {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	p := sc.progress
	p.Current = append([]string(nil), p.Current...)
	return p
}
