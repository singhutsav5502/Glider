//go:build windows

package mitm

import (
	"context"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/glider-ai/glider/internal/procinfo"
	"github.com/glider-ai/glider/internal/safego"
)

const (
	windivertLayerNetwork = 0
	windivertAddrBufSize  = 80 // WINDIVERT_ADDRESS; generously sized, only Recv/Send need it intact
	flowEntryTTL          = 2 * time.Minute
	numWorkers            = 8
	// workerQueueSize was 256 until a real, live-confirmed capacity issue
	// (2026-07-29): recvLoop is a single goroutine reading from WinDivert,
	// and its "queue full" fallback blocks that same goroutine until the
	// congested shard's channel has room — so a packet burst on ONE
	// connection (e.g. one large HTTPS response) can stall recvLoop from
	// reading *any* packets at all, including ones that would hash to a
	// completely idle shard, for as long as that one shard stays full.
	// Observed live: a single large (~2MB) HTTPS message pushed
	// queue_full into the thousands during a run that also had a
	// separate real client (cursor-agent, on a different shard in the
	// same window) experiencing connection resets — consistent with,
	// though not conclusively proven to be caused by, this head-of-line
	// blocking. Raising the buffer doesn't change the blocking fallback's
	// existence (still needed — dropping a packet here breaks that
	// connection outright) but makes hitting it far less frequent for
	// any realistic single-connection burst.
	workerQueueSize  = 4096
	tcpTableRefresh  = 2 * time.Second
	statsLogInterval = 15 * time.Second
)

type flowEntry struct {
	host       string // original real destination, dotted-quad
	port       int
	insertedAt time.Time
}

type packetJob struct {
	pkt  []byte // owned copy — safe to hand to a worker, buf gets reused immediately
	addr []byte // owned copy
}

// WinDivertRedirector implements TransparentRedirector + OriginResolver
// using WinDivert (github.com/basil00/WinDivert). See
// planning/transparent_redirector_design.md §5 and two live postmortems
// from 2026-07-26 this version fixes:
//  1. The first cut only rewrote the forward direction (client → real
//     server), so replies came back with source 127.0.0.1:<port> instead of
//     the real server's address — the client's TCP stack matched incoming
//     packets against the peer it actually connected to, saw a mismatch, and
//     silently dropped every reply. Fixed by handlePacket rewriting both
//     directions (see the switch there) and by rewriting to this machine's
//     real local IP (detectPrimaryLocalIP), not loopback.
//  2. IP-only filtering diverted more than intended: Chrome's own background
//     telemetry (optimizationguide-pa.googleapis.com, jnn-pa.googleapis.com)
//     got captured because it happened to share a Google frontend IP with an
//     allowlisted googleapis.com host. Fixed by AllowProcessNames — an
//     additional per-connection owning-process check (tcpOwnerTable +
//     processImageBasename) that only redirects traffic actually owned by a
//     known vendor CLI process, not just anything hitting the same IP.
//  3. A burst of many simultaneous connections serialized through one
//     single-threaded Recv→handle→reinject loop and stalled. Fixed by a
//     worker pool (numWorkers goroutines) sharded by the connection's client
//     key, so packets from different connections process in parallel while
//     packets from the *same* connection stay strictly ordered (forward and
//     return packets for one client always land on the same worker).
type WinDivertRedirector struct {
	DLLPath string
	Log     *slog.Logger

	mod               *syscall.LazyDLL
	procOpen          *syscall.LazyProc
	procRecv          *syscall.LazyProc
	procSend          *syscall.LazyProc
	procClose         *syscall.LazyProc
	procCalcChecksums *syscall.LazyProc

	handle     syscall.Handle
	handleOpen bool
	listenPort int
	matchPorts map[uint16]bool
	localIP    net.IP
	allowIPs   map[string]bool // resolved allowlist IPs, dotted-quad -> true

	allowProcessNames map[string]bool // lowercased basenames; nil/empty => no process narrowing

	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu    sync.Mutex
	flows map[string]flowEntry // keyed by CLIENT "ip:port" (constant across both directions)

	jobs [numWorkers]chan packetJob

	tcpTableMu sync.RWMutex
	tcpTable   map[uint16]uint32 // local port -> owning PID, refreshed periodically
	tcpTableAt time.Time

	pidNameMu    sync.Mutex
	pidNameCache map[uint32]string

	statProcessed    atomic.Int64
	statRedirected   atomic.Int64
	statReturned     atomic.Int64
	statRejectedProc atomic.Int64
	statSelfTraffic  atomic.Int64
	statUnmatched    atomic.Int64
	statRecvErrors   atomic.Int64
	statQueueFull    atomic.Int64
}

