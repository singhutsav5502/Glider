package mitm

import (
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"
)

// buildIPv4TCP makes a small and correct packet with IPv4 and TCP, with the
// addresses and the ports that a test gives. It is sufficient for parseIPv4TCP
// and classifyPacket to make a decision. The payload and the checksums have no
// importance for a decision about the route.
func buildIPv4TCP(t *testing.T, srcIP, dstIP string, srcPort, dstPort uint16) []byte {
	t.Helper()
	const ihl = 20
	pkt := make([]byte, ihl+20)
	pkt[0] = 0x45 // version 4, IHL 5 (20 bytes)
	pkt[9] = 6    // TCP
	src := net.ParseIP(srcIP).To4()
	dst := net.ParseIP(dstIP).To4()
	if src == nil || dst == nil {
		t.Fatalf("bad test IPs %q/%q", srcIP, dstIP)
	}
	copy(pkt[12:16], src)
	copy(pkt[16:20], dst)
	binary.BigEndian.PutUint16(pkt[ihl:ihl+2], srcPort)
	binary.BigEndian.PutUint16(pkt[ihl+2:ihl+4], dstPort)
	return pkt
}

func alwaysAllowed() bool { return true }

func TestParseIPv4TCP_RejectsMalformed(t *testing.T) {
	cases := map[string][]byte{
		"too short": {0x45, 0x00},
		"not IPv4":  append([]byte{0x60}, make([]byte, 39)...), // version 6
		"truncated header": func() []byte {
			p := buildIPv4TCP(t, "1.2.3.4", "5.6.7.8", 1, 2)
			return p[:25] // shorter than ihl+20
		}(),
		"not TCP": func() []byte {
			p := buildIPv4TCP(t, "1.2.3.4", "5.6.7.8", 1, 2)
			p[9] = 17 // UDP
			return p
		}(),
	}
	for name, pkt := range cases {
		t.Run(name, func(t *testing.T) {
			if got := parseIPv4TCP(pkt); got.ok {
				t.Fatalf("%s: expected ok=false, got %+v", name, got)
			}
		})
	}
}

func TestParseIPv4TCP_ExtractsFields(t *testing.T) {
	pkt := buildIPv4TCP(t, "10.0.0.5", "93.184.216.34", 51234, 443)
	got := parseIPv4TCP(pkt)
	if !got.ok {
		t.Fatal("expected ok=true")
	}
	if got.srcIP != "10.0.0.5" || got.dstIP != "93.184.216.34" || got.srcPort != 51234 || got.dstPort != 443 || got.ihl != 20 {
		t.Fatalf("got %+v", got)
	}
}

func TestClassifyPacket_MalformedReinjectsUnchanged(t *testing.T) {
	flows := map[string]flowEntry{}
	decision := classifyPacket(parsedPacket{ok: false}, 9999, "10.0.0.1", nil, nil, flows, &sync.Mutex{}, time.Now(), alwaysAllowed)
	if decision.Action != actionReinjectUnchanged {
		t.Fatalf("got %+v", decision)
	}
}

func TestClassifyPacket_NewFlow_RedirectsAndCallsProcessAllowedOnce(t *testing.T) {
	flows := map[string]flowEntry{}
	matchPorts := map[uint16]bool{443: true}
	allowIPs := map[string]bool{"93.184.216.34": true}

	calls := 0
	allowed := func() bool { calls++; return true }

	pkt := parsedPacket{ok: true, srcIP: "10.0.0.5", dstIP: "93.184.216.34", srcPort: 51234, dstPort: 443}
	decision := classifyPacket(pkt, 9999, "10.0.0.1", matchPorts, allowIPs, flows, &sync.Mutex{}, time.Now(), allowed)

	if decision.Action != actionRedirect {
		t.Fatalf("got %+v", decision)
	}
	if calls != 1 {
		t.Fatalf("processAllowed called %d times on a new flow, want 1", calls)
	}
	if _, ok := flows["10.0.0.5:51234"]; !ok {
		t.Fatal("expected a flow entry to be recorded for the client's own ip:port")
	}
}

// TestClassifyPacket_KnownFlow_NeverRechecksProcessAllowed is the direct
// regression test for the live incident from 2026-07-30.
//
// The code tested processAllowed for each packet of a flow that already existed.
// It must test it one time, at the first packet of the flow.
//
// ownerPID reads a snapshot of the TCP table of the operating system. That
// snapshot has a race and it has a time window. Therefore one failure of that
// lookup sent one packet in the middle of a connection to the incorrect
// destination, and it gave no message. To the true client, that condition looks
// the same as a connection that is lost.
//
// If this test fails one day, because the code calls processAllowed again for a
// flow that it knows, then that incident has returned.
func TestClassifyPacket_KnownFlow_NeverRechecksProcessAllowed(t *testing.T) {
	flows := map[string]flowEntry{}
	matchPorts := map[uint16]bool{443: true}
	allowIPs := map[string]bool{"93.184.216.34": true}
	var mu sync.Mutex

	calls := 0
	allowed := func() bool { calls++; return true }

	pkt := parsedPacket{ok: true, srcIP: "10.0.0.5", dstIP: "93.184.216.34", srcPort: 51234, dstPort: 443}

	// First packet: new flow, processAllowed runs once.
	if d := classifyPacket(pkt, 9999, "10.0.0.1", matchPorts, allowIPs, flows, &mu, time.Now(), allowed); d.Action != actionRedirect {
		t.Fatalf("packet 1: got %+v", d)
	}

	// A racy processAllowed that would reject this packet if it were ever
	// actually called again on the same flow.
	wouldRejectIfCalled := func() bool { calls++; return false }
	for i := 0; i < 5; i++ {
		d := classifyPacket(pkt, 9999, "10.0.0.1", matchPorts, allowIPs, flows, &mu, time.Now(), wouldRejectIfCalled)
		if d.Action != actionRedirect {
			t.Fatalf("packet %d: got %+v, want actionRedirect (flow already established)", i+2, d)
		}
	}
	if calls != 1 {
		t.Fatalf("processAllowed called %d times across 6 packets on one flow, want exactly 1 (only the first)", calls)
	}
}

