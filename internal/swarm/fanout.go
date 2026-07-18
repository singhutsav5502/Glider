package swarm

import (
	"context"
	"fmt"
	"sync"
)

// FanOut runs workers in parallel with parent context cancel.
// When ctx is cancelled, worker contexts are cancelled and FanOut returns
// partial results collected so far plus ctx.Err() if no worker finished.
// Never unbounded: MaxWorkers capped at 4; MaxInflight semaphore applied.
// Results are buffered through a sized channel (ResultChanSize) for backpressure
// before the final slice is assembled. Shared TurnID is stamped onto episodes.
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
			if w.Run == nil {
				res.Err = fmt.Errorf("swarm: nil Run on worker %s", id)
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
	got := 0
	for ir := range outCh {
		if ir.i >= 0 && ir.i < len(results) {
			results[ir.i] = ir.r
			got++
		}
	}
	_ = got

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