// NewRedirector returns the platform's TransparentRedirector implementation.
// dllPath points at WinDivert.dll (WinDivertNN.sys must sit alongside it).
func NewRedirector(dllPath string, log *slog.Logger) TransparentRedirector {
	if log == nil {
		log = slog.Default()
	}
	return &WinDivertRedirector{DLLPath: dllPath, Log: log}
}

func (r *WinDivertRedirector) Start(ctx context.Context, cfg RedirectConfig) error {
	if cfg.ListenPort == 0 {
		return fmt.Errorf("mitm: RedirectConfig.ListenPort is required")
	}
	if r.DLLPath == "" {
		return fmt.Errorf("mitm: WinDivertRedirector.DLLPath is required")
	}
	if _, err := os.Stat(r.DLLPath); err != nil {
		return fmt.Errorf("mitm: WinDivert DLL not found at %s: %w", r.DLLPath, err)
	}

	localIP, err := detectPrimaryLocalIP()
	if err != nil {
		return fmt.Errorf("mitm: detect local IP: %w", err)
	}
	r.localIP = localIP

	matchPorts := cfg.MatchPorts
	if len(matchPorts) == 0 {
		matchPorts = []int{443}
	}
	r.matchPorts = make(map[uint16]bool, len(matchPorts))
	for _, p := range matchPorts {
		r.matchPorts[uint16(p)] = true
	}

	if skipped := wildcardHosts(cfg.AllowHosts); len(skipped) > 0 {
		// Not just a comment anymore (2026-07-28 finding: a wildcard host
		// silently missing from transparent mode's IP-based packet filter
		// was the actual root cause of cursor-agent's completion call
		// never being redirected at all, independent of and in addition
		// to the tcpTable timing fix in ownerPID) — a real, actionable
		// startup warning naming exactly which patterns are affected, so
		// this doesn't have to be independently rediscovered by reading
		// resolveAllowHosts' source next time a wildcard host doesn't
		// work under transparent mode.
		r.Log.Warn("mitm transparent: wildcard host pattern(s) cannot be resolved to a fixed IP for the packet filter and will be skipped for transparent mode specifically — traffic to a matching subdomain will never reach Glider's redirect at all unless that exact subdomain is also listed explicitly",
			"patterns", skipped)
	}
	allowIPs := resolveAllowHosts(cfg.AllowHosts)
	if len(allowIPs) == 0 {
		return fmt.Errorf("mitm: no resolvable hosts in AllowHosts — refusing to start a filter that would divert every outbound connection")
	}
	r.allowIPs = make(map[string]bool, len(allowIPs))
	for _, ip := range allowIPs {
		r.allowIPs[ip] = true
	}

	r.allowProcessNames = expandProcessNameCandidates(cfg.AllowProcessNames)
	r.pidNameCache = make(map[uint32]string)

	r.listenPort = cfg.ListenPort
	r.flows = make(map[string]flowEntry)

	r.mod = syscall.NewLazyDLL(r.DLLPath)
	r.procOpen = r.mod.NewProc("WinDivertOpen")
	r.procRecv = r.mod.NewProc("WinDivertRecv")
	r.procSend = r.mod.NewProc("WinDivertSend")
	r.procClose = r.mod.NewProc("WinDivertClose")
	r.procCalcChecksums = r.mod.NewProc("WinDivertHelperCalcChecksums")

	filter := buildWinDivertFilter(matchPorts, allowIPs, r.listenPort, localIP)
	filterPtr, err := syscall.BytePtrFromString(filter)
	if err != nil {
		return fmt.Errorf("mitm: encode WinDivert filter: %w", err)
	}

	h, _, callErr := r.procOpen.Call(
		uintptr(unsafe.Pointer(filterPtr)),
		uintptr(windivertLayerNetwork),
		0,
		0,
	)
	if h == 0 || int64(h) == -1 {
		return fmt.Errorf("mitm: WinDivertOpen failed (filter=%q): %w", filter, callErr)
	}
	r.handle = syscall.Handle(h)
	r.handleOpen = true

	r.Log.Info("mitm transparent redirector starting",
		"local_ip", localIP.String(), "listen_port", r.listenPort,
		"match_ports", matchPorts, "allow_ips", allowIPs,
		"allow_process_names", mapKeys(r.allowProcessNames), "workers", numWorkers)

	loopCtx, cancel := context.WithCancel(ctx)
	r.cancel = cancel

	for i := range r.jobs {
		r.jobs[i] = make(chan packetJob, workerQueueSize)
		r.wg.Add(1)
		idx := i
		safego.Go(fmt.Sprintf("windivert-worker-%d", idx), r.Log, func() { r.worker(loopCtx, idx) })
	}
	r.wg.Add(3)
	safego.Go("windivert-recvLoop", r.Log, func() { r.recvLoop(loopCtx) })
	safego.Go("windivert-sweepStaleFlows", r.Log, func() { r.sweepStaleFlows(loopCtx) })
	safego.Go("windivert-statsLoop", r.Log, func() { r.statsLoop(loopCtx) })
	return nil
}

