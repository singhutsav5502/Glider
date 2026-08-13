package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/contextkit"
)

// Role mirrors classifier hints: plan | exec | research.
type Role string

const (
	RolePlan     Role = "plan"
	RoleExec     Role = "exec"
	RoleResearch Role = "research"
	RoleWorker   Role = "worker" // generic short-lived action
)

// Worker is one bounded fan-out action (single request, not a long-lived subagent).
type Worker struct {
	ID    string
	Role  Role
	Model string
	// Run performs the worker's single action. Must respect ctx cancel.
	Run func(ctx context.Context) (contextkit.Episode, error)
}

// Result is one worker return (Episode + optional error).
type Result struct {
	WorkerID string
	Role     Role
	Model    string
	Episode  contextkit.Episode
	Err      error
}

// Options bounds fan-out concurrency and channel backpressure.
type Options struct {
	MaxWorkers      int // hard cap (default 2, max 4)
	MaxInflight     int // semaphore; 0 → MaxWorkers
	WorkerQueueSize int // unused reserved slot sizing hint
	ResultChanSize  int // merge / stream buffer; 0 → 32
	// TurnID is the shared contextgraph turn family for this fan-out wave.
	TurnID string
	// The code calls OnStart when a worker starts. This field is optional, and the
	// live graph uses it.
	OnStart func(Result)
	// The code calls OnResult when each worker completes. This field is optional. A
	// stream or a graph uses it.
	OnResult func(Result)
}

// Default channel sizes when config leaves zeros.
const (
	DefaultResultChanSize = 32
	DefaultMaxInflight    = 4
)

// ChanSize returns n if positive, otherwise fallback.
func ChanSize(n, fallback int) int {
	if n > 0 {
		return n
	}
	if fallback > 0 {
		return fallback
	}
	return DefaultResultChanSize
}

// OptionsFromConfig maps orchestration.concurrency (+ fan_out max) into Options.
func OptionsFromConfig(c config.OrchestrationConfig) Options {
	fo := c.FanOut.MaxWorkers
	if fo <= 0 {
		fo = 2
	}
	cc := c.Concurrency
	return Options{
		MaxWorkers:     fo,
		MaxInflight:    ChanSize(cc.MaxInflight, fo),
		ResultChanSize: ChanSize(cc.ResultChanSize, DefaultResultChanSize),
	}
}

// FanOut operates the workers at the same time, and the context of the parent
// can cancel them.
//
// When code cancels ctx, this function cancels the context of each worker. It
// then returns the results that it collected, and it adds ctx.Err() when no
// worker completed.
//
// It always has a limit: MaxWorkers has a maximum of 4, and a semaphore applies
// MaxInflight.
func FanOut(ctx context.Context, workers []Worker, opts Options) ([]Result, error) {
	if len(workers) == 0 {
		return nil, nil
	}
	n := opts.MaxWorkers
	if n <= 0 {
		n = 2
	}
	if n > 4 {
		n = 4
	}
	if len(workers) < n {
		n = len(workers)
	}
	workers = workers[:n]

	inflight := opts.MaxInflight
	if inflight <= 0 {
		inflight = n
	}
	if inflight > n {
		inflight = n
	}
	sem := make(chan struct{}, inflight)
	buf := ChanSize(opts.ResultChanSize, DefaultResultChanSize)
	outCh := make(chan indexedResult, buf)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for i, w := range workers {
		wg.Add(1)
		go func(i int, w Worker) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				pushResult(ctx, outCh, indexedResult{i: i, r: Result{WorkerID: w.ID, Role: w.Role, Model: w.Model, Err: ctx.Err()}})
				return
			}
			id := w.ID
			if id == "" {
				id = fmt.Sprintf("worker-%d", i)
			}
			res := Result{WorkerID: id, Role: w.Role, Model: w.Model}
			if opts.OnStart != nil {
				opts.OnStart(res)
			}
			if w.Run == nil {
				res.Err = fmt.Errorf("fanout: nil Run on worker %s", id)
				pushResult(ctx, outCh, indexedResult{i: i, r: res})
				return
			}
			ep, err := w.Run(ctx)
			res.Episode = ep
			res.Err = err
			if res.Episode.Model == "" {
				res.Episode.Model = w.Model
			}
			if res.Episode.Role == "" && w.Role != "" {
				res.Episode.Role = string(w.Role)
			}
			if opts.TurnID != "" {
				if res.Episode.Reason == "" {
					res.Episode.Reason = "fan_out"
				}
				if res.Episode.ID == "" {
					res.Episode.ID = fmt.Sprintf("%s-%s", opts.TurnID, id)
				}
			}
			if opts.OnResult != nil {
				opts.OnResult(res)
			}
			pushResult(ctx, outCh, indexedResult{i: i, r: res})
		}(i, w)
	}

	go func() {
		wg.Wait()
		close(outCh)
	}()

	results := make([]Result, len(workers))
	for ir := range outCh {
		if ir.i >= 0 && ir.i < len(results) {
			results[ir.i] = ir.r
		}
	}

	select {
	case <-ctx.Done():
		if err := firstErr(results); err != nil {
			return results, err
		}
		return results, ctx.Err()
	default:
		return results, firstErr(results)
	}
}

