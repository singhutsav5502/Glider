// Package contextkit holds session/episode/turn-budget stubs for swarm and loop
// orchestration. These types are the foundation for SessionState + Episode memory
// (see planning/context_and_swarm_architecture.md). No live wiring yet.
package contextkit

import (
	"sync"
	"time"
)

// Episode is a compressed worker/local-fulfill return (Slate-style), not a full transcript.
type Episode struct {
	ID        string    `json:"id"`
	Summary   string    `json:"summary"`
	Artifacts []string  `json:"artifacts,omitempty"`
	Tokens    int       `json:"tokens,omitempty"`
	Model     string    `json:"model,omitempty"`
	Rule      string    `json:"rule,omitempty"`
	Reason    string    `json:"reason,omitempty"`
	Role      string    `json:"role,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// LoopCheckpoint is a resume pointer for recurring /loop or CI babysit work.
type LoopCheckpoint struct {
	Goal          string `json:"goal"`
	LastEpisodeID string `json:"last_episode_id,omitempty"`
	EvalStatus    string `json:"eval_status,omitempty"` // pending | pass | fail | unknown
	WakeReason    string `json:"wake_reason,omitempty"`
	NextDelaySec  int    `json:"next_delay_s,omitempty"`
}

// TurnBudget soft/hard caps for a Cursor turn or Glider session window.
type TurnBudget struct {
	SoftTokens int     `json:"soft_tokens"`
	HardTokens int     `json:"hard_tokens"`
	SpentTokens int    `json:"spent_tokens"`
	SpentCostUSD float64 `json:"spent_cost_usd"`
}

// Remaining returns tokens left under the hard cap (0 if exhausted).
func (b TurnBudget) Remaining() int {
	if b.HardTokens <= 0 {
		return 0
	}
	left := b.HardTokens - b.SpentTokens
	if left < 0 {
		return 0
	}
	return left
}

// OverSoft reports whether soft budget is exceeded.
func (b TurnBudget) OverSoft() bool {
	return b.SoftTokens > 0 && b.SpentTokens >= b.SoftTokens
}

// SessionState carries decisions across turns for one Glider history session
// (or Cursor composer id when extractable).
type SessionState struct {
	SessionID       string            `json:"session_id"`
	ActiveOverrides map[string]string `json:"active_overrides,omitempty"` // last /local|/cloud flags
	LastRule        string            `json:"last_rule,omitempty"`
	LastReason      string            `json:"last_reason,omitempty"`
	LastTarget      string            `json:"last_target,omitempty"`
	Episodes        []Episode         `json:"episodes,omitempty"`
	Budget          TurnBudget        `json:"budget"`
	Loop            *LoopCheckpoint   `json:"loop_checkpoint,omitempty"`
	StickyScope     string            `json:"sticky_scope,omitempty"` // turn_family | none
}

const defaultEpisodeRing = 32

// Store is an in-memory SessionState ring (stub; not persisted).
type Store struct {
	mu       sync.Mutex
	sessions map[string]*SessionState
	ringN    int
}

// NewStore creates an in-memory session store.
func NewStore(episodeRing int) *Store {
	if episodeRing <= 0 {
		episodeRing = defaultEpisodeRing
	}
	return &Store{
		sessions: make(map[string]*SessionState),
		ringN:    episodeRing,
	}
}

// Get returns a copy of session state (creates empty if missing).
func (s *Store) Get(sessionID string) SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensure(sessionID)
	return cloneSession(st)
}

// RecordEpisode appends an episode and updates last decision fields.
func (s *Store) RecordEpisode(sessionID string, ep Episode) SessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensure(sessionID)
	if ep.CreatedAt.IsZero() {
		ep.CreatedAt = time.Now().UTC()
	}
	st.Episodes = append(st.Episodes, ep)
	if len(st.Episodes) > s.ringN {
		st.Episodes = st.Episodes[len(st.Episodes)-s.ringN:]
	}
	if ep.Rule != "" {
		st.LastRule = ep.Rule
	}
	if ep.Reason != "" {
		st.LastReason = ep.Reason
	}
	st.Budget.SpentTokens += ep.Tokens
	return cloneSession(st)
}

// SetStickyScope records turn-family vs none for future swarm/context policy.
func (s *Store) SetStickyScope(sessionID, scope string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensure(sessionID)
	st.StickyScope = scope
}

// SetBudget sets soft/hard turn budgets for a session.
func (s *Store) SetBudget(sessionID string, soft, hard int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ensure(sessionID)
	st.Budget.SoftTokens = soft
	st.Budget.HardTokens = hard
}

func (s *Store) ensure(sessionID string) *SessionState {
	if sessionID == "" {
		sessionID = "default"
	}
	st, ok := s.sessions[sessionID]
	if !ok {
		st = &SessionState{
			SessionID:       sessionID,
			ActiveOverrides: map[string]string{},
			Episodes:        nil,
		}
		s.sessions[sessionID] = st
	}
	return st
}

func cloneSession(st *SessionState) SessionState {
	out := *st
	if st.ActiveOverrides != nil {
		out.ActiveOverrides = make(map[string]string, len(st.ActiveOverrides))
		for k, v := range st.ActiveOverrides {
			out.ActiveOverrides[k] = v
		}
	}
	if st.Episodes != nil {
		out.Episodes = append([]Episode(nil), st.Episodes...)
	}
	if st.Loop != nil {
		cp := *st.Loop
		out.Loop = &cp
	}
	return out
}
