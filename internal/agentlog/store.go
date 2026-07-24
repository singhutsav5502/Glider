// Package agentlog stores per-instance agent activity logs.
// Each hoop id and each swarm run id has an independent ring buffer
// (not one global mixed timeline).
package agentlog

import (
	"sync"
	"time"
)

// Scope distinguishes hoop vs swarm logging namespaces.
type Scope string

const (
	ScopeHoop  Scope = "hoop"
	ScopeSwarm Scope = "swarm"
)

// Entry is one log line for a single agent instance.
type Entry struct {
	// Seq is a store-wide monotonic id assigned on Append (stable upsert key for UIs).
	Seq        uint64            `json:"seq"`
	At         time.Time         `json:"at"`
	Scope      Scope             `json:"scope"`
	InstanceID string            `json:"instance_id"`
	Level      string            `json:"level,omitempty"` // info|warn|error
	Kind       string            `json:"kind,omitempty"`  // stage_start|stage_end|route|eval|worker|error|lifecycle
	Message    string            `json:"message"`
	Attrs      map[string]string `json:"attrs,omitempty"`
}

// Store holds independent ring buffers keyed by scope+instance id.
type Store struct {
	mu       sync.RWMutex
	rings    map[string]*ring
	cap      int
	seq      uint64
	onAppend func(Entry) // optional fan-out (e.g. metrics.Bus)
}

type ring struct {
	buf  []Entry
	head int
	n    int
}

// NewStore creates a per-instance log store. capacity is entries per instance (default 256).
func NewStore(capacity int) *Store {
	if capacity <= 0 {
		capacity = 256
	}
	return &Store{
		rings: make(map[string]*ring),
		cap:   capacity,
	}
}

// OnAppend registers a callback invoked after each Append (e.g. publish to WebSocket bus).
func (s *Store) OnAppend(fn func(Entry)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onAppend = fn
	s.mu.Unlock()
}

func key(scope Scope, id string) string {
	return string(scope) + ":" + id
}

// Reset clears and recreates the ring for an instance (fresh timeline on start).
func (s *Store) Reset(scope Scope, id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	s.rings[key(scope, id)] = &ring{buf: make([]Entry, s.cap)}
	s.mu.Unlock()
	s.Append(Entry{
		Scope:      scope,
		InstanceID: id,
		Level:      "info",
		Kind:       "lifecycle",
		Message:    "log timeline started",
	})
}

// Append adds an entry to the instance ring (creates ring if missing).
func (s *Store) Append(e Entry) {
	if s == nil || e.InstanceID == "" {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	if e.Level == "" {
		e.Level = "info"
	}
	if e.Scope == "" {
		e.Scope = ScopeHoop
	}
	s.mu.Lock()
	s.seq++
	e.Seq = s.seq
	k := key(e.Scope, e.InstanceID)
	r := s.rings[k]
	if r == nil {
		r = &ring{buf: make([]Entry, s.cap)}
		s.rings[k] = r
	}
	r.buf[r.head] = e
	r.head = (r.head + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
	cb := s.onAppend
	s.mu.Unlock()
	if cb != nil {
		cb(e)
	}
}

// Recent returns up to limit newest entries for one instance (oldest→newest).
func (s *Store) Recent(scope Scope, id string, limit int) []Entry {
	if s == nil || id == "" {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.rings[key(scope, id)]
	if r == nil || r.n == 0 {
		return nil
	}
	out := make([]Entry, 0, r.n)
	start := (r.head - r.n + len(r.buf)) % len(r.buf)
	for i := 0; i < r.n; i++ {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

// After returns up to limit entries with Seq > afterSeq for one instance (oldest→newest).
// Used by poll cursors so clients can fetch only new lines since the last seen seq.
func (s *Store) After(scope Scope, id string, afterSeq uint64, limit int) []Entry {
	if s == nil || id == "" {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.rings[key(scope, id)]
	if r == nil || r.n == 0 {
		return nil
	}
	out := make([]Entry, 0, min(limit, r.n))
	start := (r.head - r.n + len(r.buf)) % len(r.buf)
	for i := 0; i < r.n; i++ {
		e := r.buf[(start+i)%len(r.buf)]
		if e.Seq <= afterSeq {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ListInstances returns known instance ids for a scope.
func (s *Store) ListInstances(scope Scope) []string {
	if s == nil {
		return nil
	}
	prefix := string(scope) + ":"
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for k := range s.rings {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			out = append(out, k[len(prefix):])
		}
	}
	return out
}

// Clear removes the ring for one instance (no lifecycle seed line).
func (s *Store) Clear(scope Scope, id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	delete(s.rings, key(scope, id))
	s.mu.Unlock()
}

// ClearScope removes all rings for a scope.
func (s *Store) ClearScope(scope Scope) {
	if s == nil {
		return
	}
	prefix := string(scope) + ":"
	s.mu.Lock()
	for k := range s.rings {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(s.rings, k)
		}
	}
	s.mu.Unlock()
}

// ClearAll removes every ring (all scopes).
func (s *Store) ClearAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.rings = make(map[string]*ring)
	s.mu.Unlock()
}

// Info is a convenience Append with level info.
func (s *Store) Info(scope Scope, id, kind, msg string, attrs map[string]string) {
	s.Append(Entry{Scope: scope, InstanceID: id, Level: "info", Kind: kind, Message: msg, Attrs: attrs})
}

// Error is a convenience Append with level error.
func (s *Store) Error(scope Scope, id, kind, msg string, attrs map[string]string) {
	s.Append(Entry{Scope: scope, InstanceID: id, Level: "error", Kind: kind, Message: msg, Attrs: attrs})
}