func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// expandProcessNameCandidates lowercases each configured process name and,
// for script-wrapped CLIs (.cmd/.bat/.ps1 — cursor-agent.cmd is the known
// live case), also adds "node.exe" as a best-effort fallback: a wrapper
// script doesn't itself own the TCP connection, the interpreter it spawns
// does, and for a Node-based CLI that's node.exe. Documented as an
// approximation, not a general solution for arbitrary wrapper chains.
func expandProcessNameCandidates(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		out[n] = true
		if strings.HasSuffix(n, ".cmd") || strings.HasSuffix(n, ".bat") || strings.HasSuffix(n, ".ps1") {
			out["node.exe"] = true
		}
	}
	return out
}

// detectPrimaryLocalIP finds this machine's real, routable local IP (not
// 127.0.0.1) by asking the OS which interface it would use to reach an
// external address — no packet is actually sent (UDP "connect" only sets
// up local routing state). Rewriting redirected destinations to this real
// interface IP, rather than loopback, is the standard WinDivert NAT
// technique.
func detectPrimaryLocalIP() (net.IP, error) {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return nil, fmt.Errorf("unexpected local addr type %T", conn.LocalAddr())
	}
	return udpAddr.IP, nil
}

// resolveAllowHosts resolves each concrete hostname in hosts to its current
// IPv4 addresses. "*.domain" wildcard entries are skipped (no single
// concrete IP to resolve). Unresolvable hosts are skipped, not fatal.
// wildcardHosts returns the "*.domain" entries in hosts — the ones
// resolveAllowHosts below silently skips, since a wildcard has no single
// concrete IP to add to the packet filter's allowlist.
func wildcardHosts(hosts []string) []string {
	var out []string
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if strings.HasPrefix(h, "*.") {
			out = append(out, h)
		}
	}
	return out
}

func resolveAllowHosts(hosts []string) []string {
	var ips []string
	seen := make(map[string]bool)
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" || strings.HasPrefix(h, "*.") {
			continue
		}
		addrs, err := net.LookupIP(h)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if v4 := a.To4(); v4 != nil {
				s := v4.String()
				if !seen[s] {
					seen[s] = true
					ips = append(ips, s)
				}
			}
		}
	}
	return ips
}

