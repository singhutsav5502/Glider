//go:build !windows

package procinfo

func newPlatformResolver() Resolver { return otherResolver{} }

// otherResolver is the non-Windows stub — the PID/local-port lookup
// procinfo_windows.go uses (GetExtendedTcpTable) is Windows-specific;
// there's no equivalent implemented here yet. Linux has a much simpler
// path available (/proc/net/tcp for the port->inode mapping,
// /proc/<pid>/fd for inode->PID) if this package is ever needed on that
// platform, but that's unbuilt — every call here just reports "not found"
// rather than guessing.
type otherResolver struct{}

func (otherResolver) Resolve(localPort uint16) (ProcessInfo, bool) { return ProcessInfo{}, false }
