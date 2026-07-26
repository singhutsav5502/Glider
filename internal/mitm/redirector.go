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
