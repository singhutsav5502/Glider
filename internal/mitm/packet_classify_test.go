package mitm

import (
	"encoding/binary"
	"net"
	"sync"
	"testing"
	"time"
)

// buildIPv4TCP constructs a minimal, well-formed IPv4/TCP packet with the
// given addresses/ports — just enough for parseIPv4TCP/classifyPacket to
// classify (payload and checksums are irrelevant to routing decisions).
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
// regression test for the 2026-07-30 live incident: re-checking
// processAllowed on every packet of an already-established flow (instead
// of once, at the flow's first packet) let a single transient miss on
// ownerPID's racy, time-windowed OS TCP-table snapshot silently misroute
// one mid-connection packet, indistinguishable from a dropped connection
// to the real client. If this test ever starts failing because
// processAllowed gets called again for a known flow, that incident is
// back.
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

func TestClassifyPacket_ProcessRejected_NewFlowOnly(t *testing.T) {
	flows := map[string]flowEntry{}
	matchPorts := map[uint16]bool{443: true}
	allowIPs := map[string]bool{"93.184.216.34": true}

	rejected := func() bool { return false }
	pkt := parsedPacket{ok: true, srcIP: "10.0.0.5", dstIP: "93.184.216.34", srcPort: 51234, dstPort: 443}

	decision := classifyPacket(pkt, 9999, "10.0.0.1", matchPorts, allowIPs, flows, &sync.Mutex{}, time.Now(), rejected)
	if decision.Action != actionRejectProcess {
		t.Fatalf("got %+v", decision)
	}
	if _, ok := flows["10.0.0.5:51234"]; ok {
		t.Fatal("a rejected flow must not be recorded — the next packet has to be re-evaluated, not silently redirected later")
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
