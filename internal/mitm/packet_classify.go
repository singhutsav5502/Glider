package mitm

import (
	"encoding/binary"
	"net"
	"strconv"
	"sync"
	"time"
)

// flowEntry records, for one client "ip:port", the real original
// destination a forward packet was redirected away from — so a later
// return packet (source = the local listener) can be rewritten back to
// look like it came from that real destination instead of from Glider's
// own loopback listener. Shared by every platform's TransparentRedirector
// implementation (see classifyPacket below).
type flowEntry struct {
	host       string // original real destination, dotted-quad
	port       int
	insertedAt time.Time
}

// parsedPacket is the handful of IPv4/TCP header fields classifyPacket
// needs, extracted once so the decision logic itself never touches a raw
// byte slice. ihl (IP header length, in bytes) is kept so a caller that
// decides to rewrite the packet's destination knows where the TCP header
// — and therefore the port fields — actually starts.
type parsedPacket struct {
	srcIP   string
	dstIP   string
	srcPort uint16
	dstPort uint16
	ihl     int
	ok      bool // false if pkt isn't a sane IPv4/TCP segment worth classifying
}

// parseIPv4TCP extracts parsedPacket's fields from a raw IP packet, or
// reports ok=false for anything that isn't IPv4/TCP with a full header —
// exactly the same shape check handlePacket used to do inline.
func parseIPv4TCP(pkt []byte) parsedPacket {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return parsedPacket{}
	}
	ihl := int(pkt[0]&0x0f) * 4
	if len(pkt) < ihl+20 {
		return parsedPacket{}
	}
	const tcpProto = 6
	if pkt[9] != tcpProto {
		return parsedPacket{}
	}
	return parsedPacket{
		srcIP:   net.IP(append([]byte(nil), pkt[12:16]...)).String(),
		dstIP:   net.IP(append([]byte(nil), pkt[16:20]...)).String(),
		srcPort: binary.BigEndian.Uint16(pkt[ihl : ihl+2]),
		dstPort: binary.BigEndian.Uint16(pkt[ihl+2 : ihl+4]),
		ihl:     ihl,
		ok:      true,
	}
}

type packetAction int

const (
	// actionReinjectUnchanged covers both a malformed packet and a
	// return-shaped packet for a flow classifyPacket doesn't know about
	// (expired or never redirected) — reinject as-is, no stat bump,
	// matching the original handlePacket's early-return behavior exactly.
	actionReinjectUnchanged packetAction = iota
	// actionReturn: rewrite dst to ReturnHost:ReturnPort (the real origin
	// a forward packet was redirected away from), so the client's TCP
	// stack sees replies from the peer it actually thinks it connected to.
	actionReturn
	// actionRedirect: rewrite dst to the local listener so this
	// connection's bytes reach Glider's MITM/blind-tunnel handling.
	actionRedirect
	// actionRejectProcess: matched by IP/port but the owning process
	// isn't in AllowProcessNames — pass through untouched.
	actionRejectProcess
	// actionUnmatched: doesn't match any known shape (not the return
	// port, not an allowlisted destination) — pass through untouched.
	actionUnmatched
)

type packetDecision struct {
	Action     packetAction
	ReturnHost string // set only when Action == actionReturn
	ReturnPort int
}

// classifyPacket is handlePacket's routing decision, factored out as a
// standalone function over already-parsed fields, the redirector's static
// config (listenPort/localIP/matchPorts/allowIPs), and its live flow
// cache — no WinDivert handle, no raw sockets, no syscalls of its own.
// Shared by every platform's redirector (Windows/WinDivert today;
// Linux/nftables eventually) so the flow-sticky behavior below — and its
// bug history — lives in exactly one place instead of being re-derived,
// and potentially re-broken, per platform.
//
// processAllowed is a callback, not a resolved bool, specifically so
// classifyPacket only invokes it when a decision genuinely needs it: a
// brand-new flow (see the second case below). This is the actual fix from
// 2026-07-30 (see redirector_windows.go's handlePacket doc comment for
// the full incident writeup) — re-checking a process-owner lookup on
// every packet of an already-established flow, instead of deciding once
// and trusting it, was live-diagnosed as the cause of cursor-agent
// needing several silent reconnects before a plain completion call got
// through. A test asserting processAllowed is called exactly once across
// several packets on the same flow is really asserting that fix stays
// fixed, on every platform this logic ever runs on.
//
// flowsMu guards flows and is deliberately released before processAllowed
// runs, not held across it: processAllowed does real, potentially slow
// work (an OS TCP-table snapshot, with up to a few milliseconds of bounded
// retry on a cold cache, plus a process-name lookup) and flows is shared
// across every worker goroutine — holding the lock across that call would
// serialize all of them on one mutex for the duration, reintroducing the
// same shape of head-of-line stall workerQueueSize was raised to fix.
// This mirrors handlePacket's original lock/check/unlock →
// (maybe-slow check, unlocked) → lock/write/unlock shape exactly; it is
// not a simplification, it's the same concurrency contract, just testable
// without a live WinDivert handle now.
func classifyPacket(pkt parsedPacket, listenPort int, localIP string, matchPorts map[uint16]bool, allowIPs map[string]bool, flows map[string]flowEntry, flowsMu *sync.Mutex, now time.Time, processAllowed func() bool) packetDecision {
	if !pkt.ok {
		return packetDecision{Action: actionReinjectUnchanged}
	}

	switch {
	case int(pkt.srcPort) == listenPort && pkt.srcIP == localIP:
		key := pkt.dstIP + ":" + strconv.Itoa(int(pkt.dstPort))
		flowsMu.Lock()
		entry, ok := flows[key]
		if ok {
			entry.insertedAt = now // refresh TTL on every use, not just the initial forward packet
			flows[key] = entry
		}
		flowsMu.Unlock()
		if !ok {
			return packetDecision{Action: actionReinjectUnchanged}
		}
		return packetDecision{Action: actionReturn, ReturnHost: entry.host, ReturnPort: entry.port}

	case matchPorts[pkt.dstPort] && allowIPs[pkt.dstIP]:
		key := pkt.srcIP + ":" + strconv.Itoa(int(pkt.srcPort))
		flowsMu.Lock()
		entry, known := flows[key]
		if known {
			entry.insertedAt = now
			flows[key] = entry
		}
		flowsMu.Unlock()

		if !known {
			if !processAllowed() {
				return packetDecision{Action: actionRejectProcess}
			}
			flowsMu.Lock()
			flows[key] = flowEntry{host: pkt.dstIP, port: int(pkt.dstPort), insertedAt: now}
			flowsMu.Unlock()
		}
		return packetDecision{Action: actionRedirect}

	default:
		return packetDecision{Action: actionUnmatched}
	}
}
