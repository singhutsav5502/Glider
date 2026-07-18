package metrics

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Session is a Glider process run. Requests are grouped under SessionID.
// Client correlation IDs (if present on a request) are stored on the event but
// do not redefine the session — the process run is the analytics unit.
type Session struct {
	ID           string    `json:"id"`
	StartedAt    time.Time `json:"started_at"`
	EndedAt      time.Time `json:"ended_at,omitempty"`
	RequestCount int       `json:"request_count"`
	LocalCount   int       `json:"local_count"`
	CloudCount   int       `json:"cloud_count"`
	CannedCount  int       `json:"canned_count,omitempty"`
	TokenTotal   int       `json:"token_total"`
	LatencySumMs float64   `json:"latency_sum_ms"`
	Current      bool      `json:"current,omitempty"`
}

// StoredRequest is one persisted request event.
type StoredRequest struct {
	SessionID     string    `json:"session_id"`
	ClientSession string    `json:"client_session,omitempty"`
	ID            string    `json:"id"`
	Ts            time.Time `json:"ts"`
	Mode          string    `json:"mode,omitempty"`
	Action        string    `json:"action,omitempty"`
	Route         string    `json:"route"`
	Model         string    `json:"model"`
	OriginalModel string    `json:"original_model,omitempty"`
	Host          string    `json:"host,omitempty"`
	Path          string    `json:"path,omitempty"`
	Rule          string    `json:"rule,omitempty"`
	Tokens        int       `json:"tokens"`
	LatencyMs     float64   `json:"latency_ms"`
}

// HistoryStore persists session-grouped request history under a data directory
// (default ~/.glider/history) using JSONL per session + an index file.
type HistoryStore struct {
	mu        sync.Mutex
	dir       string
	sessionID string
	startedAt time.Time
	indexPath string
	logPath   string
	logFile   *os.File
	session   Session
}

// DefaultHistoryDir returns ~/.glider/history (or %USERPROFILE%\.glider\history on Windows).
func DefaultHistoryDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".glider", "history")
	}
	return filepath.Join(home, ".glider", "history")
}

// OpenHistoryStore creates/opens a store and starts a new session.
func OpenHistoryStore(dir, sessionID string) (*HistoryStore, error) {
	if dir == "" {
		dir = DefaultHistoryDir()
	}
	if sessionID == "" {
		sessionID = fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	s := &HistoryStore{
		dir:       dir,
		sessionID: sessionID,
		startedAt: time.Now().UTC(),
		indexPath: filepath.Join(dir, "sessions.json"),
		logPath:   filepath.Join(dir, sessionID+".jsonl"),
		session: Session{
			ID:        sessionID,
			StartedAt: time.Now().UTC(),
			Current:   true,
		},
	}
	f, err := os.OpenFile(s.logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	s.logFile = f
	if err := s.upsertIndexLocked(); err != nil {
		_ = f.Close()
		return nil, err
	}
	return s, nil
}

func (s *HistoryStore) SessionID() string {
	if s == nil {
		return ""
	}
	return s.sessionID
}

func (s *HistoryStore) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session.EndedAt = time.Now().UTC()
	s.session.Current = false
	_ = s.upsertIndexLocked()
	if s.logFile != nil {
		err := s.logFile.Close()
		s.logFile = nil
		return err
	}
	return nil
}

// Record appends a request to the current session log and updates aggregates.
func (s *HistoryStore) Record(rec StoredRequest) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.Ts.IsZero() {
		rec.Ts = time.Now().UTC()
	}
	rec.SessionID = s.sessionID
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := s.logFile.Write(append(data, '\n')); err != nil {
		return err
	}
	s.session.RequestCount++
	s.session.TokenTotal += rec.Tokens
	s.session.LatencySumMs += rec.LatencyMs
	switch {
	case rec.Action == "canned":
		s.session.CannedCount++
	case rec.Action == "origin_passthrough" || rec.Route == "cloud" || rec.Action == "cloud":
		s.session.CloudCount++
	case rec.Route == "local" || rec.Action == "local":
		s.session.LocalCount++
	case rec.Action == "error":
		// errors excluded from LOCAL/CLOUD %
	default:
		if rec.Route != "" {
			s.session.CloudCount++
		}
	}
	// Persist index periodically (every request is fine at dashboard scale).
	return s.upsertIndexLocked()
}

