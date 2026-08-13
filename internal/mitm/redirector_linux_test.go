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

// requireRoot makes a test skip when that test needs true access to iptables.
// The test does not fail. Therefore a person can run this suite with a usual
// `go test ./...` and no privilege, and it skips cleanly.
//
// It examines the true ability: can "sudo -n iptables" operate? That is
// possible in two conditions: this process is already root, and then sudo
// changes nothing; or a sudoers rule with NOPASSWD and a small scope for
// iptables exists.
//
// It does not examine the EUID. runIPTables always uses "sudo -n iptables ..."
// in a shell. Refer to the comment on that function. Therefore the test binary
// itself must operate with no privilege, and only that one command is
// elevated.
func requireRoot(t *testing.T) {
	t.Helper()
	if err := exec.Command("sudo", "-n", "iptables", "-t", "nat", "-L", "-n").Run(); err != nil {
		t.Skipf("requires passwordless sudo access to iptables — got: %v", err)
	}
}

// TestLinuxRedirector_EndToEnd_RedirectsAndResolvesOriginalDestination
// examines the two parts of this implementation with the most risk, and which a
// person cannot estimate:
//
//   1. Does the REDIRECT rule of iptables truly redirect?
//   2. Does SO_ORIGINAL_DST truly give the correct true destination? This
//      includes the correction for the byte order of the network in the port.
//
// The test is complete in itself, and it needs no connection to the internet. A
// local listener that is a false origin replaces the true destination.
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
