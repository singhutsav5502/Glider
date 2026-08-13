package mitm

import (
	"encoding/binary"
	"net"
	"strconv"
	"sync"
	"time"
)

// flowEntry records the true original destination for one client "ip:port". A
// forward packet went away from that destination. A return packet later has
// the local listener as its source. Therefore the code can change it back.
// That packet then appears to come from the true destination, and not from
// the loopback listener of Glider. Each platform implementation of
// TransparentRedirector uses this. Refer to classifyPacket below.
type flowEntry struct {
	host       string // original real destination, dotted-quad — meaningless when rejected is true
	port       int    // meaningless when rejected is true
	insertedAt time.Time
	// rejected records that the first packet of this flow failed processAllowed.
	//
	// Refer to the comment on classifyPacket for the live incident on 2026-07-31
	// that this field prevents. Without the field, a flow with a first packet
	// that failed got NO entry. Therefore the code examined the next packet on
	// the same client ip:port again from the start. A snapshot of the TCP table
	// of the operating system has a race, and it can become current. If
	// processAllowed then succeeded, that LATER packet went to the listener of
	// Glider. But the FIRST packet had already gone directly to the true
	// destination.
	//
	// One TCP connection divided across two different paths looks the same as a
	// connection that is lost, to the client and to the true origin. That is
	// exactly the symptom of "some reconnections with no message".
	//
	// The first correction for a sticky flow, on 2026-07-30, solved one half
	// only. It made an ACCEPT decision sticky. But the code examined a REJECT
	// decision again each time. That is the same class of defect that the
	// correction for the accept side closed, but on the other branch.
	rejected bool
}

// parsedPacket holds the few fields of the IPv4 and TCP headers that
// classifyPacket needs. The code extracts them one time. Therefore the logic
// of the decision never reads a raw slice of bytes.
//
// The code keeps ihl, which is the length of the IP header in bytes. A caller
// can decide to change the destination of the packet. That caller then knows
// the position where the TCP header starts, and thus the position of the port
// fields.
type parsedPacket struct {
	srcIP   string
	dstIP   string
	srcPort uint16
	dstPort uint16
	ihl     int
	ok      bool // false if pkt isn't a sane IPv4/TCP segment worth classifying
}

// parseIPv4TCP extracts the fields of parsedPacket from a raw IP packet. It
// returns ok=false for each packet that is not IPv4 and TCP with a full
// header. This is exactly the same test of the shape that handlePacket did in
// its own code before.
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
	// actionReinjectUnchanged covers two conditions. The first is a packet with
	// an incorrect form. The second is a packet with the shape of a return, for
	// a flow that classifyPacket does not know. That flow is expired, or the
	// code never redirected it. In both conditions, inject the packet again with
	// no change and increase no counter. This agrees exactly with the early
	// return of the first handlePacket.
	actionReinjectUnchanged packetAction = iota
	// actionReturn: change the destination to ReturnHost and ReturnPort. That is
	// the true origin that a forward packet went away from. Therefore the TCP
	// stack of the client sees the replies from the peer that it believes it
	// connected to.
	actionReturn
	// actionRedirect: rewrite dst to the local listener so this
	// connection's bytes reach Glider's MITM/blind-tunnel handling.
	actionRedirect
	// actionRejectProcess: the IP address and the port agree, but the process that
	// owns the connection is not in AllowProcessNames. Send the packet through
	// with no change.
	//
	// This decision is sticky for each flow, as actionRedirect is. Refer to
	// flowEntry.rejected. A decision to reject must continue for the full life of
	// the connection. If it does not, a later packet on the same flow can go to a
	// redirect, and that divides one TCP connection across two destinations.
	actionRejectProcess
	// actionUnmatched: does not match any known shape (not the return
	// port, not an allowlisted destination) — pass through untouched.
	actionUnmatched
)

type packetDecision struct {
	Action     packetAction
	ReturnHost string // set only when Action == actionReturn
	ReturnPort int
}

// classifyPacket is the decision about the route that handlePacket needs. It
// is a separate function. It operates on three items. The first is the fields
// that the code already parsed. The second is the fixed config of the
// redirector: listenPort, localIP, matchPorts and allowIPs. The third is its
// live cache of flows. It has no handle for WinDivert, no raw socket, and it
// makes no system call.
//
// Each platform redirector uses it: Windows with WinDivert today, and Linux
// with nftables later. Therefore the sticky behaviour for a flow below, and
// the history of its defects, is in exactly one position. No person must
// derive it again for each platform, and thus no person can break it again.
//
// processAllowed is a callback, and it is not a bool that the code already
// resolved. This is on purpose: classifyPacket then calls it only when a
// decision truly needs it, for a flow that is new. Refer to the second
// condition below.
//
// This is the true correction from 2026-07-30. Refer to the comment on
// handlePacket in redirector_windows.go for the full record of the incident.
// The code tested the owner of the process for each packet of a flow that
// already existed. It did not make one decision and then trust it. That was
// the cause of a live problem: cursor-agent needed some reconnections, with
// no message, before a plain completion call succeeded.
//
// A test that says that processAllowed operates exactly one time across some
// packets of the same flow truly says that this correction stays.
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
				// Sticky reject, not just sticky accept — see flowEntry.rejected's
				// own doc comment for the real incident this closes.
				flowsMu.Lock()
				flows[key] = flowEntry{insertedAt: now, rejected: true}
				flowsMu.Unlock()
				return packetDecision{Action: actionRejectProcess}
			}
			flowsMu.Lock()
			flows[key] = flowEntry{host: pkt.dstIP, port: int(pkt.dstPort), insertedAt: now}
			flowsMu.Unlock()
			return packetDecision{Action: actionRedirect}
		}
		if entry.rejected {
			return packetDecision{Action: actionRejectProcess}
		}
		return packetDecision{Action: actionRedirect}

	default:
		return packetDecision{Action: actionUnmatched}
	}
}
