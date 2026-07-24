// Package contextgraph is the orchestrator context layer: an append-only event
// log plus a structural entity/edge store (Graphify-inspired dual-layer Query)
// and an ephemeral turn index for sticky routing. Persist under ~/.glider/context.
// See planning/context_management.md and planning/slate_weave_graphify_plan.md.
package contextgraph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// EventKind is an append-only log verb.
type EventKind string

const (
	EventRouteDecided      EventKind = "RouteDecided"
	EventStickyBound       EventKind = "StickyBound"
	EventFulfilledLocal    EventKind = "FulfilledLocal"
	EventOriginPassthrough EventKind = "OriginPassthrough"
	EventToolStarted       EventKind = "ToolStarted"
	EventToolFinished      EventKind = "ToolFinished"
	EventSummaryRequested  EventKind = "SummaryRequested"
	EventSubagentSpawned   EventKind = "SubagentSpawned"
	EventRunSSEOpen        EventKind = "RunSSEOpen"
	EventRunSSEClose       EventKind = "RunSSEClose"
	EventBidiSeen          EventKind = "BidiSeen"
	EventError             EventKind = "Error"
	EventTurnOpened        EventKind = "TurnOpened"
	EventLoopStarted       EventKind = "LoopStarted"
	EventLoopTick          EventKind = "LoopTick"
	EventLoopStopped       EventKind = "LoopStopped"
	EventSwarmFanOut       EventKind = "SwarmFanOut"
	EventEpisodeMerged     EventKind = "EpisodeMerged"

	// Deprecated aliases kept for callers/tests written against earlier names.
	EventParentRunStarted EventKind = EventRunSSEOpen
	EventParentRunEnded   EventKind = EventRunSSEClose
)

// DefaultCloudGrace is how long a cloud turn stays sticky after last activity
// when no parent RunSSE is open (chrome wrap-up window).
const DefaultCloudGrace = 90 * time.Second

// Event is one immutable log line.
type Event struct {
	TS             time.Time         `json:"ts"`
	Kind           EventKind         `json:"kind"`
	TurnID         string            `json:"turn_id,omitempty"`
	RequestID      string            `json:"request_id,omitempty"`
	ConnectSession string            `json:"connect_session,omitempty"`
	Actor          string            `json:"actor,omitempty"` // cloud | local | mitm | orch
	Attrs          map[string]string `json:"attrs,omitempty"`
}

// TurnStats summarizes events for one turn family.
type TurnStats struct {
	EventCount   int            `json:"event_count"`
	ByKind       map[string]int `json:"by_kind,omitempty"`
	OpenRuns     int            `json:"open_runs"`
	RequestCount int            `json:"request_count"`
	CloudLive    bool           `json:"cloud_live"`
}