// Stop must fully undo Start — no leftover kernel state. A process that
// gets killed before reaching Stop leaves the kernel-side diversion rule
// active with nothing listening on the other end, which is worse than
// doing nothing (observed live 2026-07-26) — this method's contract exists
// specifically to prevent that when reachable at all.
func (r *WinDivertRedirector) Stop() error {
	r.Log.Info("mitm transparent redirector stopping", "stats", r.statsSnapshot())
	if r.cancel != nil {
		r.cancel()
	}
	if r.handleOpen {
		r.procClose.Call(uintptr(r.handle))
		r.handleOpen = false
	}
	r.wg.Wait()
	r.Log.Info("mitm transparent redirector stopped cleanly")
	return nil
}

func (r *WinDivertRedirector) statsSnapshot() map[string]int64 {
	return map[string]int64{
		"processed":     r.statProcessed.Load(),
		"redirected":    r.statRedirected.Load(),
		"returned":      r.statReturned.Load(),
		"rejected_proc": r.statRejectedProc.Load(),
		"self_traffic":  r.statSelfTraffic.Load(),
		"unmatched":     r.statUnmatched.Load(),
		"recv_errors":   r.statRecvErrors.Load(),
		"queue_full":    r.statQueueFull.Load(),
	}
}

func (r *WinDivertRedirector) statsLoop(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(statsLogInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.mu.Lock()
			activeFlows := len(r.flows)
			r.mu.Unlock()
			r.Log.Info("mitm transparent redirector stats", "active_flows", activeFlows, "counters", r.statsSnapshot())
		}
	}
}

// buildWinDivertFilter matches two categories of traffic: forward (client →
// an allowlisted server on an allowlisted port) and return (Glider's own
// reply traffic out of listenPort). Process-based narrowing (AllowProcessNames)
// happens in Go inside handlePacket, not in the WinDivert filter string
// itself — WinDivert's NETWORK layer filter language has no notion of
// owning process, only the FLOW/SOCKET layers do, and mixing layers here
// would be a much bigger change than this fix warrants; the IP/port filter
// stays the first (cheap, kernel-side) narrowing pass, process ownership is
// the second (userspace) pass.
func buildWinDivertFilter(matchPorts []int, allowIPs []string, listenPort int, localIP net.IP) string {
	portTerms := make([]string, len(matchPorts))
	for i, p := range matchPorts {
		portTerms[i] = fmt.Sprintf("tcp.DstPort == %d", p)
	}
	ipTerms := make([]string, len(allowIPs))
	for i, ip := range allowIPs {
		ipTerms[i] = fmt.Sprintf("ip.DstAddr == %s", ip)
	}
	forward := fmt.Sprintf("(outbound and (%s) and (%s))",
		strings.Join(portTerms, " or "), strings.Join(ipTerms, " or "))
	ret := fmt.Sprintf("(outbound and tcp.SrcPort == %d and ip.SrcAddr == %s)", listenPort, localIP.String())
	return forward + " or " + ret
}

