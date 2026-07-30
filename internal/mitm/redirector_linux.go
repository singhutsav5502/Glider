//go:build linux

package mitm

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/glider-ai/glider/internal/procinfo"
	"golang.org/x/sys/unix"
)

const (
	linuxChainName       = "GLIDER_TRANSPARENT"
	linuxTCPTableTTL     = 2 * time.Second
	linuxOwnerRetries    = 4
	linuxOwnerRetryDelay = 3 * time.Millisecond
	// soOriginalDst is SO_ORIGINAL_DST from linux/netfilter_ipv4.h — not
	// exported by golang.org/x/sys/unix (it's a netfilter extension, not
	// a generic POSIX sockopt), so it's defined here directly.
	soOriginalDst = 80
)

// LinuxRedirector implements TransparentRedirector + OriginResolver +
// ProcessFilter using the standard Linux transparent-proxy technique
// (the same one redsocks and similar tools use): an iptables REDIRECT
// rule in the nat table's OUTPUT chain sends matched outbound connections
// to Glider's own local listener, and SO_ORIGINAL_DST recovers the real
// destination the kernel rewrote away, once per accepted connection.
//
// Deliberately connection-oriented, not packet-oriented like
// WinDivertRedirector: the kernel does the redirect once, before Glider's
// listener ever sees the connection, so there's no packet-by-packet
// decision to make here at all — and therefore no equivalent of the
// 2026-07-30 flow-sticky bug (see redirector_windows.go's handlePacket
// doc comment for that incident) is even possible on this platform. The
// one process-owner check this implementation does (ConnectionAllowed)
// happens exactly once, at accept() time, never re-evaluated per packet —
// the bug class simply doesn't exist here by construction, not because
// of a fix.
//
// A real, more severe risk than WinDivert's, worth calling out plainly:
// iptables rules are persistent kernel state, not tied to this process
// the way a WinDivert handle is — if Glider crashes before Stop() runs,
// the REDIRECT rule stays active indefinitely, silently blackholing
// traffic to a port nothing is listening on anymore, until something
// manually clears it. setupIPTables defends against a stale rule from a
// PREVIOUS crashed run (idempotent cleanup before creating anything), but
// there's no defense against THIS run crashing uncleanly — same category
// of incident as the documented WinDivert orphaned-rule postmortem, just
// with a different root cause (no handle-close-on-exit to rely on at
// all). Anyone testing this needs the same watchdog/cleanup discipline
// used for WinDivert all along, not implicit trust that a crash is safe.
type LinuxRedirector struct {
	Log *slog.Logger

	listenPort        int
	matchPorts        []int
	allowIPs          []string
	allowProcessNames map[string]bool

	chainCreated bool
	selfPID      uint32

	tcpTableMu sync.RWMutex
	tcpTable   map[uint16]uint32
	tcpTableAt time.Time

	pidNameMu    sync.Mutex
	pidNameCache map[uint32]string
}

// NewRedirector returns the platform's TransparentRedirector
// implementation. dllPath is Windows-only configuration, ignored here —
// matches procinfo's and redirector_other.go's existing per-platform
// constructor shape.
func NewRedirector(_ string, log *slog.Logger) TransparentRedirector {
	return &LinuxRedirector{Log: log}
}

func (r *LinuxRedirector) Start(ctx context.Context, cfg RedirectConfig) error {
	if r.Log == nil {
		r.Log = slog.Default()
	}
	matchPorts := cfg.MatchPorts
	if len(matchPorts) == 0 {
		matchPorts = []int{443}
	}

	if skipped := wildcardHosts(cfg.AllowHosts); len(skipped) > 0 {
		r.Log.Warn("mitm transparent: wildcard host pattern(s) cannot be resolved to a fixed IP for the iptables allowlist and will be skipped for transparent mode specifically — traffic to a matching subdomain will never reach Glider's redirect at all unless that exact subdomain is also listed explicitly",
			"patterns", skipped)
	}
	allowIPs := resolveAllowHosts(cfg.AllowHosts)
	if len(allowIPs) == 0 {
		return fmt.Errorf("mitm: no resolvable hosts in AllowHosts — refusing to start a filter that would divert every outbound connection")
	}

	r.listenPort = cfg.ListenPort
	r.matchPorts = matchPorts
	r.allowIPs = allowIPs
	r.allowProcessNames = expandProcessNameCandidates(cfg.AllowProcessNames)
	r.pidNameCache = make(map[uint32]string)
	r.selfPID = uint32(os.Getpid())

	if err := r.setupIPTables(); err != nil {
		return err
	}
	r.chainCreated = true

	r.Log.Info("mitm transparent redirector starting (linux/iptables)",
		"listen_port", r.listenPort, "match_ports", r.matchPorts,
		"allow_ips", r.allowIPs, "allow_process_names", mapKeys(r.allowProcessNames))
	return nil
}