// TurnView is a hot in-memory projection of one turn family.
type TurnView struct {
	ID              string     `json:"id"`
	RootRequestID   string     `json:"root_request_id,omitempty"`
	Route           string     `json:"route,omitempty"` // cloud | local
	Source          string     `json:"source,omitempty"`
	OpenedAt        time.Time  `json:"opened_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	RequestIDs      []string   `json:"request_ids,omitempty"`
	ConnectSessions []string   `json:"connect_sessions,omitempty"`
	OpenRuns        int        `json:"open_runs,omitempty"`
	Events          []Event    `json:"events,omitempty"`
	Stats           *TurnStats `json:"stats,omitempty"`
}

// StoreStats is a process-wide summary for /api/context/recent.
type StoreStats struct {
	Turns      int            `json:"turns"`
	Events     int            `json:"events"`
	Entities   int            `json:"entities"`
	CloudTurns int            `json:"cloud_turns"`
	LocalTurns int            `json:"local_turns"`
	OpenRuns   int            `json:"open_runs"`
	ByKind     map[string]int `json:"by_kind,omitempty"`
	Sessions   int            `json:"sessions"`
}

// Store is the public facade over EventLog (append-only events + turn index) and
// EntityIndex (structural entity/edge layer). Callers keep using *Store; internals
// are split for SRP (see planning/solid_refactor.md). Optional JSONL under Dir.
type Store struct {
	mu sync.Mutex
	EventLog
	EntityIndex
	Dir   string        // empty → memory only
	Max   int           // ring cap for events (0 → 4096)
	Grace time.Duration // cloud sticky grace after last activity
}

// DefaultDir returns ~/.glider/context (or %USERPROFILE%\.glider\context).
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".glider", "context")
	}
	return filepath.Join(home, ".glider", "context")
}

// New constructs an in-memory store. Dir may be empty.
func New(dir string) *Store {
	s := &Store{
		Dir:   dir,
		Max:   4096,
		Grace: DefaultCloudGrace,
	}
	s.EventLog.ensure()
	s.EntityIndex.ensure()
	return s
}

var defaultStore = New("")

// Default returns the process-wide store (MITM wires into this unless overridden).
func Default() *Store { return defaultStore }

// SetDefault replaces the process-wide store (tests / main).
func SetDefault(s *Store) {
	if s != nil {
		defaultStore = s
	}
}

// Append records an event and updates the turn index.
func (s *Store) Append(ev Event) {
	if s == nil {
		return
	}
	if ev.TS.IsZero() {
		ev.TS = time.Now().UTC()
	}
	if ev.Attrs != nil {
		// Defensive copy so callers can reuse maps.
		cp := make(map[string]string, len(ev.Attrs))
		for k, v := range ev.Attrs {
			cp[k] = v
		}
		ev.Attrs = cp
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Max <= 0 {
		s.Max = 4096
	}
	idx := s.EventLog.appendEvent(ev, s.Max)
	s.persistLocked(s.events[idx])
}

func (s *Store) persistLocked(ev Event) {
	if strings.TrimSpace(s.Dir) == "" {
		return
	}
	_ = os.MkdirAll(s.Dir, 0o755)
	day := ev.TS.Format("2006-01-02")
	path := filepath.Join(s.Dir, "events-"+day+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	_ = enc.Encode(ev)
}

func (s *Store) graceLocked() time.Duration {
	if s.Grace <= 0 {
		return DefaultCloudGrace
	}
	return s.Grace
}

// Turn returns a snapshot for turnID (or requestID / session keyed into a turn).
func (s *Store) Turn(id string) (TurnView, bool) {
	if s == nil {
		return TurnView{}, false
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return TurnView{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	turnID := s.EventLog.resolveTurnID(id)
	ti, ok := s.turns[turnID]
	if !ok {
		return TurnView{}, false
	}
	out := ti.view
	out.OpenRuns = ti.opens
	out.Events = make([]Event, 0, len(ti.evIdx))
	for _, i := range ti.evIdx {
		if i >= 0 && i < len(s.events) {
			out.Events = append(out.Events, s.events[i])
		}
	}
	st := s.EventLog.turnStats(ti, time.Now(), s.graceLocked())
	out.Stats = &st
	return out, true
}

// TurnIDForRequest resolves a request UUID to its turn family id.
func (s *Store) TurnIDForRequest(requestID string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byReq[strings.TrimSpace(requestID)]
}

// TurnIDForSession resolves a connect/x-session key to its latest turn family id.
func (s *Store) TurnIDForSession(sessionKey string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bySession[strings.TrimSpace(sessionKey)]
}

// SessionLastRoute returns the route ("cloud"|"local"|"") of the latest turn bound
// to sessionKey, even when cloud grace has expired (wrap-up inheritance signal).
func (s *Store) SessionLastRoute(sessionKey string) string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tid := s.bySession[strings.TrimSpace(sessionKey)]
	if tid == "" {
		return ""
	}
	ti, ok := s.turns[tid]
	if !ok || ti == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(ti.view.Route))
}

// CloudTurnLive reports whether requestID (or its turn) is bound to a live cloud route.
func (s *Store) CloudTurnLive(requestID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := strings.TrimSpace(requestID)
	turnID := id
	if t, ok := s.byReq[id]; ok {
		turnID = t
	}
	ti, ok := s.turns[turnID]
	if !ok {
		return false
	}
	return s.EventLog.cloudLive(ti, time.Now(), s.graceLocked())
}

// LiveCloudFamily returns the newest live cloud turn (open parent run or within grace).
// Used when the TTL sticky map expired but the graph still says /cloud is in flight.
func (s *Store) LiveCloudFamily() (turnID, source, root string, ok bool) {
	if s == nil {
		return "", "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	grace := s.graceLocked()
	var best *turnIndex
	for _, ti := range s.turns {
		if !s.EventLog.cloudLive(ti, now, grace) {
			continue
		}
		if best == nil || ti.view.UpdatedAt.After(best.view.UpdatedAt) {
			best = ti
		}
	}
	if best == nil {
		return "", "", "", false
	}
	root = best.view.RootRequestID
	if root == "" {
		root = best.view.ID
	}
	return best.view.ID, best.view.Source, root, true
}

// ResolveCloudSticky answers whether a child/summary/subagent request should inherit
// cloud origin from the graph (not only the MITM TTL map).
func (s *Store) ResolveCloudSticky(requestID, sessionKey string) (turnID, source, root string, ok bool) {
	if s == nil {
		return "", "", "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	grace := s.graceLocked()

	try := func(id string) bool {
		if id == "" {
			return false
		}
		tid := id
		if t, mapped := s.byReq[id]; mapped {
			tid = t
		} else if t, mapped := s.bySession[id]; mapped {
			tid = t
		}
		ti, found := s.turns[tid]
		if !found || !s.EventLog.cloudLive(ti, now, grace) {
			return false
		}
		turnID = ti.view.ID
		source = ti.view.Source
		root = ti.view.RootRequestID
		if root == "" {
			root = ti.view.ID
		}
		ok = true
		return true
	}

	if try(strings.TrimSpace(requestID)) {
		return turnID, source, root, true
	}
	if try(strings.TrimSpace(sessionKey)) {
		return turnID, source, root, true
	}
	// Fall back to newest live cloud family (summary/child with new UUID + empty session).
	var best *turnIndex
	for _, ti := range s.turns {
		if !s.EventLog.cloudLive(ti, now, grace) {
			continue
		}
		if best == nil || ti.view.UpdatedAt.After(best.view.UpdatedAt) {
			best = ti
		}
	}
	if best == nil {
		return "", "", "", false
	}
	root = best.view.RootRequestID
	if root == "" {
		root = best.view.ID
	}
	return best.view.ID, best.view.Source, root, true
}

// RouteTallies returns coarse local/cloud counts from turn views (optional
// analytics companion to metrics.Distribution — not a substitute for request-log %).
func (s *Store) RouteTallies() map[string]int {
	if s == nil {
		return map[string]int{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for _, ti := range s.turns {
		r := strings.ToLower(strings.TrimSpace(ti.view.Route))
		switch r {
		case "local", "cloud":
			out[r]++
		}
	}
	return out
}

// RecentTurns returns up to limit newest turns (by UpdatedAt) with stats.
func (s *Store) RecentTurns(limit int) []TurnView {
	if s == nil {
		return nil
	}
	if limit <= 0 {
		limit = 20
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]TurnView, 0, len(s.turns))
	now := time.Now()
	grace := s.graceLocked()
	for _, ti := range s.turns {
		v := ti.view
		v.Events = nil
		v.OpenRuns = ti.opens
		st := s.EventLog.turnStats(ti, now, grace)
		v.Stats = &st
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// RecentEvents returns up to limit newest events (newest first).
func (s *Store) RecentEvents(limit int) []Event {
	if s == nil {
		return nil
	}
	if limit <= 0 {
		limit = 50
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.events)
	if n == 0 {
		return nil
	}
	if limit > n {
		limit = n
	}
	out := make([]Event, limit)
	for i := 0; i < limit; i++ {
		out[i] = s.events[n-1-i]
	}
	return out
}

// Stats returns aggregate counters for the dashboard.
func (s *Store) Stats() StoreStats {
	if s == nil {
		return StoreStats{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st := StoreStats{
		Turns:    len(s.turns),
		Events:   len(s.events),
		Entities: s.EntityIndex.len(),
		ByKind:   make(map[string]int),
	}
	st.Sessions = len(s.bySession)
	now := time.Now()
	_ = now
	for _, ti := range s.turns {
		st.OpenRuns += ti.opens
		switch strings.ToLower(ti.view.Route) {
		case "cloud":
			st.CloudTurns++
		case "local":
			st.LocalTurns++
		}
	}
	for _, ev := range s.events {
		st.ByKind[string(ev.Kind)]++
	}
	return st
}

// BindRequest links a child/summary requestUUID into an existing turn.
func (s *Store) BindRequest(turnID, requestID string) {
	if s == nil || turnID == "" || requestID == "" {
		return
	}
	s.Append(Event{
		Kind:      EventStickyBound,
		TurnID:    turnID,
		RequestID: requestID,
		Actor:     "mitm",
		Attrs:     map[string]string{"edge": "sticky_inherits"},
	})
}

// BindSession links a connect/x-session key into a turn family.
func (s *Store) BindSession(turnID, sessionKey string) {
	if s == nil || turnID == "" || sessionKey == "" {
		return
	}
	s.Append(Event{
		Kind:           EventStickyBound,
		TurnID:         turnID,
		ConnectSession: sessionKey,
		Actor:          "mitm",
		Attrs:          map[string]string{"edge": "session_binds"},
	})
}

// RecordRoute is a convenience for orchestrator/MITM route chips.
func (s *Store) RecordRoute(turnID, requestID, route, source, actor string, attrs map[string]string) {
	if s == nil {
		return
	}
	if attrs == nil {
		attrs = map[string]string{}
	}
	if route != "" {
		attrs["route"] = route
	}
	if source != "" {
		attrs["source"] = source
	}
	if turnID == "" {
		turnID = requestID
	}
	s.Append(Event{
		Kind:      EventRouteDecided,
		TurnID:    turnID,
		RequestID: requestID,
		Actor:     actor,
		Attrs:     attrs,
	})
}

// RecordEpisodeMerged emits EpisodeMerged onto the turn family (fulfill / fan-out / loop).
func (s *Store) RecordEpisodeMerged(turnID, requestID, episodeID string, attrs map[string]string) {
	if s == nil {
		return
	}
	if attrs == nil {
		attrs = map[string]string{}
	}
	if episodeID != "" {
		attrs["episode_id"] = episodeID
	}
	if turnID == "" {
		turnID = requestID
	}
	s.Append(Event{
		Kind:      EventEpisodeMerged,
		TurnID:    turnID,
		RequestID: requestID,
		Actor:     "orch",
		Attrs:     attrs,
	})
}

// Export returns a JSON-serializable dump of recent events (+ optional turn filter).
func (s *Store) Export(turnID string, maxEvents int) map[string]any {
	if s == nil {
		return map[string]any{"events": []Event{}, "turns": []TurnView{}}
	}
	if maxEvents <= 0 {
		maxEvents = 500
	}
	out := map[string]any{}
	if turnID != "" {
		if v, ok := s.Turn(turnID); ok {
			out["turn"] = v
			out["events"] = v.Events
			return out
		}
		out["turn"] = nil
		out["events"] = []Event{}
		return out
	}
	out["stats"] = s.Stats()
	out["turns"] = s.RecentTurns(50)
	out["events"] = s.RecentEvents(maxEvents)
	return out
}

// LoadWarm replays JSONL files from Dir for the last retainDays (inclusive of today).
// Returns the number of events loaded. No-op when Dir is empty.
func (s *Store) LoadWarm(retainDays int) (int, error) {
	if s == nil || strings.TrimSpace(s.Dir) == "" {
		return 0, nil
	}
	if retainDays <= 0 {
		retainDays = 2
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -(retainDays - 1))
	cutoffDay := cutoff.Format("2006-01-02")
	loaded := 0
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "events-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, "events-"), ".jsonl")
		if day < cutoffDay {
			continue
		}
		path := filepath.Join(s.Dir, name)
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		dec := json.NewDecoder(f)
		for {
			var ev Event
			if err := dec.Decode(&ev); err != nil {
				break
			}
			if s.Max <= 0 {
				s.Max = 4096
			}
			if len(s.events) >= s.Max {
				s.EventLog.trimHalf(s.Max)
			}
			idx := len(s.events)
			s.events = append(s.events, ev)
			s.EventLog.index(idx, &s.events[idx])
			loaded++
		}
		_ = f.Close()
	}
	return loaded, nil
}

// PruneDisk deletes events-*.jsonl older than retainDays. Returns files removed.
func (s *Store) PruneDisk(retainDays int) (int, error) {
	if s == nil || strings.TrimSpace(s.Dir) == "" {
		return 0, nil
	}
	if retainDays <= 0 {
		retainDays = 14
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -(retainDays - 1))
	cutoffDay := cutoff.Format("2006-01-02")
	removed := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, "events-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(name, "events-"), ".jsonl")
		if day >= cutoffDay {
			continue
		}
		if err := os.Remove(filepath.Join(s.Dir, name)); err == nil {
			removed++
		}
	}
	return removed, nil
}

// PruneMemory drops the oldest half of the in-memory ring when over Max (same as Append pressure).
func (s *Store) PruneMemory() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Max <= 0 {
		s.Max = 4096
	}
	return s.EventLog.trimHalf(s.Max)
}
