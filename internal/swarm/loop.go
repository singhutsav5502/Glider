package swarm

import (
	"context"
	"sync"
	"time"

	"github.com/glider-ai/glider/internal/contextkit"
)

// LoopJob is one recurring agent task (compatible with a future Cursor /loop wake).
type LoopJob struct {
	ID         string
	Checkpoint contextkit.LoopCheckpoint
	Interval   time.Duration
	// Tick updates the checkpoint; return updated cp + nil to continue,
	// or error to stop (context cancel also stops).
	Tick func(ctx context.Context, cp contextkit.LoopCheckpoint) (contextkit.LoopCheckpoint, error)
}

// LoopTick is emitted after each successful Tick.
type LoopTick struct {
	JobID      string
	Checkpoint contextkit.LoopCheckpoint
	At         time.Time
	Err        error
}

// LoopRunner runs recurring jobs until Stop or context cancel.
type LoopRunner interface {
	Start(ctx context.Context, job LoopJob) (<-chan LoopTick, error)
	Stop()
}

// IntervalLoop is a minimal ticker-based LoopRunner (not Cursor-integrated yet).
type IntervalLoop struct {
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

var _ LoopRunner = (*IntervalLoop)(nil)

// Start begins the loop. Only one job per IntervalLoop instance.
func (l *IntervalLoop) Start(ctx context.Context, job LoopJob) (<-chan LoopTick, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		l.cancel()
		if l.done != nil {
			<-l.done
		}
	}
	interval := job.Interval
	if interval <= 0 {
		if job.Checkpoint.NextDelaySec > 0 {
			interval = time.Duration(job.Checkpoint.NextDelaySec) * time.Second
		} else {
			interval = 5 * time.Minute
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel
	l.done = make(chan struct{})
	out := make(chan LoopTick, 8)
	cp := job.Checkpoint
	go func() {
		defer close(out)
		defer close(l.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		if job.Tick != nil {
			next, err := job.Tick(runCtx, cp)
			if err == nil {
				cp = next
			}
			select {
			case out <- LoopTick{JobID: job.ID, Checkpoint: cp, At: time.Now().UTC(), Err: err}:
			case <-runCtx.Done():
				return
			}
			if err != nil {
				return
			}
		}
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				if job.Tick == nil {
					continue
				}
				next, err := job.Tick(runCtx, cp)
				if err == nil {
					cp = next
				}
				select {
				case out <- LoopTick{JobID: job.ID, Checkpoint: cp, At: time.Now().UTC(), Err: err}:
				case <-runCtx.Done():
					return
				}
				if err != nil {
					return
				}
				if cp.NextDelaySec > 0 {
					ticker.Reset(time.Duration(cp.NextDelaySec) * time.Second)
				}
			}
		}
	}()
	return out, nil
}

// Stop cancels the active loop.
func (l *IntervalLoop) Stop() {
	l.mu.Lock()
	cancel := l.cancel
	done := l.done
	l.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}
