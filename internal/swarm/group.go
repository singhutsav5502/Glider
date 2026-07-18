package swarm

import (
	"context"
	"sync"

	"github.com/glider-ai/glider/internal/config"
)

// Default channel / queue sizes when config leaves zeros.
const (
	DefaultResultChanSize  = 32
	DefaultWorkerQueueSize = 16
	DefaultMaxInflight     = 4
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
		MaxWorkers:      fo,
		MaxInflight:     ChanSize(cc.MaxInflight, fo),
		WorkerQueueSize: ChanSize(cc.WorkerQueueSize, DefaultWorkerQueueSize),
		ResultChanSize:  ChanSize(cc.ResultChanSize, DefaultResultChanSize),
	}
}

// Group is a minimal errgroup: first error cancels siblings; Wait joins all.
type Group struct {
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	errOnce sync.Once
	err     error
}

// WithContext returns a Group derived from ctx.
func WithContext(ctx context.Context) (*Group, context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	return &Group{ctx: ctx, cancel: cancel}, ctx
}

// Go runs fn in a goroutine. On non-nil error, cancels the group context.
func (g *Group) Go(fn func(ctx context.Context) error) {
	g.wg.Add(1)
	go func() {
		defer g.wg.Done()
		if err := fn(g.ctx); err != nil {
			g.errOnce.Do(func() {
				g.err = err
				g.cancel()
			})
		}
	}()
}

// Wait blocks until all Go calls finish and returns the first error (if any).
func (g *Group) Wait() error {
	g.wg.Wait()
	g.cancel()
	return g.err
}