func (s *HistoryStore) upsertIndexLocked() error {
	sessions, _ := s.readIndexUnlocked()
	found := false
	for i := range sessions {
		sessions[i].Current = sessions[i].ID == s.sessionID
		if sessions[i].ID == s.sessionID {
			snap := s.session
			snap.Current = true
			sessions[i] = snap
			found = true
		}
	}
	if !found {
		snap := s.session
		snap.Current = true
		sessions = append(sessions, snap)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartedAt.After(sessions[j].StartedAt)
	})
	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.indexPath, data, 0o644)
}

func (s *HistoryStore) readIndexUnlocked() ([]Session, error) {
	data, err := os.ReadFile(s.indexPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var sessions []Session
	if err := json.Unmarshal(data, &sessions); err != nil {
		return nil, err
	}
	return sessions, nil
}

// ListSessions returns known sessions (newest first).
func (s *HistoryStore) ListSessions() ([]Session, error) {
	if s == nil {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sessions, err := s.readIndexUnlocked()
	if err != nil {
		return nil, err
	}
	for i := range sessions {
		sessions[i].Current = sessions[i].ID == s.sessionID
	}
	return sessions, nil
}

// GetSession returns one session summary.
func (s *HistoryStore) GetSession(id string) (Session, error) {
	sessions, err := s.ListSessions()
	if err != nil {
		return Session{}, err
	}
	for _, sess := range sessions {
		if sess.ID == id {
			return sess, nil
		}
	}
	return Session{}, fmt.Errorf("session %q not found", id)
}

// ListRequests returns requests for a session (newest first, limited).
func (s *HistoryStore) ListRequests(sessionID string, limit int) ([]StoredRequest, error) {
	if s == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 500
	}
	path := filepath.Join(s.dir, sessionID+".jsonl")
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []StoredRequest{}, nil
		}
		return nil, err
	}
	defer f.Close()

	var all []StoredRequest
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec StoredRequest
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		all = append(all, rec)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	// Newest first
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	if len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

// SessionAggregates is a convenience summary for the dashboard.
type SessionAggregates struct {
	Session      Session        `json:"session"`
	AvgLatencyMs float64        `json:"avg_latency_ms"`
	RouteCounts  map[string]int `json:"route_counts"`
	ActionCounts map[string]int `json:"action_counts"`
	Distribution Distribution   `json:"distribution"`
}

// Aggregates computes per-session stats from the JSONL log.
func (s *HistoryStore) Aggregates(sessionID string) (SessionAggregates, error) {
	sess, err := s.GetSession(sessionID)
	if err != nil {
		return SessionAggregates{}, err
	}
	reqs, err := s.ListRequests(sessionID, 10000)
	if err != nil {
		return SessionAggregates{}, err
	}
	out := SessionAggregates{
		Session:      sess,
		RouteCounts:  map[string]int{},
		ActionCounts: map[string]int{},
	}
	var latSum float64
	for _, r := range reqs {
		out.RouteCounts[r.Route]++
		action := r.Action
		if action == "" {
			action = r.Route
		}
		if action != "" {
			out.ActionCounts[action]++
		}
		latSum += r.LatencyMs
	}
	out.Distribution = ComputeDistribution(out.ActionCounts)
	if n := len(reqs); n > 0 {
		out.AvgLatencyMs = latSum / float64(n)
	} else if sess.RequestCount > 0 {
		out.AvgLatencyMs = sess.LatencySumMs / float64(sess.RequestCount)
	}
	return out, nil
}
