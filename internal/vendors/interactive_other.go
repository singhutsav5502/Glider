//go:build !windows

package vendors

import "fmt"

// launchInteractive has no implementation outside Windows yet — same
// scope boundary as internal/mitm's transparent redirector and
// internal/tray (see their respective _other.go stubs).
func launchInteractive(v Vendor, cwd string, extraArgs ...string) error {
	return fmt.Errorf("vendors: interactive launch is not implemented on this platform yet")
}
