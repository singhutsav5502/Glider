package loop

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/glider-ai/glider/internal/agentlog"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/contextkit"
	"github.com/glider-ai/glider/internal/tools"
	"github.com/google/uuid"
)

// Completer is the shared harness surface used by each stage in a cycle.
// PipelineCompleter satisfies this (Complete / CompleteLocal).
type Completer interface {
	Complete(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error)
	CompleteLocal(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error)
}

// RunnerConfig configures the hoop Manager.
type RunnerConfig struct {
	Hoop         HoopLearningConfig
	DefaultRoute RoutePref // used when spec.Route empty; default local
	OutcomeRing  int
}

// Manager owns CRUD + start/stop for Loop Engineering hoops.
type Manager struct {
	mu       sync.Mutex
	Store    *Store
	Complete Completer
	Graph    *contextgraph.Store
	Cfg      RunnerConfig
	Episodes *contextkit.Store // optional episode ring
	// Logs is optional per-hoop agent activity (independent ring per hoop id).
	Logs *agentlog.Store
	// Tools is the unified builtin/MCP/plugin registry for node tool refs.
	Tools *tools.Registry
	// BudgetCheck optional FinOps hook; nil → always OK.
	BudgetCheck func(*LoopState) bool

	runners map[string]*runnerHandle
}

type runnerHandle struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewManager wires persistence + harness.
func NewManager(store *Store, completer Completer, graph *contextgraph.Store, cfg RunnerConfig) *Manager {
	if store == nil {
		store = NewStore("")
	}
	if cfg.DefaultRoute == "" {
		cfg.DefaultRoute = RouteLocal
	}
	return &Manager{
		Store:    store,
		Complete: completer,
		Graph:    graph,
		Cfg:      cfg,
		runners:  make(map[string]*runnerHandle),
	}
}

// Create validates and persists a new idle hoop.
func (m *Manager) Create(spec LoopSpec) (*LoopState, error) {
	if err := spec.Normalize(); err != nil {
		return nil, err
	}
	if spec.ID == "" {
		spec.ID = "hoop-" + uuid.New().String()[:8]
	}
	if existing, err := m.Store.Get(spec.ID); err == nil && existing != nil {
		return nil, fmt.Errorf("hoop %q already exists", spec.ID)
	}
	if spec.Route == "" {
		spec.Route = m.Cfg.DefaultRoute
	}
	if len(spec.Stages) == 0 {
		spec.Stages = DefaultModules(spec.Goal)
	}
	st := CreateState(spec)
	if err := m.Store.Save(st); err != nil {
		return nil, err
	}
	return st, nil
}

// Update replaces the spec of an idle/stopped hoop (not while running).
func (m *Manager) Update(id string, spec LoopSpec) (*LoopState, error) {
	m.mu.Lock()
	_, running := m.runners[id]
	m.mu.Unlock()
	if running {
		return nil, fmt.Errorf("hoop %q is running; stop first", id)
	}
	st, err := m.Store.Get(id)
	if err != nil {
		return nil, err
	}
	spec.ID = id
	if err := spec.Normalize(); err != nil {
		return nil, err
	}
	spec.CreatedAt = st.Spec.CreatedAt
	if len(spec.Stages) == 0 {
		spec.Stages = DefaultModules(spec.Goal)
	}
	st.Spec = spec
	if err := m.Store.Save(st); err != nil {
		return nil, err
	}
	return st, nil
}

// Delete stops (if running) and removes state.
func (m *Manager) Delete(id string) error {
	_ = m.Stop(id)
	return m.Store.Delete(id)
}

// Get returns current state (refreshed from disk).
func (m *Manager) Get(id string) (*LoopState, error) {
	return m.Store.Get(id)
}

// List returns all hoops.
func (m *Manager) List() ([]LoopState, error) {
	return m.Store.List()
}

// Start begins engineering cycles until stop, eval pass, max iterations, or fail policy.
func (m *Manager) Start(parent context.Context, id string) (*LoopState, error) {
	if m.Complete == nil {
		return nil, fmt.Errorf("no completer configured")
	}
	st, err := m.Store.Get(id)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	if _, ok := m.runners[id]; ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("hoop %q already running", id)
	}
	// Detach from HTTP request context — otherwise the handler return cancels the hoop.
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	h := &runnerHandle{cancel: cancel, done: make(chan struct{})}
	m.runners[id] = h
	m.mu.Unlock()

	now := time.Now().UTC()
	st.Status = StatusRunning
	st.StartedAt = &now
	st.StoppedAt = nil
	st.LastError = ""
	if st.Checkpoint.WakeReason != "resume" {
		st.Checkpoint.WakeReason = "start"
	}
	if err := m.Store.Save(st); err != nil {
		cancel()
		m.mu.Lock()
		delete(m.runners, id)
		m.mu.Unlock()
		return nil, err
	}
	// Fresh independent log timeline for this hoop instance (keep history on HITL resume).
	if m.Logs != nil {
		if st.Checkpoint.WakeReason != "resume" {
			m.Logs.Reset(agentlog.ScopeHoop, id)
		}
		m.Logs.Info(agentlog.ScopeHoop, id, "lifecycle", "hoop started", map[string]string{
			"route": string(st.Spec.Route),
			"goal":  truncate(st.Spec.Goal, 80),
			"wake":  st.Checkpoint.WakeReason,
		})
	}
	m.emit(st, contextgraph.EventLoopStarted, map[string]string{
		"loop_id": id,
		"route":   string(st.Spec.Route),
		"kind":    "engineering",
	})

	go m.runLoop(ctx, h, id)
	return st, nil
}