func (r *LinuxRedirector) Stop() error {
	if r.Log != nil {
		r.Log.Info("mitm transparent redirector stopping (linux/iptables)")
	}
	if !r.chainCreated {
		return nil
	}
	r.teardownIPTablesBestEffort()
	r.chainCreated = false
	return nil
}

func (r *LinuxRedirector) setupIPTables() error {
	// Idempotent: clear out any stale chain/rule left behind by a
	// previous run that crashed before Stop() could run — see this
	// file's own top-level doc comment for why that's a real risk here.
	r.teardownIPTablesBestEffort()

	if err := runIPTables("-t", "nat", "-N", linuxChainName); err != nil {
		return fmt.Errorf("mitm transparent: create chain: %w", err)
	}
	for _, port := range r.matchPorts {
		for _, ip := range r.allowIPs {
			if err := runIPTables("-t", "nat", "-A", linuxChainName,
				"-d", ip, "-p", "tcp", "--dport", strconv.Itoa(port),
				"-j", "REDIRECT", "--to-port", strconv.Itoa(r.listenPort)); err != nil {
				r.teardownIPTablesBestEffort()
				return fmt.Errorf("mitm transparent: add redirect rule for %s:%d: %w", ip, port, err)
			}
		}
	}
	if err := runIPTables("-t", "nat", "-A", "OUTPUT", "-j", linuxChainName); err != nil {
		r.teardownIPTablesBestEffort()
		return fmt.Errorf("mitm transparent: hook OUTPUT chain: %w", err)
	}
	return nil
}

// teardownIPTablesBestEffort removes the OUTPUT jump and the chain
// itself, tolerating "doesn't exist" failures throughout — it's called
// both from Stop() (the expected, clean path) and from the start of
// setupIPTables (defending against a previous unclean exit), so it must
// never assume a known-good starting state. Loops the OUTPUT delete
// rather than assuming exactly one copy exists, since a previous Start
// without a matching Stop could have added more than one.
func (r *LinuxRedirector) teardownIPTablesBestEffort() {
	for i := 0; i < 8; i++ {
		if err := runIPTables("-t", "nat", "-D", "OUTPUT", "-j", linuxChainName); err != nil {
			break
		}
	}
	_ = runIPTables("-t", "nat", "-F", linuxChainName)
	_ = runIPTables("-t", "nat", "-X", linuxChainName)
}

