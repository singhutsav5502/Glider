//go:build linux

package mitm

import (
	"context"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
)

// requireRoot skips a test that needs real iptables access instead of
// failing outright — this suite is meant to be runnable (and skip
// cleanly) in a normal unprivileged `go test ./...`. It probes the actual
// capability (can "sudo -n iptables" run at all — either because this
// process is already root, in which case sudo is a no-op passthrough, or
// because a scoped NOPASSWD sudoers rule for iptables exists), not EUID
// directly: runIPTables always shells out via "sudo -n iptables ..." (see
// its own doc comment), so the test binary itself is meant to run
// unprivileged, with only that one command elevated.
func requireRoot(t *testing.T) {
	t.Helper()
	if err := exec.Command("sudo", "-n", "iptables", "-t", "nat", "-L", "-n").Run(); err != nil {
		t.Skipf("requires passwordless sudo access to iptables — got: %v", err)
	}
}

// TestLinuxRedirector_EndToEnd_RedirectsAndResolvesOriginalDestination is
// the real test of the two riskiest, least-guessable pieces of this
// implementation: does the iptables REDIRECT rule actually redirect, and
// does SO_ORIGINAL_DST actually recover the real destination correctly
// (including the network-byte-order port fix)? Fully self-contained — no
// dependency on real internet connectivity: a local "fake origin"
// listener stands in for the real destination, so a passing test proves
// the redirect mechanism itself works, not that DNS/routing happened to
// cooperate.
func TestLinuxRedirector_EndToEnd_RedirectsAndResolvesOriginalDestination(t *testing.T) {
	requireRoot(t)

	// The fake "real origin" — if the REDIRECT works, this listener must
	// NEVER actually receive a connection; everything should land on
	// Glider's own listener below instead.
	fakeOrigin, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer fakeOrigin.Close()
	fakeOriginPort := fakeOrigin.Addr().(*net.TCPAddr).Port
	go func() {
		conn, err := fakeOrigin.Accept()
		if err == nil {
			t.Errorf("fake origin received a direct connection — REDIRECT did not intercept it")
			conn.Close()
		}
	}()

	// Glider's own transparent-ingress listener.
	gliderLn, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer gliderLn.Close()
	listenPort := gliderLn.Addr().(*net.TCPAddr).Port

	r := &LinuxRedirector{Log: slog.Default()}
	cfg := RedirectConfig{
		ListenPort: listenPort,
		MatchPorts: []int{fakeOriginPort},
		AllowHosts: []string{"127.0.0.1"},
	}
	if err := r.Start(context.Background(), cfg); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		if err := r.Stop(); err != nil {
			t.Errorf("Stop: %v", err)
		}
	}()

	// The actual client connection this whole test is exercising: dial
	// the "real origin" address — the kernel's REDIRECT rule should
	// rewrite this to Glider's listener before the handshake completes.
	clientDone := make(chan error, 1)
	go func() {
		conn, err := net.DialTimeout("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(fakeOriginPort)), 3*time.Second)
		if err != nil {
			clientDone <- err
			return
		}
		defer conn.Close()
		time.Sleep(200 * time.Millisecond) // hold it open long enough for the accept side to inspect it
		clientDone <- nil
	}()

	_ = gliderLn.(*net.TCPListener).SetDeadline(time.Now().Add(3 * time.Second))
	accepted, err := gliderLn.Accept()
	if err != nil {
		t.Fatalf("Glider's own listener never received the redirected connection: %v", err)
	}
	defer accepted.Close()

	host, port, err := r.ResolveOriginalDestination(accepted)
	if err != nil {
		t.Fatalf("ResolveOriginalDestination: %v", err)
	}
	if host != "127.0.0.1" || port != fakeOriginPort {
		t.Fatalf("got %s:%d, want 127.0.0.1:%d (the fake origin's real address)", host, port, fakeOriginPort)
	}

	if err := <-clientDone; err != nil {
		t.Fatalf("client dial failed: %v", err)
	}
}

// TestLinuxRedirector_ConnectionAllowed_RejectsSelfTraffic proves the
// self-PID exclusion (mirroring WinDivertRedirector's statSelfTraffic
// fast path) actually works against a real connection: dialing from
// inside this very test process must be recognized and rejected, the
// same way Glider's own outbound passthrough/upstream dials must never
// be treated as "vendor CLI traffic" worth MITM'ing.
func TestLinuxRedirector_ConnectionAllowed_RejectsSelfTraffic(t *testing.T) {
	requireRoot(t) // ConnectionAllowed's ownerPID path shells out to /proc via procinfo, no iptables needed, but keep the suite's privilege story consistent

	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	client, err := net.DialTimeout("tcp4", ln.Addr().String(), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var serverSide net.Conn
	select {
	case serverSide = <-accepted:
	case <-time.After(3 * time.Second):
		t.Fatal("accept timed out")
	}
	defer serverSide.Close()

	r := &LinuxRedirector{
		Log:               slog.Default(),
		selfPID:           uint32(os.Getpid()),
		allowProcessNames: map[string]bool{"whatever-vendor-cli": true},
		pidNameCache:      map[uint32]string{},
	}
	// serverSide's remote address is the client socket's local port —
	// i.e. this same test process's own outbound connection.
	if r.ConnectionAllowed(serverSide) {
		t.Fatal("expected self-traffic to be rejected, got allowed")
	}
}