// TestClassifyPacket_ProcessRejected_IsStickyAcrossLaterPackets is the direct
// regression test for a true incident, and a live test confirmed it on
// 2026-07-31.
//
// This test said the OPPOSITE before: "the code must not record a flow that it
// refuses, and it must examine the next packet again". That statement was the
// defect.
//
// With no sticky refusal, a flow whose first packet had one failure of
// processAllowed got NO entry. That failure comes from a snapshot of the TCP
// table of the operating system that has a race, and it is worse with many true
// connections at the same time.
//
// Therefore the code examined the next packet on the same client ip:port again,
// from the start. If THAT lookup succeeded, the code redirected that later
// packet.
func TestClassifyPacket_ProcessRejected_IsStickyAcrossLaterPackets(t *testing.T) {
	flows := map[string]flowEntry{}
	matchPorts := map[uint16]bool{443: true}
	allowIPs := map[string]bool{"93.184.216.34": true}
	var mu sync.Mutex

	calls := 0
	rejectedOnce := func() bool { calls++; return false }
	pkt := parsedPacket{ok: true, srcIP: "10.0.0.5", dstIP: "93.184.216.34", srcPort: 51234, dstPort: 443}

	// First packet: processAllowed misses, gets rejected — and now must
	// be recorded, not silently dropped from flows.
	if d := classifyPacket(pkt, 9999, "10.0.0.1", matchPorts, allowIPs, flows, &mu, time.Now(), rejectedOnce); d.Action != actionRejectProcess {
		t.Fatalf("packet 1: got %+v, want actionRejectProcess", d)
	}
	if entry, ok := flows["10.0.0.5:51234"]; !ok || !entry.rejected {
		t.Fatalf("expected a sticky rejected flow entry to be recorded, got %+v (ok=%v)", flows["10.0.0.5:51234"], ok)
	}

	// A processAllowed that would now ACCEPT if it were ever called again
	// — proving later packets on the same flow never re-run it and never
	// flip to actionRedirect, which is exactly the split-connection bug.
	wouldAcceptIfCalled := func() bool { calls++; return true }
	for i := 0; i < 5; i++ {
		d := classifyPacket(pkt, 9999, "10.0.0.1", matchPorts, allowIPs, flows, &mu, time.Now(), wouldAcceptIfCalled)
		if d.Action != actionRejectProcess {
			t.Fatalf("packet %d: got %+v, want actionRejectProcess (reject must stay sticky)", i+2, d)
		}
	}
	if calls != 1 {
		t.Fatalf("processAllowed called %d times across 6 packets on one rejected flow, want exactly 1 (only the first)", calls)
	}
}

func TestClassifyPacket_Unmatched(t *testing.T) {
	flows := map[string]flowEntry{}
	pkt := parsedPacket{ok: true, srcIP: "10.0.0.5", dstIP: "1.2.3.4", srcPort: 51234, dstPort: 80}
	decision := classifyPacket(pkt, 9999, "10.0.0.1", map[uint16]bool{443: true}, map[string]bool{"93.184.216.34": true}, flows, &sync.Mutex{}, time.Now(), alwaysAllowed)
	if decision.Action != actionUnmatched {
		t.Fatalf("got %+v", decision)
	}
}

func TestClassifyPacket_ReturnPath_KnownFlow(t *testing.T) {
	flows := map[string]flowEntry{
		"10.0.0.5:51234": {host: "93.184.216.34", port: 443, insertedAt: time.Now().Add(-time.Minute)},
	}
	// Return packet: source is the local listener, destination is the client.
	pkt := parsedPacket{ok: true, srcIP: "10.0.0.1", dstIP: "10.0.0.5", srcPort: 9999, dstPort: 51234}
	decision := classifyPacket(pkt, 9999, "10.0.0.1", nil, nil, flows, &sync.Mutex{}, time.Now(), alwaysAllowed)

	if decision.Action != actionReturn || decision.ReturnHost != "93.184.216.34" || decision.ReturnPort != 443 {
		t.Fatalf("got %+v", decision)
	}
	if flows["10.0.0.5:51234"].insertedAt.Before(time.Now().Add(-time.Second)) {
		t.Fatal("expected insertedAt (TTL) to be refreshed on use, not left at its original value")
	}
}

func TestClassifyPacket_ReturnPath_UnknownFlow_ReinjectsUnchanged(t *testing.T) {
	flows := map[string]flowEntry{}
	pkt := parsedPacket{ok: true, srcIP: "10.0.0.1", dstIP: "10.0.0.5", srcPort: 9999, dstPort: 51234}
	decision := classifyPacket(pkt, 9999, "10.0.0.1", nil, nil, flows, &sync.Mutex{}, time.Now(), alwaysAllowed)
	if decision.Action != actionReinjectUnchanged {
		t.Fatalf("got %+v", decision)
	}
}
