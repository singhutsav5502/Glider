//go:build !windows && !linux

package mitm

import (
	"context"
	"fmt"
	"log/slog"
	"net"
)

// unsupportedRedirector is the stand-in for platforms with neither
// redirector_windows.go's nor redirector_linux.go's implementation
// (macOS today — see planning/transparent_redirector_design.md §6-7 for
// the pf-based design that would close this gap, not yet built; no
// verifiable macOS environment was available to build and test it
// against, and shipping unverified kernel-adjacent networking code isn't
// something this project does).
type unsupportedRedirector struct{}

// NewRedirector returns the platform's TransparentRedirector implementation.
// dllPath and log are Windows-only configuration, ignored here.
func NewRedirector(dllPath string, log *slog.Logger) TransparentRedirector {
	return &unsupportedRedirector{}
}

func (unsupportedRedirector) Start(ctx context.Context, cfg RedirectConfig) error {
	return fmt.Errorf("mitm: transparent OS-level redirection is not yet implemented on this OS")
}

func (unsupportedRedirector) Stop() error { return nil }

func (unsupportedRedirector) ResolveOriginalDestination(conn net.Conn) (string, int, error) {
	return "", 0, fmt.Errorf("mitm: transparent OS-level redirection is not yet implemented on this OS")
}
