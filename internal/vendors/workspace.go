package vendors

import (
	"strings"
	"sync"
)

// WorkspaceStore remembers which real filesystem directory a given origin
// process's delegated calls should be scoped to.
//
// Why this exists (planning/permission_relay_design.md §10): {{cwd}}
// substitution otherwise falls back to Glider's own server process
// directory — confirmed live 2026-07-26 to be wrong (a resumed delegate
// call read files from Glider's own repo instead of the user's actual
// project). A PID-based automatic resolution (reading the origin
// process's own working directory out of its PEB) was tried and removed:
// confirmed live to be blocked by Windows Defender's default real-time
// cross-process memory-read protection, on a real, representative
// machine, not a fixable implementation bug.
//
// The design settled on instead (explicit direction, 2026-07-26): PID
// resolution IS reliable when it's just "which process, what name" (see
// internal/procinfo, no memory read involved) — so PID becomes a stable
// KEY for a small directory registry, and Glider asks the human once per
// origin process ("I don't know your directory yet, reply with
// <path> /workspace") rather than guessing or reading memory. A
// dashboard-configured default covers the common single-project case
// without ever having to ask.
type WorkspaceStore struct {
	mu         sync.Mutex
	byPID      map[uint32]string
	defaultDir string
}

// NewWorkspaceStore returns an empty store — used directly by production
// code via the package-level defaultWorkspaceStore, and separately by
// tests that want isolation from that shared instance.
func NewWorkspaceStore() *WorkspaceStore {
	return &WorkspaceStore{byPID: map[uint32]string{}}
}

// Lookup returns the directory to use for pid: a per-PID override if one
// was set, else the configured default, else ok=false — the caller must
// ask the human rather than guess.
func (s *WorkspaceStore) Lookup(pid uint32) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir, ok := s.byPID[pid]; ok {
		return dir, true
	}
	if s.defaultDir != "" {
		return s.defaultDir, true
	}
	return "", false
}

// Set records the directory for one specific origin process — "once per
// session" in practice, since PIDs get reused after a process exits and
// this store has no PID-reuse detection of its own (a stale entry for a
// long-dead PID could, in principle, get reused by an unrelated later
// process; low-probability and not distinguishable from here — accepted,
// not silently pretended away).
func (s *WorkspaceStore) Set(pid uint32, dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byPID[pid] = dir
}

// SetDefault configures the fallback directory used for any origin PID
// with no specific entry — the dashboard-configured "default workspace."
func (s *WorkspaceStore) SetDefault(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.defaultDir = dir
}

// Default returns the currently configured default, "" if none.
func (s *WorkspaceStore) Default() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.defaultDir
}

// defaultWorkspaceStore is the process-wide store production code uses —
// deliberately package-level (like defaultResumeStore in resume.go) rather
// than threaded through every call site, since every HTTP-facing caller
// needs the same shared state.
var defaultWorkspaceStore = NewWorkspaceStore()

// SetWorkspaceForPID records the real working directory for one origin
// process — called when a human answers Glider's "I don't know your
// directory yet" question (see ParseWorkspaceCommand).
func SetWorkspaceForPID(pid uint32, dir string) {
	defaultWorkspaceStore.Set(pid, dir)
}

// SetDefaultWorkspace configures the fallback directory used for any
// origin PID with no specific entry yet — the dashboard's "default
// workspace" setting.
func SetDefaultWorkspace(dir string) {
	defaultWorkspaceStore.SetDefault(dir)
}

// DefaultWorkspace returns the currently configured default, "" if none —
// for the dashboard to display the current setting.
func DefaultWorkspace() string {
	return defaultWorkspaceStore.Default()
}

// ParseWorkspaceCommand looks for a trailing "/workspace" flag — same
// trailing-only convention as ParseDelegateCommand and for the same
// reason: a leading "/" gets read as a local slash command by whatever
// client the human is typing into, so the flag has to be the last thing
// in the message, not the first. Everything before it, trimmed, is the
// path. Deliberately NOT vendor-scoped like "/vendorname" — the workspace
// directory is a property of the ORIGIN process, orthogonal to which
// vendor a later delegate call targets, so there is exactly one
// "/workspace" flag, not one per vendor.
func ParseWorkspaceCommand(userText string) (path string, ok bool) {
	trimmed := strings.TrimRight(userText, " \t\r\n")
	const flag = "/workspace"
	if !strings.HasSuffix(trimmed, flag) {
		return "", false
	}
	start := len(trimmed) - len(flag)
	if !trailingFlagBoundaryOK(trimmed, start) {
		return "", false
	}
	path = strings.TrimSpace(trimmed[:start])
	if path == "" {
		return "", false
	}
	return path, true
}