func (r *WinDivertRedirector) recvLoop(ctx context.Context) {
	defer r.wg.Done()
	buf := make([]byte, 65535)
	addr := make([]byte, windivertAddrBufSize)
	var recvLen uint32

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		ret, _, callErr := r.procRecv.Call(
			uintptr(r.handle),
			uintptr(unsafe.Pointer(&buf[0])),
			uintptr(len(buf)),
			uintptr(unsafe.Pointer(&recvLen)),
			uintptr(unsafe.Pointer(&addr[0])),
		)
		if ret == 0 {
			r.statRecvErrors.Add(1)
			if r.statRecvErrors.Load()%50 == 1 { // don't flood the log under sustained errors
				r.Log.Warn("mitm transparent WinDivertRecv error", "err", callErr, "total_errors", r.statRecvErrors.Load())
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
			continue
		}

		n := int(recvLen)
		r.statProcessed.Add(1)

		// buf/addr are reused on the next Recv — copy before handing to a
		// worker so a concurrent goroutine never reads memory this loop is
		// about to overwrite.
		pktCopy := make([]byte, n)
		copy(pktCopy, buf[:n])
		addrCopy := make([]byte, windivertAddrBufSize)
		copy(addrCopy, addr)

		shard := r.shardFor(pktCopy)
		select {
		case r.jobs[shard] <- packetJob{pkt: pktCopy, addr: addrCopy}:
		default:
			r.statQueueFull.Add(1)
			// Block rather than drop — dropping a packet here breaks that
			// connection outright, and a full queue is exactly the signal
			// statsLoop's periodic log is meant to surface, not a reason to
			// silently discard traffic.
			select {
			case r.jobs[shard] <- packetJob{pkt: pktCopy, addr: addrCopy}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// shardFor picks a worker so that all packets belonging to one client
// connection (both directions) are processed in order by the same
// goroutine, while different connections can run fully in parallel across
// workers — the fix for the 2026-07-26 stall, without introducing
// same-connection packet reordering.
func (r *WinDivertRedirector) shardFor(pkt []byte) int {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return 0
	}
	ihl := int(pkt[0]&0x0f) * 4
	if len(pkt) < ihl+4 {
		return 0
	}
	srcIP := pkt[12:16]
	dstIP := pkt[16:20]
	srcPort := binary.BigEndian.Uint16(pkt[ihl : ihl+2])
	dstPort := binary.BigEndian.Uint16(pkt[ihl+2 : ihl+4])

	h := fnv.New32a()
	if int(srcPort) == r.listenPort {
		// Return packet: client is the destination.
		h.Write(dstIP)
		binary.Write(h, binary.BigEndian, dstPort)
	} else {
		// Forward packet: client is the source.
		h.Write(srcIP)
		binary.Write(h, binary.BigEndian, srcPort)
	}
	return int(h.Sum32() % numWorkers)
}

func (r *WinDivertRedirector) worker(ctx context.Context, idx int) {
	defer r.wg.Done()
	ch := r.jobs[idx]
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-ch:
			r.handlePacket(job.pkt, job.addr)
		}
	}
}

// handlePacket rewrites a matched packet and reinjects it — or, if it
// doesn't parse as a sane IPv4/TCP segment, or doesn't match either the
// forward or return shape the filter guarantees, reinjects it completely
// unchanged. Every code path ends in a reinject: with flags=0 the driver
// already removed this packet from the stack, so failing to resend it
// drops that connection's traffic, and a bug here must never silently
// swallow a packet.
func (r *WinDivertRedirector) handlePacket(pkt []byte, addr []byte) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		r.reinject(pkt, addr)
		return
	}
	ihl := int(pkt[0]&0x0f) * 4
	if len(pkt) < ihl+20 {
		r.reinject(pkt, addr)
		return
	}
	const tcpProto = 6
	if pkt[9] != tcpProto {
		r.reinject(pkt, addr)
		return
	}

	srcIP := net.IP(append([]byte(nil), pkt[12:16]...)).String()
	dstIP := net.IP(append([]byte(nil), pkt[16:20]...)).String()
	srcPort := binary.BigEndian.Uint16(pkt[ihl : ihl+2])
	dstPort := binary.BigEndian.Uint16(pkt[ihl+2 : ihl+4])

	switch {
	case int(srcPort) == r.listenPort && srcIP == r.localIP.String():
		key := dstIP + ":" + strconv.Itoa(int(dstPort))
		r.mu.Lock()
		entry, ok := r.flows[key]
		if ok {
			entry.insertedAt = time.Now() // refresh TTL on every use, not just the initial forward packet
			r.flows[key] = entry
		}
		r.mu.Unlock()
		if !ok {
			r.reinject(pkt, addr)
			return
		}
		copy(pkt[12:16], net.ParseIP(entry.host).To4())
		binary.BigEndian.PutUint16(pkt[ihl:ihl+2], uint16(entry.port))
		r.statReturned.Add(1)
		r.recalcAndReinject(pkt, addr)

	case r.matchPorts[dstPort] && r.allowIPs[dstIP]:
		// processAllowed is gated per FLOW, not per packet. It used to run
		// on every single forward packet, which live-diagnosed 2026-07-30
		// as the real cause of cursor-agent needing several silent
		// reconnects before a plain (non-delegate) completion call finally
		// got through: ownerPID's answer comes from a 2s time-windowed,
		// port-keyed OS TCP-table snapshot, and a single transient miss on
		// ANY mid-connection packet (not just the opening SYN) flipped
		// processAllowed to false for that one packet, which then got
		// reinjected unredirected — sent straight to the real origin
		// instead of the local listener Glider had already accepted the
		// connection on. That one misrouted packet vanishes from Glider's
		// side of an already-established TCP stream, which is exactly
		// what a client sees as a dropped connection. Fix: decide once,
		// at the first packet of a flow (keyed by client ip:port, same
		// key the return path already uses), and trust that decision for
		// every later packet on the same flow — a real client can't
		// switch owning processes mid-connection, so re-checking
		// ownerPID's racy, time-windowed cache on every packet was pure
		// risk with no correctness benefit. The existing flowEntryTTL
		// (2min, matching Windows' own default TIME_WAIT) already bounds
		// how long a stale entry could theoretically outlive a closed
		// connection and its port being reused by a different process —
		// this reuses that same accepted tradeoff, not a new one.
		key := srcIP + ":" + strconv.Itoa(int(srcPort))
		r.mu.Lock()
		entry, known := r.flows[key]
		if known {
			entry.insertedAt = time.Now()
			r.flows[key] = entry
		}
		r.mu.Unlock()

		if !known {
			if !r.processAllowed(srcPort) {
				r.statRejectedProc.Add(1)
				r.reinject(pkt, addr) // matched by IP/port, rejected by owning process — pass through untouched
				return
			}
			r.mu.Lock()
			r.flows[key] = flowEntry{host: dstIP, port: int(dstPort), insertedAt: time.Now()}
			r.mu.Unlock()
		}

		copy(pkt[16:20], r.localIP.To4())
		binary.BigEndian.PutUint16(pkt[ihl+2:ihl+4], uint16(r.listenPort))
		r.statRedirected.Add(1)
		r.recalcAndReinject(pkt, addr)

	default:
		r.statUnmatched.Add(1)
		r.reinject(pkt, addr)
	}
}