// runIPTables always goes through "sudo -n iptables ..." rather than a
// bare "iptables ...": when Glider itself already runs as root (the
// expected deployment, matching "run as Administrator" parity on
// Windows), sudo executing as root is an unconditional no-op passthrough
// — no password check, no behavior change. When Glider runs unprivileged
// with a narrowly-scoped NOPASSWD sudoers rule for just iptables/ip
// (a reasonable alternative to full root), this is what makes that work
// at all. And when neither applies, -n makes the failure immediate and
// non-interactive — a clear permission error Start() can log and recover
// from the same way a non-Administrator WinDivertOpen failure already
// does, never a hang waiting on a password prompt nothing will ever
// answer (the exact failure mode this comment exists to prevent — hit
// live while first testing this file).
func runIPTables(args ...string) error {
	out, err := exec.Command("sudo", append([]string{"-n", "iptables"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ResolveOriginalDestination recovers the real destination a REDIRECT
// rule rewrote away, via SO_ORIGINAL_DST — the standard technique every
// Linux transparent proxy uses. conn must be the real *net.TCPConn (not
// wrapped) so SyscallConn() reaches the actual file descriptor; this is
// called directly on the connection handleTransparent just Accept()ed,
// before anything wraps it.
func (r *LinuxRedirector) ResolveOriginalDestination(conn net.Conn) (string, int, error) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return "", 0, fmt.Errorf("mitm transparent: not a *net.TCPConn: %T", conn)
	}
	rawConn, err := tcpConn.SyscallConn()
	if err != nil {
		return "", 0, err
	}

	var addr unix.RawSockaddrInet4
	var sockErr error
	ctrlErr := rawConn.Control(func(fd uintptr) {
		size := uint32(unsafe.Sizeof(addr))
		_, _, errno := unix.Syscall6(unix.SYS_GETSOCKOPT, fd, unix.SOL_IP, soOriginalDst,
			uintptr(unsafe.Pointer(&addr)), uintptr(unsafe.Pointer(&size)), 0)
		if errno != 0 {
			sockErr = errno
		}
	})
	if ctrlErr != nil {
		return "", 0, ctrlErr
	}
	if sockErr != nil {
		return "", 0, fmt.Errorf("mitm transparent: getsockopt SO_ORIGINAL_DST: %w", sockErr)
	}

	ip := net.IPv4(addr.Addr[0], addr.Addr[1], addr.Addr[2], addr.Addr[3])
	// addr.Port comes back in network byte order; Go reads the raw
	// memory as a native-endian uint16, so on this platform's
	// little-endian hosts the two bytes land swapped — same byte-swap
	// FetchTCPOwnerTable's Windows counterpart already needs for exactly
	// the same reason (see redirector_windows.go's MIB_TCPROW_OWNER_PID
	// comment).
	port := int(addr.Port&0xff)<<8 | int(addr.Port>>8)
	return ip.String(), port, nil
}

// ConnectionAllowed implements ProcessFilter — see redirector.go's own
// doc comment on that interface for why this has to run post-accept on
// Linux instead of pre-accept the way Windows' packet filter does.
func (r *LinuxRedirector) ConnectionAllowed(conn net.Conn) bool {
	if len(r.allowProcessNames) == 0 {
		return true
	}
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return false
	}
	remoteAddr, ok := tcpConn.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return false
	}
	// The client's own local port, from our side of an accepted
	// connection, is the port our peer connected FROM — exactly what
	// /proc/net/tcp indexes a socket by from that process's perspective.
	srcPort := uint16(remoteAddr.Port)

	pid, ok := r.ownerPID(srcPort)
	if !ok {
		r.Log.Debug("mitm transparent: no TCP-table owner found for port", "port", srcPort)
		return false
	}
	if pid == r.selfPID {
		return false
	}
	name, err := r.processName(pid)
	if err != nil {
		r.Log.Debug("mitm transparent: process name lookup failed", "pid", pid, "err", err)
		return false
	}
	allowed := r.allowProcessNames[name]
	if !allowed {
		r.Log.Debug("mitm transparent: rejecting non-vendor process", "port", srcPort, "pid", pid, "process", name)
	}
	return allowed
}

// ownerPID mirrors WinDivertRedirector.ownerPID exactly: a short TTL
// cache plus a bounded retry on a cold-cache miss for the specific port,
// the same live-diagnosed fix for a kernel-side propagation delay between
// connect() and the TCP table reflecting it (see redirector_windows.go's
// own ownerPID doc comment for the original incident writeup this
// defends against, ported here defensively rather than re-discovering it
// live on this platform too).
func (r *LinuxRedirector) ownerPID(port uint16) (uint32, bool) {
	r.tcpTableMu.RLock()
	fresh := time.Since(r.tcpTableAt) < linuxTCPTableTTL
	table := r.tcpTable
	r.tcpTableMu.RUnlock()

	if fresh {
		if pid, ok := table[port]; ok {
			return pid, true
		}
	}

	var newTable map[uint16]uint32
	var err error
	for attempt := 0; attempt < linuxOwnerRetries; attempt++ {
		newTable, err = procinfo.FetchTCPOwnerTable()
		if err != nil {
			break
		}
		if _, ok := newTable[port]; ok {
			break
		}
		time.Sleep(linuxOwnerRetryDelay)
	}
	if err != nil {
		if table != nil {
			pid, ok := table[port]
			return pid, ok
		}
		return 0, false
	}
	r.tcpTableMu.Lock()
	r.tcpTable = newTable
	r.tcpTableAt = time.Now()
	r.tcpTableMu.Unlock()

	pid, ok := newTable[port]
	return pid, ok
}

func (r *LinuxRedirector) processName(pid uint32) (string, error) {
	r.pidNameMu.Lock()
	if name, ok := r.pidNameCache[pid]; ok {
		r.pidNameMu.Unlock()
		return name, nil
	}
	r.pidNameMu.Unlock()

	name, err := procinfo.ProcessImageBasename(pid)
	if err != nil {
		return "", err
	}
	r.pidNameMu.Lock()
	r.pidNameCache[pid] = name
	r.pidNameMu.Unlock()
	return name, nil
}