// Stop cancels a running hoop.
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	h, ok := m.runners[id]
	if ok {
		delete(m.runners, id)
	}
	m.mu.Unlock()
	if !ok {
		st, err := m.Store.Get(id)
		if err != nil {
			return err
		}
		if st.Status == StatusRunning {
			now := time.Now().UTC()
			st.Status = StatusStopped
			st.StoppedAt = &now
			_ = m.Store.Save(st)
		}
		return nil
	}
	h.cancel()
	<-h.done
	st, err := m.Store.Get(id)
	if err != nil {
		return err
	}
	if st.Status == StatusRunning {
		now := time.Now().UTC()
		st.Status = StatusStopped
		st.StoppedAt = &now
		st.Checkpoint.WakeReason = "stop"
		_ = m.Store.Save(st)
		m.emit(st, contextgraph.EventLoopStopped, map[string]string{"loop_id": id, "reason": "stop"})
	}
	return nil
}

// Shutdown stops all runners (process exit).
func (m *Manager) Shutdown() {
	m.mu.Lock()
	ids := make([]string, 0, len(m.runners))
	for id := range m.runners {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.Stop(id)
	}
}

func (m *Manager) runLoop(ctx context.Context, h *runnerHandle, id string) {
	defer close(h.done)
	defer func() {
		m.mu.Lock()
		delete(m.runners, id)
		m.mu.Unlock()
	}()

	for {
		st, err := m.Store.Get(id)
		if err != nil {
			return
		}
		if st.Status != StatusRunning {
			return
		}

		_, stopReason, err := m.runCycle(ctx, st)
		if err != nil && ctx.Err() != nil {
			now := time.Now().UTC()
			st.Status = StatusStopped
			st.StoppedAt = &now
			st.Checkpoint.WakeReason = "cancel"
			_ = m.Store.Save(st)
			m.emit(st, contextgraph.EventLoopStopped, map[string]string{"loop_id": id, "reason": "cancel"})
			return
		}

		if stopReason != "" {
			now := time.Now().UTC()
			st.StoppedAt = &now
			switch stopReason {
			case "failed", "on_fail_n", "max_latency", "budget_exceeded":
				st.Status = StatusFailed
			case "human_gate", "waiting_human":
				st.Status = StatusWaitingHuman
				st.StoppedAt = nil // still "live" for HITL resume
			default:
				st.Status = StatusCompleted
			}
			st.Checkpoint.WakeReason = stopReason
			_ = m.Store.Save(st)
			m.emit(st, contextgraph.EventLoopStopped, map[string]string{
				"loop_id": id,
				"reason":  stopReason,
			})
			return
		}
		_ = m.Store.Save(st)

		// Optional Automations heartbeat between cycles.
		delay := 50 * time.Millisecond
		if st.Spec.Interval != "" || st.Spec.Cron != "" {
			sched, err := parseSchedule(st.Spec.Cron, st.Spec.Interval)
			if err != nil {
				now := time.Now().UTC()
				st.Status = StatusFailed
				st.StoppedAt = &now
				st.LastError = err.Error()
				_ = m.Store.Save(st)
				return
			}
			delay = sched.nextDelay(time.Now())
		}
		if st.Checkpoint.NextDelaySec > 0 {
			delay = time.Duration(st.Checkpoint.NextDelaySec) * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			now := time.Now().UTC()
			st.Status = StatusStopped
			st.StoppedAt = &now
			st.Checkpoint.WakeReason = "cancel"
			_ = m.Store.Save(st)
			m.emit(st, contextgraph.EventLoopStopped, map[string]string{"loop_id": id, "reason": "cancel"})
			return
		case <-timer.C:
		}
	}
}

