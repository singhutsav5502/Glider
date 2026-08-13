package mitm

import (
	"context"
	"net"
)

// TransparentRedirector owns exactly one OS-specific fact: how to make
// outbound connections to MatchPorts land on ListenPort instead of their
// real destination. It knows nothing about TLS, hosts, or Glider's routing
// — that is what makes it swappable per OS without touching mitmSession or
// blindTunnel. See planning/transparent_redirector_design.md §2.
type TransparentRedirector interface {
	Start(ctx context.Context, cfg RedirectConfig) error
	Stop() error
}

// RedirectConfig configures a TransparentRedirector.
type RedirectConfig struct {
	ListenPort int   // Glider's local transparent-ingress port
	MatchPorts []int // destination ports to intercept, e.g. []int{443}
	// AllowHosts limits the packet filter to the IP addresses of these host
	// names. It uses exact hosts only. The code cannot resolve a wildcard such as
	// "*.domain" to one IP address, therefore it does not use such a pattern for
	// the filter. But that pattern still operates in the usual way at the level of
	// the SNI and matchHostPattern, for each connection that Glider does divert.
	//
	// This is the same list as mitm.hosts. The objective is to divert only the
	// vendor traffic that Glider needs, and not each outbound connection to port
	// 443 on the machine.
	AllowHosts []string
	// AllowProcessNames makes the interception more narrow. Glider then
	// intercepts only the connections that one of these processes owns. Give the
	// base name of the process image, in small letters, for example "claude.exe".
	// The code finds the owner in the table of TCP connections of the operating
	// system, for each connection.
	//
	// This closes a true gap. AllowHosts alone is not sufficient when a vendor
	// host is behind a front-end IP address that many hosts share. The GFE of
	// Google serves many different *.googleapis.com hosts from the same IP
	// addresses that a configured vendor host resolves to.
	//
	// A live test confirmed this on 2026-07-26. Chrome sends its own data in the
	// background to optimizationguide-pa.googleapis.com and jnn-pa.googleapis.com.
	// Glider diverted that traffic, because it used an IP address that an
	// allowlisted googleapis.com host also used.
	//
	// An empty value gives no limit by process, and only the limit by IP address
	// and port.
	AllowProcessNames []string
}

// OriginResolver answers the one question that a transparent path cannot
// answer with no work. A path with CONNECT gets the answer with no work,
// because the request line itself says "host:443".
//
// The question: a connection already arrived at the transparent listener of
// Glider, so which destination IP address and port did the client try to
// reach?
//
// The answer is a raw IP address, and it is not a host name. The host name,
// for matchHostPattern, comes from the SNI field of the TLS ClientHello
// instead, with peekClientHelloSNI. Interception at the level of packets never
// sees DNS.
type OriginResolver interface {
	ResolveOriginalDestination(conn net.Conn) (host string, port int, err error)
}

// ProcessFilter is an optional function that a TransparentRedirector can
// implement. It is necessary when the packet filter of the operating system
// cannot apply AllowProcessNames. The code must then apply it to each
// connection that it already accepted.
//
// WinDivertRedirector, on Windows, does not implement this. It filters by the
// process that owns the connection at the level of packets. Therefore a
// connection that the rules do not permit never arrives at the listener of
// Glider, and handleTransparent never sees it.
//
// An implementation on Linux that uses REDIRECT of iptables has no equal
// mechanism. The REDIRECT target of Netfilter has no condition that agrees
// with the name of the process image that owns a connection. Therefore each
// connection that agrees with the IP address and the port arrives at the
// listener of Glider, whatever process opened it. The code must apply
// AllowProcessNames here, after the accept. It uses the local port of the
// connection to find the process that owns it. That is the same method that
// the packet filter uses.
type ProcessFilter interface {
	// ConnectionAllowed says if the process that owns conn has permission for a
	// MITM operation.
	//
	// A false result means that handleTransparent must make a blind tunnel to
	// the true destination, with no test. That is the same result as "inject the
	// packet again with no change" for a packet that Windows refuses. But here
	// the code makes a true dial and a true splice. Linux cannot remove a
	// REDIRECT after the kernel completed the handshake against the listening
	// socket of Glider.
	ConnectionAllowed(conn net.Conn) bool
}

// PIDScoper is an optional function that a TransparentRedirector can
// implement. It makes the interception more narrow: Glider then uses a set of
// process IDs that a person enrolls while Glider operates. This operates ON
// TOP OF AllowProcessNames, and not in place of it. A person can also change
// it while Glider operates. AllowProcessNames is different: RedirectConfig
// fixes it for the full life of the process. A person added this on
// 2026-07-31, after a true live incident. AllowProcessNames compares the name
// of the process image, such as "claude.exe", across the full machine.
// Therefore two processes agreed with the same name: the delegate subprocess
// of a controlled test, and the separate Claude Code session of the operator,
// which operated at the same time. Glider intercepted the true traffic of
// both, and it searched both for a flag. Therefore it took control of the
// live session of the operator, with no message, when a message in plain
// English ended with a trailing "/vendorname" flag that Glider recognized.
// PIDScoper lets a caller enroll only the exact PIDs that it intends to test.
// The dashboard or a test harness is such a caller. Each other process on the
// machine with an agreeing name stays untouched. An enrollment that is empty
// or nil is the default, and it keeps the behaviour of today with no change.
// A person selects this narrow mode, and it is not a new requirement. The
// primary statement of Glider is that it operates for a true session of one
// front CLI, and that it needs no cooperation. That statement depends on an
// enrollment that no person must make.
type PIDScoper interface {
	// SetEnrolledPIDs replaces the enrolled-PID set wholesale. Passing nil
	// or an empty slice disables PID narrowing entirely. Safe to call
	// concurrently with the redirector's own packet/connection handling.
	SetEnrolledPIDs(pids []uint32)
}
