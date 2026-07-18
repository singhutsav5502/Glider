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

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]Result, len(workers))
	var wg sync.WaitGroup
	for i, w := range workers {
		wg.Add(1)
		go func(i int, w Worker) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = Result{WorkerID: w.ID, Role: w.Role, Model: w.Model, Err: ctx.Err()}
				return
			}
			id := w.ID
			if id == "" {
				id = fmt.Sprintf("worker-%d", i)
			}
			res := Result{WorkerID: id, Role: w.Role, Model: w.Model}
			if w.Run == nil {
				res.Err = fmt.Errorf("swarm: nil Run on worker %s", id)
				results[i] = res
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
			results[i] = res
		}(i, w)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return results, firstErr(results)
	case <-ctx.Done():
		<-done
		if err := firstErr(results); err != nil {
			return results, err
		}
		return results, ctx.Err()
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