func (m *Manager) evaluateStop(st *LoopState, o IterationOutcome, text string) string {
	sc := st.Spec.Stop
	okN, failN := st.ConsecutiveOK, st.ConsecutiveFail
	// Counters already updated in AppendOutcome; MaxLatency flips Success before append.
	if sc.OnSuccessN > 0 && okN >= sc.OnSuccessN {
		return "on_success_n"
	}
	if sc.OnFailN > 0 && failN >= sc.OnFailN {
		return "on_fail_n"
	}
	if sc.MaxLatencyMS > 0 && o.LatencyMS > int64(sc.MaxLatencyMS) && sc.OnFailN <= 0 {
		// Hard stop when latency exceeded and no OnFailN window configured.
		return "max_latency"
	}
	lower := strings.ToLower(text)
	for _, c := range sc.Contains {
		c = strings.TrimSpace(c)
		if c != "" && strings.Contains(lower, strings.ToLower(c)) {
			return "contains"
		}
	}
	return ""
}

func (m *Manager) emit(st *LoopState, kind contextgraph.EventKind, attrs map[string]string) {
	if m.Graph == nil || st == nil {
		return
	}
	m.Graph.Append(contextgraph.Event{
		Kind:      kind,
		TurnID:    "loop:" + st.Spec.ID,
		RequestID: fmt.Sprintf("loop-%s-%d", st.Spec.ID, st.Iteration),
		Actor:     "loop",
		Attrs:     attrs,
	})
}

// GateDecision is the HITL approve/reject payload.
type GateDecision struct {
	Approve bool   `json:"approve"`
	Comment string `json:"comment,omitempty"`
	Actor   string `json:"actor,omitempty"`
	Resume  bool   `json:"resume,omitempty"` // if approve+resume, continue cycles
}

// DecideGate records an operator decision on a waiting_human hoop.
func (m *Manager) DecideGate(id string, d GateDecision) (*LoopState, error) {
	st, err := m.Store.Get(id)
	if err != nil {
		return nil, err
	}
	if st.Status != StatusWaitingHuman && !st.Gate.Active {
		return nil, fmt.Errorf("hoop %q is not waiting for human (status=%s)", id, st.Status)
	}
	now := time.Now().UTC()
	st.Gate.Comment = strings.TrimSpace(d.Comment)
	st.Gate.Actor = strings.TrimSpace(d.Actor)
	if st.Gate.Actor == "" {
		st.Gate.Actor = "operator"
	}
	st.Gate.DecidedAt = now
	if d.Approve {
		st.Gate.Decision = "approve"
		st.Gate.Active = false
		st.Checkpoint.EvalStatus = "human_approved"
		st.Progress.Note = "approved"
		if m.Logs != nil {
			m.Logs.Info(agentlog.ScopeHoop, id, "hitl", "approved: "+truncate(st.Gate.Comment, 80), map[string]string{
				"actor": st.Gate.Actor,
			})
		}
	} else {
		st.Gate.Decision = "reject"
		st.Gate.Active = false
		st.Status = StatusFailed
		st.StoppedAt = &now
		st.Checkpoint.EvalStatus = "human_rejected"
		st.Checkpoint.WakeReason = "human_reject"
		st.LastError = "rejected by human"
		if st.Gate.Comment != "" {
			st.LastError += ": " + st.Gate.Comment
		}
		st.Progress.Phase = "idle"
		st.Progress.Note = "rejected"
		if m.Logs != nil {
			m.Logs.Info(agentlog.ScopeHoop, id, "hitl", "rejected: "+truncate(st.Gate.Comment, 80), map[string]string{
				"actor": st.Gate.Actor,
			})
		}
		_ = m.Store.Save(st)
		return st, nil
	}
	st.UpdatedAt = now
	if err := m.Store.Save(st); err != nil {
		return nil, err
	}
	if d.Resume {
		return m.Resume(context.Background(), id)
	}
	// Approved but not resumed: idle ready for Start/Resume.
	st.Status = StatusStopped
	st.Progress.Phase = "idle"
	_ = m.Store.Save(st)
	return st, nil
}

// Resume continues a hoop after HITL approve (or from stopped with prior approval).
func (m *Manager) Resume(parent context.Context, id string) (*LoopState, error) {
	st, err := m.Store.Get(id)
	if err != nil {
		return nil, err
	}
	if st.Gate.Decision == "reject" {
		return nil, fmt.Errorf("hoop %q was rejected; create/start a new run", id)
	}
	// Clear gate wait; Start will set running.
	st.Gate.Active = false
	if st.Gate.Decision == "" && st.Status == StatusWaitingHuman {
		// Resume without explicit approve is allowed for operator override.
		st.Gate.Decision = "approve"
		st.Gate.DecidedAt = time.Now().UTC()
		st.Gate.Actor = "resume"
	}
	st.Status = StatusStopped
	st.Checkpoint.WakeReason = "resume"
	_ = m.Store.Save(st)
	if m.Logs != nil {
		m.Logs.Info(agentlog.ScopeHoop, id, "hitl", "resuming after human gate", nil)
	}
	return m.Start(parent, id)
}

func truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
