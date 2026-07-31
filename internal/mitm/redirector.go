package mitm

import (
	"context"
	"net"
)

// TransparentRedirector owns exactly one OS-specific fact: how to make
// outbound connections to MatchPorts land on ListenPort instead of their
// real destination. It knows nothing about TLS, hosts, or Glider's routing
// — that's what makes it swappable per OS without touching mitmSession or
// blindTunnel. See planning/transparent_redirector_design.md §2.
type TransparentRedirector interface {
	Start(ctx context.Context, cfg RedirectConfig) error
	Stop() error
}

// RedirectConfig configures a TransparentRedirector.
type RedirectConfig struct {
	ListenPort int   // Glider's local transparent-ingress port
	MatchPorts []int // destination ports to intercept, e.g. []int{443}
	// AllowHosts scopes the packet filter to just these hostnames' resolved
	// IPs (exact hosts only — "*.domain" wildcards can't be resolved to a
	// concrete IP and are skipped for filter purposes, though they still
	// apply normally at the SNI/matchHostPattern layer for any traffic that
	// does get diverted). Same list as mitm.hosts — the point is to divert
	// only the vendor traffic Glider actually cares about, not every
	// outbound :443 connection on the machine.
	AllowHosts []string
	// AllowProcessNames further scopes interception to only connections
	// actually owned by one of these process image basenames (lowercased,
	// e.g. "claude.exe"), looked up per-connection via the OS's own TCP
	// connection table. Real gap this closes: AllowHosts alone isn't enough
	// when a vendor host sits behind a shared/anycast frontend IP (Google's
	// GFE serves many unrelated *.googleapis.com hosts off the same IPs a
	// configured vendor host resolves to) — confirmed live on 2026-07-26,
	// where Chrome's own background telemetry (optimizationguide-pa.
	// googleapis.com, jnn-pa.googleapis.com) got diverted anyway because it
	// happened to share an IP with an allowlisted googleapis.com host.
	// Empty → no process-based narrowing (IP/port scoping only).
	AllowProcessNames []string
}

// OriginResolver answers the one question a transparent path can't get for
// free the way a CONNECT-based path can (CONNECT states "host:443" in the
// request line itself). Given a connection that already landed on Glider's
// transparent listener, what destination IP:port was the client actually
// trying to reach? This is a raw IP, not a hostname — the hostname (for
// matchHostPattern) comes from the TLS ClientHello's SNI instead
// (peekClientHelloSNI), since packet-level interception never sees DNS.
type OriginResolver interface {
	ResolveOriginalDestination(conn net.Conn) (host string, port int, err error)
}

// ProcessFilter is an optional capability a TransparentRedirector can
// implement when AllowProcessNames enforcement can't happen at the OS's
// own packet-filtering layer, and has to happen instead on each already-
// accepted connection. WinDivertRedirector (Windows) doesn't implement
// this: it filters by owning process at the packet level, before a
// disallowed connection is ever redirected to Glider's listener at all,
// so handleTransparent never even sees it. A Linux implementation built
// on iptables REDIRECT has no equivalent primitive — Netfilter's REDIRECT
// target has no "match by owning process image name" condition, so every
// connection matching the IP/port criteria lands on Glider's listener
// regardless of which process opened it, and AllowProcessNames has to be
// enforced here, after accept, using the connection's local port to look
// up its owning process the same way the packet-level filter does.
type ProcessFilter interface {
	// ConnectionAllowed reports whether conn's owning process is allowed
	// to be MITM'd. false means handleTransparent must blind-tunnel to
	// the real destination unconditionally — the same "reinject
	// unchanged" outcome a rejected packet gets on Windows, just
	// implemented as a real dial+splice since a Linux REDIRECT can't be
	// un-redirected after the kernel already completed the handshake
	// against Glider's own listening socket.
	ConnectionAllowed(conn net.Conn) bool
}

// PIDScoper is an optional capability a TransparentRedirector can implement
// to further narrow interception to a specific, dynamically-enrolled set of
// process IDs — layered ON TOP of (not instead of) AllowProcessNames, and
// mutable at runtime (unlike AllowProcessNames, fixed for the process's
// whole lifetime via RedirectConfig). Added 2026-07-31 after a real, live
// incident: AllowProcessNames matches by process image name machine-wide
// ("claude.exe"), so a controlled test's own delegate subprocess and the
// operator's own unrelated, concurrently-running Claude Code session both
// matched the same name and both got their real traffic intercepted and
// flag-scanned — the operator's own live session was silently hijacked
// when a plain-English message happened to end in a recognized trailing
// "/vendorname" flag. PIDScoper lets a caller (the dashboard, or a test
// harness) explicitly enroll only the specific PID(s) it actually intends
// to test, leaving every other matching-by-name process on the machine
// untouched. Empty/nil enrollment (the default) preserves today's
// behavior unchanged — this is opt-in narrowing, not a new requirement,
// since Glider's core "just works, zero-cooperation" pitch for a real
// single front-CLI session depends on NOT requiring enrollment.
type PIDScoper interface {
	// SetEnrolledPIDs replaces the enrolled-PID set wholesale. Passing nil
	// or an empty slice disables PID narrowing entirely. Safe to call
	// concurrently with the redirector's own packet/connection handling.
	SetEnrolledPIDs(pids []uint32)
}
