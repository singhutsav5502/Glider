// Package swarm provides minimal multi-worker orchestration skeletons inspired by
// Slate-style thread weaving — not a full swarm runtime.
//
// Working pieces:
//   - Swarm / Worker / Role interfaces
//   - FanOut with parent context cancel + bounded concurrency
//   - MergeResults callable stub (text/episode weave)
//   - LoopRunner for recurring agent tasks (Cursor /loop-compatible later)
//   - HotSwap Registry documenting Watch/Swap vs restart modules
//   - Group (errgroup-style) + channel size helpers from config
//
// Intentionally future: planner decomposition, Path B multi-agent, DSL action space.
package swarm

import (
	"context"

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

// Worker is one bounded action (Slate: one action then pause), not a long-lived subagent.
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
	// OnStart is called when a worker begins execution (optional; live graph paint).
	OnStart func(Result)
	// OnResult is called as each worker finishes (optional; for streaming / graph sinks).
	OnResult func(Result)
}

// Swarm runs a set of workers and returns merged-capable results.
type Swarm interface {
	Run(ctx context.Context, workers []Worker) ([]Result, error)
}

// GraphSink records fan-out events (implemented by *contextgraph.Store).
// Placed here alongside Swarm/Worker so all core swarm contracts are co-located.
type GraphSink interface {
	AppendTurn(turnID, workerID, role, model string, ok bool, summary string)
}

// GraphContext is the dual-layer context surface used by multi-wave runs.
// Implemented by *contextgraph.Store via dashboard adapters.
type GraphContext interface {
	Query(turnID, q string, limit int) string
	PathSummary(turnID, from, to string) string
	WaveOutputs(turnID string, waveIndex int, limit int) []string
	RecordThreadWave(turnID, threadID string, waveIndex int, mergedID, mergedSummary string, workers []WaveWorkerOut)
	RecordEpisodeFact(turnID, episodeID, label, summary string)
	RecordSubtasks(turnID, threadID string, tasks []SubtaskOut)
}

// WaveWorkerOut is a graph-write DTO (avoids contextgraph importing swarm).
type WaveWorkerOut struct {
	WorkerID string
	Role     string
	Model    string
	Summary  string
	OK       bool
}

// SubtaskOut is a planner-decomposed subtask fact for the entity layer.
type SubtaskOut struct {
	Index  int
	Prompt string
	Target string
	Model  string
}

// DefaultSwarm is the FanOut-backed Swarm used by the orchestrator.
type DefaultSwarm struct {
	Opts Options
}

var _ Swarm = (*DefaultSwarm)(nil)

// Run fans out workers with context cancel (see FanOut).
func (s *DefaultSwarm) Run(ctx context.Context, workers []Worker) ([]Result, error) {
	opts := Options{}
	if s != nil {
		opts = s.Opts
	}
	return FanOut(ctx, workers, opts)
}
