package vendors

import (
	"strings"
	"sync"
)

// WorkspaceStore keeps the true directory in the file system that the
// delegated calls of one origin process must use.
//
// The cause for this. Refer to planning/permission_relay_design.md §10. If
// this store does not give a directory, the value for {{cwd}} becomes the
// directory of the server process of Glider. A live test on 2026-07-26
// confirmed that this is incorrect. A delegate call that resumed read files
// from the repository of Glider. It did not read files from the true project
// of the user.
//
// A person tried an automatic method that used the PID: it read the work
// directory of the origin process out of its PEB. A person then removed that
// method. Windows Defender protects the memory of a process against a read
// from a different process, and it does this by default. A live test on a
// true machine confirmed that this protection stops that method. That is not
// a defect in the implementation, and no person can correct it.
//
// A person selected a different design, with explicit direction on
// 2026-07-26. A lookup by PID IS dependable when it gives only "which
// process, and what name". Refer to internal/procinfo, which reads no memory.
//
// Therefore the PID becomes a stable KEY for a small registry of directories.
// Glider asks the person one time for each origin process: "I do not know
// your directory yet, reply with <path> /workspace". It does not estimate the
// directory, and it does not read memory. A default that a person sets on the
// dashboard covers the usual condition of one project, and then Glider asks
// nothing.
type WorkspaceStore struct {
	mu         sync.Mutex
	byPID      map[uint32]string
	defaultDir string
}

// NewWorkspaceStore returns an empty store. The code in the product uses it
// through the package-level defaultWorkspaceStore. A test uses it separately,
// when that test needs isolation from the store that each caller shares.
func NewWorkspaceStore() *WorkspaceStore {
	return &WorkspaceStore{byPID: map[uint32]string{}}
}

// Lookup returns the directory that a person set for this one PID. It returns
// ok=false when no person set one, and the caller must then ask.
//
// It does NOT use the configured default, and this is on purpose.
//
// Before 2026-08-03, the default was a silent answer for each PID with no
// entry. That is correct while a person works on one project. It becomes
// incorrect the moment that a person opens a second CLI in a second project:
// the first handoff of that new session went to the directory of the FIRST
// project, and Glider asked nothing. A delegate could then change the files of
// the incorrect project.
//
// A better default cannot prevent that condition, because Glider cannot know
// which project a new session belongs to. procinfo gives the PID and the name
// of the image, and it gives no path. The read of the memory of the PEB, which
// would give a path, does not operate. Refer to the comment on this package.
//
// Therefore each new session gets the question. Default() gives the value that
// the question offers, thus a person can accept it in one reply.
func (s *WorkspaceStore) Lookup(pid uint32) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, ok := s.byPID[pid]
	return dir, ok
}

// Set records the directory for one origin process. In practice this occurs
// one time for each session.
//
// An operating system uses a PID again after a process exits, and this store
// has no detection of that condition. Therefore an old entry for a process that
// stopped a long time before can, in theory, go to a different process later.
// The probability is low, and this code cannot see the difference. This comment
// states the condition, and it does not hide it.
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

// defaultWorkspaceStore is the store for the full process, and the code in the
// product uses it. It is at the level of the package, as defaultResumeStore in
// resume.go is. This is on purpose, and the code does not give it to each call
// site. Each caller that faces HTTP needs the same shared state.
var defaultWorkspaceStore = NewWorkspaceStore()

// SetWorkspaceForPID records the true work directory for one origin process.
// Code calls it when a person answers the question of Glider, "I do not know
// your directory yet". Refer to ParseWorkspaceCommand.
func SetWorkspaceForPID(pid uint32, dir string) {
	defaultWorkspaceStore.Set(pid, dir)
}

// WorkspaceForPID is the package-level read counterpart of SetWorkspaceForPID
// — resolves an origin process to its working directory, falling back to the
// configured default (see WorkspaceStore.Lookup). ok=false means that the
// code knows no directory. A caller must treat that as "do not estimate". The
// server directory of Glider is a confirmed incorrect alternative, from
// 2026-07-26. It is not only imprecise.
func WorkspaceForPID(pid uint32) (string, bool) {
	return defaultWorkspaceStore.Lookup(pid)
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

// ParseWorkspaceCommand looks for a "/workspace" flag at the end. This is the
// same convention as ParseDelegateCommand, and the cause is the same. The
// client that the person types in reads a "/" at the start as its own local
// command. Therefore the flag must be the last item in the message, and not
// the first. Everything before it, trimmed, is the path. This flag has no
// vendor, and "/vendorname" does have one. That is on purpose. The workspace
// directory belongs to the ORIGIN process. It has no relation to the vendor
// that a later delegate call uses. Therefore there is exactly one
// "/workspace" flag, and not one for each vendor.
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
