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
	"sync/atomic"
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
	// soOriginalDst is SO_ORIGINAL_DST, from linux/netfilter_ipv4.h. The package
	// golang.org/x/sys/unix does not export it, because it is an extension of
	// netfilter and not a usual POSIX socket option. Therefore this file defines
	// it directly.
	soOriginalDst = 80
)

// LinuxRedirector implements TransparentRedirector, OriginResolver and
// ProcessFilter. It uses the standard method for a transparent proxy on
// Linux, which redsocks and similar tools also use. A REDIRECT rule of
// iptables is in the OUTPUT chain of the nat table. It sends each outbound
// connection that agrees with the rule to the local listener of Glider. Then
// SO_ORIGINAL_DST gives the true destination that the kernel changed, one
// time for each connection that the code accepts.
//
// This implementation operates on a connection, and not on a packet.
// WinDivertRedirector operates on a packet. This difference is on purpose.
// The kernel does the redirect one time, before the listener of Glider sees
// the connection. Therefore there is no decision to make for each packet.
//
// For that cause, the defect with a sticky flow from 2026-07-30 cannot occur
// on this platform. Refer to the comment on handlePacket in
// redirector_windows.go for that incident. This implementation tests the
// owner of the process one time, in ConnectionAllowed, at the moment of
// accept(). It never tests it again for each packet. That class of defect
// does not exist here because of the structure, and not because of a
// correction.
//
// One risk here is true and more severe than the equal risk with WinDivert,
// and this comment states it directly. The rules of iptables are permanent
// state in the kernel. They have no relation to this process, and a WinDivert
// handle does have that relation. If Glider stops after a failure and before
// Stop() operates, the REDIRECT rule stays active with no limit. It then
// sends traffic to a port where no code listens, and it gives no message,
// until a person removes the rule.
//
// setupIPTables protects against a rule that a PREVIOUS run left after a
// failure, because it removes each rule before it makes one. But there is no
// protection against a failure of THIS run. This is the same class of
// incident as the record about the WinDivert rule that stayed after a
// failure. But the root cause is different. Here, no handle closes at the
// exit.
//
// A person who tests this needs the same discipline with a watchdog and with
// cleaning that WinDivert always needed. That person must not believe that a
// failure is safe.
type LinuxRedirector struct {
	Log *slog.Logger

	listenPort        int
	matchPorts        []int
	allowIPs          []string
	allowProcessNames map[string]bool

	// pidScopingActive lets pidEnrolled use no lock when no PID enrollment is
	// active. The cause is the same as for the field with the same name in
	// WinDivertRedirector, in redirector_windows.go. This file has it for
	// agreement between the two implementations. But this path operates one time
	// for each connection that the code accepts. It does not operate one time
	// for each packet.
	pidScopingActive atomic.Bool
	pidScopeMu       sync.Mutex
	enrolledPIDs     map[uint32]bool // nil/empty => no PID narrowing (see PIDScoper's doc comment, redirector.go)

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
	// This operation can occur again with the same result. It removes each chain
	// and each rule that a previous run left after a failure, before Stop() could
	// operate. Refer to the comment at the top of this file for the cause: that
	// risk is true here.
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

// teardownIPTablesBestEffort removes the jump in OUTPUT and the chain itself.
// It accepts each failure that says "this does not exist".
//
// Two positions call it. The first is Stop(), which is the usual and clean
// path. The second is the start of setupIPTables, which protects against an
// earlier exit after a failure. Therefore it must never assume that the start
// state is correct.
//
// It repeats the delete of the OUTPUT rule in a loop, and it does not assume
// that exactly one copy exists. An earlier Start with no Stop can add more
// than one copy.
func (r *LinuxRedirector) teardownIPTablesBestEffort() {
	for i := 0; i < 8; i++ {
		if err := runIPTables("-t", "nat", "-D", "OUTPUT", "-j", linuxChainName); err != nil {
			break
		}
	}
	_ = runIPTables("-t", "nat", "-F", linuxChainName)
	_ = runIPTables("-t", "nat", "-X", linuxChainName)
}

// runIPTables always uses "sudo -n iptables ...", and not "iptables ..."
// alone.
//
// Glider usually operates as root, and that agrees with "run as
// Administrator" on Windows. In that condition, sudo runs as root and changes
// nothing. It asks for no password and it changes no behaviour.
//
// Glider can operate with no privilege. If a person made a sudoers rule with
// NOPASSWD, and that rule has a small scope for iptables only, this command
// uses that rule.
func runIPTables(args ...string) error {
	out, err := exec.Command("sudo", append([]string{"-n", "iptables"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ResolveOriginalDestination recovers the real destination a REDIRECT rule
// rewrote away, via SO_ORIGINAL_DST — the standard technique every Linux
// transparent proxy uses. conn must be the true *net.TCPConn, and no code can
// put it in a wrapper. Then SyscallConn() reaches the true file descriptor.
// Code calls this directly on the connection that handleTransparent accepted,
// and before any wrapper.
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
	// addr.Port arrives in the byte order of the network. Go reads the raw
	// memory as a uint16 with the byte order of the machine. Therefore on a host
	// with the little-endian order, the two bytes are in the wrong sequence. The
	// equal function of FetchTCPOwnerTable on Windows needs the same change of
	// the bytes, for exactly the same cause. Refer to the comment about
	// MIB_TCPROW_OWNER_PID in redirector_windows.go.
	port := int(addr.Port&0xff)<<8 | int(addr.Port>>8)
	return ip.String(), port, nil
}

// ConnectionAllowed implements ProcessFilter. Refer to the comment on that
// interface in redirector.go for the cause. On Linux this must operate after
// the accept. The packet filter of Windows operates before the accept.
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
	// The local port of the client, seen from this side of a connection that
	// the code accepted, is the port that the peer connected FROM. That is
	// exactly the value that /proc/net/tcp uses as the key for a socket, from
	// the view of that process.
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
		return false
	}
	if !r.pidEnrolled(pid) {
		r.Log.Debug("mitm transparent: rejecting non-enrolled process", "port", srcPort, "pid", pid, "process", name)
		return false
	}
	return true
}

// pidEnrolled mirrors WinDivertRedirector.pidEnrolled — see that doc
// comment (redirector_windows.go) and PIDScoper (redirector.go) for why
// this exists.
func (r *LinuxRedirector) pidEnrolled(pid uint32) bool {
	if !r.pidScopingActive.Load() {
		return true
	}
	r.pidScopeMu.Lock()
	defer r.pidScopeMu.Unlock()
	if len(r.enrolledPIDs) == 0 {
		return true
	}
	return r.enrolledPIDs[pid]
}

// SetEnrolledPIDs implements PIDScoper — see WinDivertRedirector's own
// implementation for why pidScopingActive is set last on enroll and
// cleared first on disable.
func (r *LinuxRedirector) SetEnrolledPIDs(pids []uint32) {
	if len(pids) == 0 {
		r.pidScopingActive.Store(false)
		r.pidScopeMu.Lock()
		r.enrolledPIDs = nil
		r.pidScopeMu.Unlock()
		return
	}
	set := make(map[uint32]bool, len(pids))
	for _, pid := range pids {
		set[pid] = true
	}
	r.pidScopeMu.Lock()
	r.enrolledPIDs = set
	r.pidScopeMu.Unlock()
	r.pidScopingActive.Store(true)
}

// ownerPID does exactly the same work as WinDivertRedirector.ownerPID. It
// uses a cache with a short TTL. It then tries again, a limited number of
// times, when that cache has no value for the port. This is the same
// correction that a live test gave for a delay in the kernel, between
// connect() and the moment when the TCP table shows the connection. Refer to
// the comment on ownerPID in redirector_windows.go for the record of the
// first incident. A person moved this code here for protection. That person
// did not wait to find the same problem live on this platform.
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
