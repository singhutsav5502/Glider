package contextgraph

import (
	"strings"
	"time"
)

// EventLog is the append-only event ring plus turn / request / session indexes.
// It is not safe for concurrent use without external synchronization — Store
// holds the mutex and embeds EventLog as a facade component (SOLID SRP split).
type EventLog struct {
	events    []Event
	turns     map[string]*turnIndex // turnID → index
	byReq     map[string]string     // requestID → turnID
	bySession map[string]string     // connect_session → turnID (latest)
}

type turnIndex struct {
	view  TurnView
	evIdx []int
	opens int // RunSSEOpen − RunSSEClose
}

func (l *EventLog) ensure() {
	if l.turns == nil {
		l.turns = make(map[string]*turnIndex)
	}
	if l.byReq == nil {
		l.byReq = make(map[string]string)
	}
	if l.bySession == nil {
		l.bySession = make(map[string]string)
	}
}

// rebuild reindexes all events into turn/request/session maps (ring trim).
func (l *EventLog) rebuild() {
	l.turns = make(map[string]*turnIndex)
	l.byReq = make(map[string]string)
	l.bySession = make(map[string]string)
	for i := range l.events {
		l.index(i, &l.events[i])
	}
}

// trimHalf drops the oldest half of the ring and rebuilds indexes. Returns cut count.
func (l *EventLog) trimHalf(max int) int {
	if max <= 0 {
		max = 4096
	}
	if len(l.events) < max {
		return 0
	}
	cut := len(l.events) / 2
	l.events = append([]Event(nil), l.events[cut:]...)
	l.rebuild()
	return cut
}

// appendEvent appends one event (caller supplies defensive Attrs copy + TS).
// Returns the index of the new event. Trims the ring when at capacity.
func (l *EventLog) appendEvent(ev Event, max int) int {
	l.ensure()
	if max <= 0 {
		max = 4096
	}
	if len(l.events) >= max {
		l.trimHalf(max)
	}
	idx := len(l.events)
	l.events = append(l.events, ev)
	l.index(idx, &l.events[idx])
	return idx
}

func (l *EventLog) index(idx int, ev *Event) {
	l.ensure()
	turnID := strings.TrimSpace(ev.TurnID)
	reqID := strings.TrimSpace(ev.RequestID)
	sess := strings.TrimSpace(ev.ConnectSession)

	if turnID == "" && reqID != "" {
		if t, ok := l.byReq[reqID]; ok {
			turnID = t
			ev.TurnID = t
		} else if sess != "" {
			if t, ok := l.bySession[sess]; ok {
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
		if t, ok := l.bySession[sess]; ok {
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

	ti, ok := l.turns[turnID]
	if !ok {
		ti = &turnIndex{view: TurnView{ID: turnID, OpenedAt: ev.TS, RootRequestID: turnID}}
		l.turns[turnID] = ti
	}
	ti.evIdx = append(ti.evIdx, idx)
	ti.view.UpdatedAt = ev.TS

	if reqID != "" {
		l.byReq[reqID] = turnID
		if !containsStr(ti.view.RequestIDs, reqID) {
			ti.view.RequestIDs = append(ti.view.RequestIDs, reqID)
		}
	}
	if sess != "" {
		l.bySession[sess] = turnID
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

func (l *EventLog) cloudLive(ti *turnIndex, now time.Time, grace time.Duration) bool {
	if ti == nil || !strings.EqualFold(ti.view.Route, "cloud") {
		return false
	}
	if ti.opens > 0 {
		return true
	}
	if grace <= 0 {
		grace = DefaultCloudGrace
	}
	return now.Sub(ti.view.UpdatedAt) <= grace
}

func (l *EventLog) turnStats(ti *turnIndex, now time.Time, grace time.Duration) TurnStats {
	st := TurnStats{
		EventCount:   len(ti.evIdx),
		ByKind:       make(map[string]int),
		OpenRuns:     ti.opens,
		RequestCount: len(ti.view.RequestIDs),
		CloudLive:    l.cloudLive(ti, now, grace),
	}
	for _, i := range ti.evIdx {
		if i >= 0 && i < len(l.events) {
			st.ByKind[string(l.events[i].Kind)]++
		}
	}
	return st
}

func (l *EventLog) resolveTurnID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if t, ok := l.byReq[id]; ok {
		return t
	}
	if t, ok := l.bySession[id]; ok {
		return t
	}
	if strings.HasPrefix(id, "cs:") || strings.HasPrefix(id, "xs:") {
		if t, ok := l.bySession[id]; ok {
			return t
		}
	}
	return id
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
