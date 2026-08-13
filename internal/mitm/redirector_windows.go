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
	// workerQueueSize was 256 until a live test found a capacity problem on
	// 2026-07-29.
	//
	// recvLoop is one goroutine, and it reads from WinDivert. When a queue
	// becomes full, its fallback blocks that same goroutine. It waits until the
	// channel of the full shard has space. Therefore a burst of packets on ONE
	// connection, for example one large HTTPS response, can stop recvLoop from
	// reading *any* packet. This includes a packet for a shard that does no work.
	// The stop continues while that one shard stays full.
	//
	// A live test showed this: one large HTTPS message of approximately 2MB
	// increased queue_full to more than one thousand. In the same period, a
	// different true client, cursor-agent on a different shard, had connection
	// resets. This agrees with the block at the head of the line, but no test
	// proved that the block caused it.
	//
	// A larger buffer does not remove the fallback that blocks. That fallback is
	// still necessary, because to discard a packet here stops that connection
	// fully. But the larger buffer makes the condition much less frequent for
	// each realistic burst on one connection.
	workerQueueSize  = 4096
	tcpTableRefresh  = 2 * time.Second
	statsLogInterval = 15 * time.Second
)

// flowEntry is defined in packet_classify.go, shared with every
// platform's redirector implementation.

type packetJob struct {
	pkt  []byte // owned copy — safe to hand to a worker, buf gets reused immediately
	addr []byte // owned copy
}

// WinDivertRedirector implements TransparentRedirector and OriginResolver
// with WinDivert (github.com/basil00/WinDivert). Refer to
// planning/transparent_redirector_design.md §5.
//
// This version corrects three problems that live tests found on 2026-07-26:
//
//  1. The first version changed only the forward direction, from the client
//     to the true server. Therefore the replies came back with the source
//     127.0.0.1:<port>, and not the address of the true server. The TCP stack
//     of the client compared each incoming packet with the peer that it
//     connected to, saw a difference, and discarded each reply with no
//     message. The correction is in handlePacket: it now changes both
//     directions (refer to the switch there), and it writes the true local IP
//     address of this machine (detectPrimaryLocalIP), and not the loopback
//     address.
//  2. A filter on the IP address alone took more traffic than it must. Chrome
//     sends its own data in the background to optimizationguide-pa
//     .googleapis.com and jnn-pa.googleapis.com. Glider captured that traffic,
//     because it used the same Google front-end IP address as a host on the
//     allowlist. The correction is AllowProcessNames. It adds a test of the
//     process that owns each connection (tcpOwnerTable and
//     processImageBasename). Thus Glider redirects only the traffic that a
//     known vendor CLI process owns, and not each connection to the same IP
//     address.
//  3. A burst of many connections at the same time went in a sequence through
//     one loop of Recv, handle and reinject, and that loop stopped. The
//     correction is a pool of workers, with numWorkers goroutines, divided by
//     the client key of the connection. Thus packets from different
//     connections operate at the same time, and the packets of one connection
//     stay in a strict sequence. The forward packets and the return packets of
//     one client always go to the same worker.
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

	// pidScopingActive lets pidEnrolled use no lock on the path that operates
	// for each packet, when no PID enrollment is active. That is the usual
	// condition. Refer to the comment on pidEnrolled for the true regression that
	// this corrects. Only the slow path of pidEnrolled, when scoping is truly
	// active, and SetEnrolledPIDs use pidScopeMu and enrolledPIDs.
	pidScopingActive atomic.Bool
	pidScopeMu       sync.Mutex
	enrolledPIDs     map[uint32]bool // nil/empty => no PID narrowing (see PIDScoper's doc comment)

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
		// This is a true warning at start now, and not only a comment.
		//
		// A finding on 2026-07-28 gave the cause. A host with a wildcard was absent
		// from the packet filter of transparent mode, which uses IP addresses, and
		// no message showed this. That was the root cause: Glider never redirected
		// the completion call of cursor-agent. This is separate from the correction
		// to the timing of tcpTable in ownerPID, and it is in addition to that
		// correction.
		//
		// The warning gives the exact patterns with the problem. Thus a person does
		// not have to find this again by a read of the source of resolveAllowHosts.
		// That was necessary each time that a host with a wildcard did not operate
		// in transparent mode.
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

// detectPrimaryLocalIP finds the true local IP address of this machine that
// can route traffic. It is not 127.0.0.1. The function asks the operating
// system which interface it uses to reach an external address. It sends no
// packet, because a UDP "connect" only prepares the local routing state.
//
// Glider writes this address of a true interface as the destination of a
// redirected packet, and not the loopback address. This is the standard NAT
// method for WinDivert.
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

