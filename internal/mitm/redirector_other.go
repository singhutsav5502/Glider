//go:build !windows

package mitm

import (
	"context"
	"fmt"
	"log/slog"
	"net"
)

// unsupportedRedirector is the non-Windows stand-in until a Linux/macOS
// implementation lands (see planning/transparent_redirector_design.md §6-7 —
// iptables/nftables REDIRECT + SO_ORIGINAL_DST is designed, not yet built).
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
