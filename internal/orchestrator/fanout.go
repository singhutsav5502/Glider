package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/contextkit"
)

// ErrFanOutDisabled is returned when StrategyFanOut is requested but not enabled.
var ErrFanOutDisabled = errors.New("fan_out strategy disabled (set orchestration.fan_out.enabled)")

// FanOutConfig feature-flags gateway-only multi-model fan-out (swarm foundation).
type FanOutConfig struct {
	Enabled    bool
	MaxWorkers int // default 2
	// EpisodeStore optionally records compressed worker returns.
	EpisodeStore *contextkit.Store
	SessionID    string
	// Graph optionally emits EpisodeMerged (hook; nil-safe).
	Graph *contextgraph.Store
	// TurnID binds fan-out merge into a turn family when set.
	TurnID string
	// ResultChanSize backpressure for merged chunk channel (0 → swarm default).
	ResultChanSize int
	MaxInflight    int
}

// FanOutConfigFromOrchestration maps YAML orchestration block into FanOutConfig.
func FanOutConfigFromOrchestration(c config.OrchestrationConfig, store *contextkit.Store, sessionID string) FanOutConfig {
	opts := OptionsFromConfig(c)
	return FanOutConfig{
		Enabled:        c.FanOut.Enabled,
		MaxWorkers:     opts.MaxWorkers,
		EpisodeStore:   store,
		SessionID:      sessionID,
		ResultChanSize: opts.ResultChanSize,
		MaxInflight:    opts.MaxInflight,
	}
}

// FanOutExecutor runs StrategyFanOut when Enabled; otherwise delegates to Inner
// for StrategySingle, or returns ErrFanOutDisabled for fan_out.
// Uses internal/swarm FanOut with parent context cancel + MergeResults.
type FanOutExecutor struct {
	Inner  Executor
	Config FanOutConfig
	// live holds hot-swappable fan-out enable/limits (generation bump on Apply).
	live atomic.Pointer[FanOutConfig]
}

var _ Executor = (*FanOutExecutor)(nil)

// ApplyConfig updates live fan-out settings (hot-swap friendly).
func (e *FanOutExecutor) ApplyConfig(c FanOutConfig) {
	if e == nil {
		return
	}
	cp := c
	e.live.Store(&cp)
	e.Config = c
}

func (e *FanOutExecutor) cfg() FanOutConfig {
	if e == nil {
		return FanOutConfig{}
	}
	if p := e.live.Load(); p != nil {
		return *p
	}
	return e.Config
}

// Execute dispatches by decision.Strategy.
func (e *FanOutExecutor) Execute(ctx context.Context, decision *backend.RoutingDecision, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	if e == nil || e.Inner == nil {
		return nil, fmt.Errorf("fan_out: nil inner executor")
	}
	if decision == nil {
		return nil, fmt.Errorf("fan_out: nil decision")
	}
	if decision.Strategy != backend.StrategyFanOut {
		return e.Inner.Execute(ctx, decision, req)
	}
	cfg := e.cfg()
	if !cfg.Enabled {
		return nil, ErrFanOutDisabled
	}
	n := cfg.MaxWorkers
	if n <= 0 {
		n = 2
	}
	if len(decision.SubTasks) > 0 && len(decision.SubTasks) < n {
		n = len(decision.SubTasks)
	}
	if n > 4 {
		n = 4
	}

	workers := make([]Worker, n)
	for i := 0; i < n; i++ {
		i := i
		model := decision.Model
		target := decision.Target
		if len(decision.SubTasks) > i {
			st := decision.SubTasks[i]
			if st.Model != "" {
				model = st.Model
			}
			if st.Target != "" {
				target = st.Target
			}
		}
		workers[i] = Worker{
			ID:    fmt.Sprintf("worker-%d", i),
			Role:  Role(decision.Role),
			Model: model,
			Run: func(wctx context.Context) (contextkit.Episode, error) {
				d := *decision
				d.Strategy = backend.StrategySingle
				d.Model = model
				d.Target = target
				reqCopy := *req
				if model != "" {
					reqCopy.Model = model
				}
				ch, err := e.Inner.Execute(wctx, &d, &reqCopy)
				if err != nil {
					return contextkit.Episode{}, err
				}
				var text string
				for c := range ch {
					select {
					case <-wctx.Done():
						return contextkit.Episode{Summary: text, Model: model, Tokens: len(text) / 4}, wctx.Err()
					default:
					}
					text += c.Content
				}
				return contextkit.Episode{
					Summary: text,
					Model:   model,
					Tokens:  len(text) / 4,
					Reason:  "fan_out",
					Role:    decision.Role,
				}, nil
			},
		}
	}

	turnID := ""
	if req != nil {
		turnID = req.Metadata.RequestID
	}
	results, err := FanOut(ctx, workers, Options{
		MaxWorkers:     n,
		MaxInflight:    cfg.MaxInflight,
		ResultChanSize: cfg.ResultChanSize,
		TurnID:         turnID,
	})
	// Partial success still merges; only hard-fail when parent cancelled with zero text.
	merged := CritiqueMerge(results)
	buf := ChanSize(cfg.ResultChanSize, DefaultResultChanSize)
	out := make(chan backend.CompletionChunk, buf)
	go func() {
		defer close(out)
		ok := 0
		for _, r := range results {
			if r.Err != nil || r.Episode.Summary == "" {
				continue
			}
			ok++
			out <- backend.CompletionChunk{Content: r.Episode.Summary + "\n", Model: r.Model}
		}
		if ok == 0 {
			msg := "fan_out: all workers failed"
			if err != nil {
				msg = fmt.Sprintf("fan_out: %v", err)
			}
			out <- backend.CompletionChunk{
				Content:      msg,
				FinishReason: "stop",
				Model:        "glider-fanout",
			}
			return
		}
		out <- backend.CompletionChunk{FinishReason: "stop", Model: "glider-fanout"}
		if cfg.EpisodeStore != nil {
			cfg.EpisodeStore.RecordEpisode(cfg.SessionID, merged)
		}
		epTurn := cfg.TurnID
		if epTurn == "" {
			epTurn = turnID
		}
		if cfg.Graph != nil {
			cfg.Graph.RecordEpisodeMerged(epTurn, turnID, merged.ID, map[string]string{
				"source": "fan_out", "tokens": fmt.Sprintf("%d", merged.Tokens),
			})
		}
	}()
	return out, nil
}