// processAllowed reports whether srcPort's owning process matches
// AllowProcessNames. No configured names → no narrowing (always allowed) —
// this is opt-in scoping, not a hard requirement.
func (r *WinDivertRedirector) processAllowed(srcPort uint16) bool {
	if len(r.allowProcessNames) == 0 {
		return true
	}
	pid, ok := r.ownerPID(srcPort)
	if !ok {
		r.Log.Debug("mitm transparent: no TCP-table owner found for port", "port", srcPort)
		return false
	}

	// Fast path, checked before any process-name lookup: Glider's own
	// origin-passthrough/upstream dials (blindTunnel, mitmSession) are real
	// outbound connections to the same allowlisted hosts, so they hit this
	// same filter — expected, not an anomaly. Confirmed live (2026-07-26):
	// a single active Glider passthrough connection generated 1300+ packets
	// in a few seconds, and running the full OpenProcess+
	// QueryFullProcessImageNameW lookup (even cached, still a map lookup
	// under a lock) plus a Debug log line for every single one of those
	// packets was real, needless overhead on Glider's own hot path — a
	// pure PID comparison is orders of magnitude cheaper and belongs first.
	if pid == uint32(os.Getpid()) {
		r.statSelfTraffic.Add(1)
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

func (r *WinDivertRedirector) ownerPID(port uint16) (uint32, bool) {
	r.tcpTableMu.RLock()
	fresh := time.Since(r.tcpTableAt) < tcpTableRefresh
	table := r.tcpTable
	r.tcpTableMu.RUnlock()

	if fresh {
		pid, ok := table[port]
		if ok {
			return pid, true
		}
	}

	// Cache miss or stale: refresh, with a short bounded retry if the
	// specific port still isn't in a fresh snapshot. A single synchronous
	// refresh (the original version of this function, before 2026-07-28)
	// was reasoned to be enough — "connect() populates the table before
	// any packet leaves" — but live testing that day proved that
	// reasoning wrong in practice: a real cursor-agent -p run's early
	// connections (its account-plane calls, and the completion call
	// itself) were consistently missed even with the single-refresh
	// version, while a LATE connection from the same short-lived process
	// (a telemetry call sent near exit) was reliably caught. That split
	// — early connections miss, late ones don't — is exactly the
	// signature of a genuine kernel-side propagation delay between
	// connect() and GetExtendedTcpTable reflecting it, not a permanent
	// mismatch or a config problem (hosts/process-name allowlisting were
	// independently confirmed correct for the same test). A few retries a
	// couple milliseconds apart costs nothing on the common/hot path
	// (only ever runs after a miss already happened) and directly targets
	// that propagation window.
	const (
		ownerPIDRetries    = 4
		ownerPIDRetryDelay = 3 * time.Millisecond
	)
	var newTable map[uint16]uint32
	var err error
	for attempt := 0; attempt < ownerPIDRetries; attempt++ {
		newTable, err = procinfo.FetchTCPOwnerTable()
		if err != nil {
			break
		}
		if _, ok := newTable[port]; ok {
			break
		}
		time.Sleep(ownerPIDRetryDelay)
	}
	if err != nil {
		r.Log.Debug("mitm transparent: GetExtendedTcpTable failed", "err", err)
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

func (r *WinDivertRedirector) processName(pid uint32) (string, error) {
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

func (r *WinDivertRedirector) recalcAndReinject(pkt []byte, addr []byte) {
	r.procCalcChecksums.Call(
		uintptr(unsafe.Pointer(&pkt[0])),
		uintptr(len(pkt)),
		uintptr(unsafe.Pointer(&addr[0])),
		0,
	)
	r.reinject(pkt, addr)
}

func (r *WinDivertRedirector) reinject(pkt []byte, addr []byte) {
	if len(pkt) == 0 {
		return
	}
	var sendLen uint32
	r.procSend.Call(
		uintptr(r.handle),
		uintptr(unsafe.Pointer(&pkt[0])),
		uintptr(len(pkt)),
		uintptr(unsafe.Pointer(&sendLen)),
		uintptr(unsafe.Pointer(&addr[0])),
	)
}

// sweepStaleFlows evicts flow-table entries nothing ever claimed (a
// connection that was redirected at the packet level but never actually
// exchanged another packet) — the eviction policy flagged as an open item
// in the design doc §5/§9.
func (r *WinDivertRedirector) sweepStaleFlows(ctx context.Context) {
	defer r.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cutoff := time.Now().Add(-flowEntryTTL)
			r.mu.Lock()
			evicted := 0
			for k, v := range r.flows {
				if v.insertedAt.Before(cutoff) {
					delete(r.flows, k)
					evicted++
				}
			}
			r.mu.Unlock()
			if evicted > 0 {
				r.Log.Debug("mitm transparent: evicted stale flows", "count", evicted)
			}
		}
	}
}

// ResolveOriginalDestination implements OriginResolver: look up the flow
// table entry populated at rewrite time, keyed by the client's remote
// address. Left in the table (not deleted) because handlePacket's
// return-direction rewrite needs the same entry for as long as the
// connection stays open, not just once at accept time.
func (r *WinDivertRedirector) ResolveOriginalDestination(conn net.Conn) (string, int, error) {
	key := conn.RemoteAddr().String()
	r.mu.Lock()
	entry, ok := r.flows[key]
	r.mu.Unlock()
	if !ok {
		return "", 0, fmt.Errorf("mitm: no original-destination record for %s", key)
	}
	return entry.host, entry.port, nil
}

// TCP-owner-PID + process-name lookups now live in internal/procinfo
// (FetchTCPOwnerTable, ProcessImageBasename) — moved there 2026-07-26 so
// the gateway HTTP server's origin-process resolution (needed for
// workspace-directory scoping, see internal/vendors' WorkspaceStore)
// shares the exact same implementation instead of a second, drifting copy.
