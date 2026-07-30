//go:build !windows && !linux

package procinfo

func newPlatformResolver() Resolver { return otherResolver{} }

// otherResolver is the stub for platforms with neither procinfo_windows.go
// nor procinfo_linux.go's technique implemented (macOS today) — every
// call here just reports "not found" rather than guessing.
type otherResolver struct{}

func (otherResolver) Resolve(localPort uint16) (ProcessInfo, bool) { return ProcessInfo{}, false }