type indexedResult struct {
	i int
	r Result
}

func pushResult(ctx context.Context, ch chan<- indexedResult, ir indexedResult) {
	select {
	case ch <- ir:
	case <-ctx.Done():
		select {
		case ch <- ir:
		default:
		}
	}
}

func firstErr(results []Result) error {
	for _, r := range results {
		if r.Err != nil {
			return r.Err
		}
	}
	return nil
}

// Per-worker / merge retention for audit-sized fan-out text (hard caps).
const mergeWorkerSummaryCap = 16000
const mergeFailNoteCap = 800

// mergeResults concatenates episode summaries into one Episode (fan-out text merge).
func mergeResults(results []Result) contextkit.Episode {
	var parts []string
	var tokens int
	var artifacts []string
	ok := 0
	role := ""
	for _, r := range results {
		if r.Err != nil {
			continue
		}
		ok++
		label := r.WorkerID
		if label == "" {
			label = r.Model
		}
		sum := strings.TrimSpace(r.Episode.Summary)
		if sum == "" {
			sum = strings.TrimSpace(strings.Join(r.Episode.Artifacts, " "))
		}
		if sum != "" {
			parts = append(parts, fmt.Sprintf("[%s] %s", label, truncate(sum, mergeWorkerSummaryCap)))
		}
		tokens += r.Episode.Tokens
		artifacts = append(artifacts, r.Episode.Artifacts...)
		if role == "" && r.Role != "" {
			role = string(r.Role)
		}
		if role == "" {
			role = r.Episode.Role
		}
	}
	summary := strings.Join(parts, " | ")
	if summary == "" {
		summary = fmt.Sprintf("fan_out merged %d/%d workers", ok, len(results))
	}
	return contextkit.Episode{
		ID:        fmt.Sprintf("merge-%d", ok),
		Summary:   summary,
		Artifacts: artifacts,
		Tokens:    tokens,
		Model:     "glider-fanout",
		Reason:    "fan_out",
		Role:      role,
	}
}

// CritiqueMerge ranks successful workers, drops empty failures from the weave,
// and annotates fail count — a lightweight critic pass after fan-in (not an LLM).
func CritiqueMerge(results []Result) contextkit.Episode {
	type scored struct {
		r     Result
		score int
	}
	var okList []scored
	failN := 0
	var failNotes []string
	for _, r := range results {
		if r.Err != nil {
			failN++
			label := r.WorkerID
			if label == "" {
				label = string(r.Role)
			}
			failNotes = append(failNotes, fmt.Sprintf("%s:%s", label, truncate(r.Err.Error(), mergeFailNoteCap)))
			continue
		}
		sum := strings.TrimSpace(r.Episode.Summary)
		if sum == "" {
			sum = strings.TrimSpace(strings.Join(r.Episode.Artifacts, " "))
		}
		sc := len(sum) + r.Episode.Tokens*2
		if sc <= 0 {
			sc = 1
		}
		okList = append(okList, scored{r: r, score: sc})
	}
	// Prefer longer / higher-token summaries (weak quality proxy without an LLM critic).
	for i := 0; i < len(okList); i++ {
		for j := i + 1; j < len(okList); j++ {
			if okList[j].score > okList[i].score {
				okList[i], okList[j] = okList[j], okList[i]
			}
		}
	}
	ranked := make([]Result, len(okList))
	for i, s := range okList {
		ranked[i] = s.r
	}
	ep := mergeResults(ranked)
	ep.Reason = "critique_merge"
	ep.ID = fmt.Sprintf("critique-%d-%d", len(ranked), failN)
	prefix := fmt.Sprintf("critique ok=%d fail=%d", len(ranked), failN)
	if len(failNotes) > 0 {
		prefix += " drops=[" + strings.Join(failNotes, "; ") + "]"
	}
	if ep.Summary != "" {
		ep.Summary = prefix + " | " + ep.Summary
	} else {
		ep.Summary = prefix
	}
	return ep
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
