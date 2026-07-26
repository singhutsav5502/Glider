// Package procinfo resolves OS process identity — PID and executable name
// — from one end of a local TCP connection. Built to answer one specific
// question Glider's delegation feature needs: which real CLI process sent
// this request? Neither the Anthropic Messages API nor any other wire
// protocol Glider intercepts carries that information at all.
//
// A PEB-memory-read technique to also recover the process's current
// working directory was implemented and then removed (2026-07-26):
// confirmed live to fail under Windows Defender's default real-time
// cross-process memory-read protection — not a bug in the offsets (the
// PID/parent-PID fields in the very same struct read back correctly,
// proving the struct layout was right), but a fundamental obstacle on any
// machine with default AV/EDR active, which is effectively all of them.
// The actual design instead keys a small workspace directory registry by
// PID (see internal/vendors' WorkspaceStore) and asks the human once per
// session when no directory is known yet — PID/name resolution here is
// exactly what makes that key resolvable, without needing the blocked
// memory read at all.
//
// This is also the second consumer of exactly the PID/process-name lookup
// internal/mitm's transparent redirector already needed for its
// process-based traffic filtering — moved here so both consumers share one
// implementation instead of two copies drifting apart.
package procinfo

// ProcessInfo is what Resolver can recover about the process on the other
// end of a local TCP connection.
type ProcessInfo struct {
	PID  uint32
	Name string // executable basename, lowercased
}

// Resolver looks up the process owning one end of a local TCP connection,
// identified by that end's local port number (the port on THIS machine —
// for an inbound connection accepted by a local server, that's the port
// visible in the client's remote address as seen by the server, since
// loopback connections have both ends "local"; for an outbound packet
// intercepted mid-flight, it's the source port).
type Resolver interface {
	// Resolve looks up PID and process name for the given local port.
	// ok=false if no owning process could be found (stale/closed
	// connection, permission denied, or the platform isn't supported).
	Resolve(localPort uint16) (ProcessInfo, bool)
}

// NewResolver returns the platform's Resolver implementation — a working
// one on Windows, a stub that always reports ok=false elsewhere (see
// procinfo_other.go), matching internal/mitm's existing per-OS split for
// the exact same reason: this technique is inherently OS-specific.
func NewResolver() Resolver {
	return newPlatformResolver()
}
