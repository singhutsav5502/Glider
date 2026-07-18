// Package contextgraph is the MVP orchestrator context layer: an append-only
// event log plus an ephemeral in-memory turn index used for sticky routing and
// debug/analytics. See planning/context_management.md.
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
	EventStickyBound        EventKind = "StickyBound"
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
	ID             string    `json:"id"`
	RootRequestID  string    `json:"root_request_id,omitempty"`
	Route          string    `json:"route,omitempty"` // cloud | local
	Source         string    `json:"source,omitempty"`
	OpenedAt       time.Time `json:"opened_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	RequestIDs     []string  `json:"request_ids,omitempty"`
	ConnectSessions []string `json:"connect_sessions,omitempty"`
	OpenRuns       int       `json:"open_runs,omitempty"`
	Events         []Event   `json:"events,omitempty"`
	Stats          *TurnStats `json:"stats,omitempty"`
}

// StoreStats is a process-wide summary for /api/context/recent.
type StoreStats struct {
	Turns       int            `json:"turns"`
	Events      int            `json:"events"`
	CloudTurns  int            `json:"cloud_turns"`
	LocalTurns  int            `json:"local_turns"`
	OpenRuns    int            `json:"open_runs"`
	ByKind      map[string]int `json:"by_kind,omitempty"`
	Sessions    int            `json:"sessions"`
}

// Store is the MVP: append-only events + turn index. Optional JSONL persistence
// under Dir (typically ~/.glider/context).
type Store struct {
	mu        sync.Mutex
	events    []Event
	turns     map[string]*turnIndex // turnID → index
	byReq     map[string]string     // requestID → turnID
	bySession map[string]string     // connect_session → turnID (latest)
	Dir       string                // empty → memory only
	Max       int                   // ring cap for events (0 → 4096)
	Grace     time.Duration         // cloud sticky grace after last activity
}

type turnIndex struct {
	view   TurnView
	evIdx  []int
	opens  int // RunSSEOpen − RunSSEClose
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
	return &Store{
		turns:     make(map[string]*turnIndex),
		byReq:     make(map[string]string),
		bySession: make(map[string]string),
		Dir:       dir,
		Max:       4096,
		Grace:     DefaultCloudGrace,
	}
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
	if len(s.events) >= s.Max {
		cut := len(s.events) / 2
		s.events = append([]Event(nil), s.events[cut:]...)
		s.rebuildLocked()
	}
	idx := len(s.events)
	s.events = append(s.events, ev)
	s.indexLocked(idx, &s.events[idx])
	s.persistLocked(s.events[idx])
}

func (s *Store) rebuildLocked() {
	s.turns = make(map[string]*turnIndex)
	s.byReq = make(map[string]string)
	s.bySession = make(map[string]string)
	for i := range s.events {
		s.indexLocked(i, &s.events[i])
	}
}

func (s *Store) indexLocked(idx int, ev *Event) {
	turnID := strings.TrimSpace(ev.TurnID)
	reqID := strings.TrimSpace(ev.RequestID)
	sess := strings.TrimSpace(ev.ConnectSession)

	if turnID == "" && reqID != "" {
		if t, ok := s.byReq[reqID]; ok {
			turnID = t
			ev.TurnID = t
		} else if sess != "" {
			if t, ok := s.bySession[sess]; ok {
				turnID = t
				ev.TurnID = t
			}
		}
		if turnID == "" {
			turnID = reqID
			ev.TurnID = reqID
		}
	}
	if turnID == "" && sess != "" {
		if t, ok := s.bySession[sess]; ok {
			turnID = t
			ev.TurnID = t
		} else {
			turnID = "sess:" + sess
			ev.TurnID = turnID
		}
	}
	if turnID == "" {
		return
	}

	ti, ok := s.turns[turnID]
	if !ok {
		ti = &turnIndex{view: TurnView{ID: turnID, OpenedAt: ev.TS, RootRequestID: turnID}}
		s.turns[turnID] = ti
	}
	ti.evIdx = append(ti.evIdx, idx)
	ti.view.UpdatedAt = ev.TS

	if reqID != "" {
		s.byReq[reqID] = turnID
		if !containsStr(ti.view.RequestIDs, reqID) {
			ti.view.RequestIDs = append(ti.view.RequestIDs, reqID)
		}
	}
	if sess != "" {
		s.bySession[sess] = turnID
		if !containsStr(ti.view.ConnectSessions, sess) {
			ti.view.ConnectSessions = append(ti.view.ConnectSessions, sess)
		}
	}

	switch ev.Kind {
	case EventTurnOpened, EventStickyBound, EventRouteDecided:
		if r := attr(ev, "route"); r != "" {
			ti.view.Route = r
		}
		if src := attr(ev, "source"); src != "" {
			ti.view.Source = src
		}
		if root := attr(ev, "root_request_id"); root != "" {
			ti.view.RootRequestID = root
		}
	case EventRunSSEOpen: // also EventParentRunStarted (alias)
		ti.opens++
		ti.view.OpenRuns = ti.opens
	case EventRunSSEClose: // also EventParentRunEnded (alias)
		if ti.opens > 0 {
			ti.opens--
		}
		ti.view.OpenRuns = ti.opens
	}
}

func attr(ev *Event, key string) string {
	if ev == nil || ev.Attrs == nil {
		return ""
	}
	return ev.Attrs[key]
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
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

func (s *Store) cloudLiveLocked(ti *turnIndex, now time.Time) bool {
	if ti == nil || !strings.EqualFold(ti.view.Route, "cloud") {
		return false
	}
	if ti.opens > 0 {
		return true
	}
	return now.Sub(ti.view.UpdatedAt) <= s.graceLocked()
}

func (s *Store) turnStatsLocked(ti *turnIndex) TurnStats {
	st := TurnStats{
		EventCount:   len(ti.evIdx),
		ByKind:       make(map[string]int),
		OpenRuns:     ti.opens,
		RequestCount: len(ti.view.RequestIDs),
		CloudLive:    s.cloudLiveLocked(ti, time.Now()),
	}
	for _, i := range ti.evIdx {
		if i >= 0 && i < len(s.events) {
			st.ByKind[string(s.events[i].Kind)]++
		}
	}
	return st
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
	turnID := id
	if t, ok := s.byReq[id]; ok {
		turnID = t
	} else if t, ok := s.bySession[id]; ok {
		turnID = t
	} else if strings.HasPrefix(id, "cs:") || strings.HasPrefix(id, "xs:") {
		if t, ok := s.bySession[id]; ok {
			turnID = t
		}
	}
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
	st := s.turnStatsLocked(ti)
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
	return s.cloudLiveLocked(ti, time.Now())
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
	var best *turnIndex
	for _, ti := range s.turns {
		if !s.cloudLiveLocked(ti, now) {
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
		if !found || !s.cloudLiveLocked(ti, now) {
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
		if !s.cloudLiveLocked(ti, now) {
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
	for _, ti := range s.turns {
		v := ti.view
		v.Events = nil
		v.OpenRuns = ti.opens
		st := TurnStats{
			EventCount:   len(ti.evIdx),
			OpenRuns:     ti.opens,
			RequestCount: len(ti.view.RequestIDs),
			CloudLive:    s.cloudLiveLocked(ti, now),
			ByKind:       make(map[string]int),
		}
		for _, i := range ti.evIdx {
			if i >= 0 && i < len(s.events) {
				st.ByKind[string(s.events[i].Kind)]++
			}
		}
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
		Turns:  len(s.turns),
		Events: len(s.events),
		ByKind: make(map[string]int),
	}
	st.Sessions = len(s.bySession)
	now := time.Now()
	for _, ti := range s.turns {
		st.OpenRuns += ti.opens
		switch strings.ToLower(ti.view.Route) {
		case "cloud":
			st.CloudTurns++
			_ = now
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