// Stop must remove each result of Start. No state can stay in the kernel.
//
// A process that stops before it calls Stop leaves the diversion rule of the
// kernel active, and no code listens at the other end. That condition is
// worse than no operation, and a live test observed it on 2026-07-26. The
// contract of this method exists to prevent that condition, when code can
// call the method.
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

// buildWinDivertFilter agrees with two classes of traffic. The forward class
// goes from a client to a server on the allowlist, on a port on the
// allowlist. The return class is the reply traffic of Glider, out of
// listenPort.
//
// The limit by process (AllowProcessNames) operates in Go, inside
// handlePacket. It is not in the filter text of WinDivert. The filter
// language of the NETWORK layer in WinDivert has no idea of the process that
// owns a connection. Only the FLOW layer and the SOCKET layer have it. To mix
// the layers here would be a much larger change than this correction needs.
//
// Therefore the filter on the IP address and the port is the first pass, and
// it is cheap and in the kernel. The test of the process that owns the
// connection is the second pass, in user space.
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

		// The next Recv uses buf and addr again. Copy them before you give them to a
		// worker. Thus a goroutine that operates at the same time never reads memory
		// that this loop writes over.
		pktCopy := make([]byte, n)
		copy(pktCopy, buf[:n])
		addrCopy := make([]byte, windivertAddrBufSize)
		copy(addrCopy, addr)

		shard := r.shardFor(pktCopy)
		select {
		case r.jobs[shard] <- packetJob{pkt: pktCopy, addr: addrCopy}:
		default:
			r.statQueueFull.Add(1)
			// Block, and do not discard. To discard a packet here stops that connection
			// fully. A queue that is full is exactly the signal that the periodic log of
			// statsLoop must show. It is not a cause to discard traffic with no
			// message.
			select {
			case r.jobs[shard] <- packetJob{pkt: pktCopy, addr: addrCopy}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// shardFor selects a worker. Therefore one goroutine processes each packet of
// one client connection in sequence, in both directions. Different
// connections can operate fully at the same time across the workers.
//
// This is the correction for the stop on 2026-07-26, and it adds no incorrect
// sequence to the packets of one connection.
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

// handlePacket changes a packet that agrees with the filter, and then it
// injects the packet again. handlePacket injects a packet again with no
// change in two conditions. The first: the packet is not a correct IPv4 and
// TCP segment. The second: it agrees with neither the forward shape nor the
// return shape that the filter promises.
//
// Each path in the code ends with an injection. With flags=0, the driver
// already removed this packet from the stack. Therefore a failure to send the
// packet again discards the traffic of that connection. A defect here must
// never remove a packet with no message.
//
// The true decision about the route is in classifyPacket, in
// packet_classify.go, which each platform redirector uses. This function only
// parses the packet, selects the action from the decision, and does the
// change of the bytes and the injection for this platform.
//
// The two functions stay separate on purpose. classifyPacket decides that a
// process is permitted one time for each flow, and not one time for each
// packet. That behaviour was the true correction for a live incident on
// 2026-07-30. cursor-agent needed some reconnections, with no message, before
// a plain completion call succeeded. The cause was a test of ownerPID for
// each packet, and that test had a race. This is exactly the code that needs
// usual go test coverage, and not only a live test that needs WinDivert and
// Administrator permission.
func (r *WinDivertRedirector) handlePacket(pkt []byte, addr []byte) {
	parsed := parseIPv4TCP(pkt)
	decision := classifyPacket(parsed, r.listenPort, r.localIP.String(), r.matchPorts, r.allowIPs, r.flows, &r.mu, time.Now(), func() bool {
		return r.processAllowed(parsed.srcPort)
	})

	switch decision.Action {
	case actionReturn:
		copy(pkt[12:16], net.ParseIP(decision.ReturnHost).To4())
		binary.BigEndian.PutUint16(pkt[parsed.ihl:parsed.ihl+2], uint16(decision.ReturnPort))
		r.statReturned.Add(1)
		r.recalcAndReinject(pkt, addr)

	case actionRedirect:
		copy(pkt[16:20], r.localIP.To4())
		binary.BigEndian.PutUint16(pkt[parsed.ihl+2:parsed.ihl+4], uint16(r.listenPort))
		r.statRedirected.Add(1)
		r.recalcAndReinject(pkt, addr)

	case actionRejectProcess:
		r.statRejectedProc.Add(1)
		r.reinject(pkt, addr) // matched by IP/port, rejected by owning process — pass through untouched

	case actionUnmatched:
		r.statUnmatched.Add(1)
		r.reinject(pkt, addr)

	default: // actionReinjectUnchanged: malformed packet, or a return packet for an unknown/expired flow
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

	// A fast path, before any lookup of a process name.
	//
	// Glider itself dials the origin for a passthrough, and it dials an upstream
	// server. blindTunnel and mitmSession do this. Those are true outbound
	// connections to the same hosts on the allowlist. Therefore they come to
	// this same filter. This is expected, and it is not an anomaly.
	//
	// A live test confirmed the quantity on 2026-07-26: one active passthrough
	// connection of Glider made more than 1300 packets in some seconds.
	//
	// The full lookup uses OpenProcess and QueryFullProcessImageNameW, and it
	// also wrote one Debug log line. For each of those packets, that was true
	// and unnecessary work. It was on the path of Glider that operates most
	// frequently. The lookup uses a cache, but that cache is still a map with a
	// lock.
	//
	// A comparison of two PIDs is much cheaper. Therefore it goes first.
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
		return false
	}

	if !r.pidEnrolled(pid) {
		r.Log.Debug("mitm transparent: rejecting non-enrolled process", "port", srcPort, "pid", pid, "process", name)
		return false
	}
	return true
}

// pidEnrolled says if pid passes the PID scoping.
//
// It is always true when no enrollment is active. That condition occurs when
// no code called SetEnrolledPIDs, or when code called it with an empty set.
// This agrees with the convention of AllowProcessNames: with no configured
// names, there is no narrow selection. Therefore the two controls operate in
// the same way.
//
// This corrects a true regression, and a live test confirmed it on
// 2026-07-31.
//
// The first version of this method always took pidScopeMu, before it examined
// if any enrollment was active. That was a true and measurable loss of speed
// on this path, which operates for each packet.
//
// The comment beside it on processAllowed already gives the measure of "very
// frequent" here. One active passthrough connection made more than 1300
// packets in some seconds. This method operates for each packet on each
// connection of a vendor CLI that the code recognizes, with an enrollment and
// with no enrollment.
//
// pidScopingActive is an atomic.Bool, and the code reads it with no lock.
// That lets the usual condition, where no person configured an enrollment,
// use no mutex. Before, the code took a mutex for each packet, for a test
// that would give true in any condition.
func (r *WinDivertRedirector) pidEnrolled(pid uint32) bool {
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

// SetEnrolledPIDs implements PIDScoper. Refer to the comment on that interface
// for the cause.
//
// It copies the input. Therefore a later change to the slice of the caller
// cannot reach the state of the redirector.
//
// The sequence of the two writes is important:
//
//   On an enroll, the code sets pidScopingActive last. Thus no code sees an
//   enrolledPIDs map that is only half full.
//   On a disable, the code clears pidScopingActive first. Thus no code sees an
//   old map that is not empty, through a flag that is still true.
//
// The fast path of pidEnrolled trusts the flag only. Therefore this sequence is
// what keeps that fast path correct.
func (r *WinDivertRedirector) SetEnrolledPIDs(pids []uint32) {
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

	// The cache has no value, or the value is old. Refresh it, and try again a
	// small number of times if the port is still absent from a new snapshot.
	//
	// The first version of this function, before 2026-07-28, did one refresh. A
	// person gave this cause: "connect() fills the table before any packet
	// leaves".
	//
	// A live test on that day proved that the cause is incorrect in practice.
	//
	// In a true run of cursor-agent with -p, the code always missed the early
	// connections. Those are its calls to the account plane, and the completion
	// call itself. But the code always caught a LATE connection from the same
	// process, which has a short life. That was a telemetry call near the exit.
	//
	// The code misses the early connections and catches the late ones. That
	// division is exactly the signal of a true delay in the kernel. That delay
	// is between connect() and the moment when GetExtendedTcpTable shows the
	// connection.
	//
	// It is not a permanent difference, and it is not a problem in the config. A
	// person confirmed the allowlists for the hosts and for the process names
	// independently, for the same test.
	//
	// Some new attempts, some milliseconds apart, cost nothing on the path that
	// operates most frequently, because they operate only after a miss. And they
	// go directly to that period of delay.
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

// sweepStaleFlows removes each entry in the flow table that no code used. Such
// an entry is a connection that the code redirected at the level of the
// packets, but which never sent a second packet.
//
// This is the policy for removal that §5 and §9 of the design document gave as
// an open item.
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

// ResolveOriginalDestination implements OriginResolver. It finds the entry in
// the flow table that the code made at the time of the rewrite. The remote
// address of the client is the key.
//
// The code leaves that entry in the table, and it does not delete it. The
// rewrite of the return direction in handlePacket needs the same entry, for the
// full time that the connection stays open. It does not need the entry one time
// at the accept.
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

// The lookups for the PID that owns a TCP connection, and for the name of
// that process, are in internal/procinfo now. They are FetchTCPOwnerTable and
// ProcessImageBasename. A person moved them there on 2026-07-26. The HTTP
// server of the gateway also finds the origin process, for the scope of the
// workspace directory. Refer to WorkspaceStore in internal/vendors. Therefore
// both users share exactly one implementation, and there is no second copy
// that can become different.
