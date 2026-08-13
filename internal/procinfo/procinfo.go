// Package procinfo finds the identity of a process in the operating system.
// That identity is its PID and the name of its executable file. The code
// finds it from one end of a local TCP connection.
//
// It answers one question that the delegation function of Glider needs: which
// true CLI process sent this request? The Messages API of Anthropic carries
// no such data, and each other wire protocol that Glider intercepts carries
// none.
//
// A person implemented a method to also find the current work directory of
// the process. That method read the memory of the PEB. A person removed it on
// 2026-07-26. A live test confirmed that it fails under the default
// protection of Windows Defender against a read of the memory of a different
// process.
//
// The offsets were correct. The PID field and the parent-PID field in the
// same structure gave correct values. That proves that the layout was
// correct. But this is a fundamental obstacle on each machine with an active
// AV or EDR system in its default state. That is effectively each machine.
//
// The design that a person built instead uses a small registry of workspace
// directories, with the PID as the key. Refer to WorkspaceStore in
// internal/vendors. It asks the person one time in each session, when it
// knows no directory. The lookup of the PID and the name here is exactly what
// makes that key resolvable, and it needs no read of the memory.
//
// The transparent redirector in internal/mitm already needed the lookup of
// the PID and of the process name, for its filter on the process. This
// package is the second user of exactly that lookup. A person moved the code
// here. Thus both users share one implementation, and two copies cannot
// become different.
package procinfo

// ProcessInfo is what Resolver can recover about the process on the other
// end of a local TCP connection.
type ProcessInfo struct {
	PID  uint32
	Name string // executable basename, lowercased
}

// Resolver finds the process that owns one end of a local TCP connection. The
// local port number of that end identifies it, and that port is on THIS
// machine. For an inbound connection that a local server accepted, that port
// is in the remote address of the client, as the server sees it. A loopback
// connection has both of its ends on this machine. For an outbound packet
// that the code intercepts, that port is the source port.
type Resolver interface {
	// Resolve looks up PID and process name for the given local port.
	// ok=false if no owning process could be found (stale/closed
	// connection, permission denied, or the platform is not supported).
	Resolve(localPort uint16) (ProcessInfo, bool)
}

// NewResolver returns the Resolver implementation for this platform. On
// Windows it returns one that operates. On each other platform it returns a
// version that always gives ok=false. Refer to procinfo_other.go. This agrees
// with the division by operating system that internal/mitm already has, and
// the cause is the same: this method belongs to one operating system.
func NewResolver() Resolver {
	return newPlatformResolver()
}
