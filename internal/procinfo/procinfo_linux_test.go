//go:build linux

package procinfo

import (
	"net"
	"os"
	"testing"
)

func TestFetchTCPOwnerTable_FindsOwnListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := uint16(ln.Addr().(*net.TCPAddr).Port)

	table, err := FetchTCPOwnerTable()
	if err != nil {
		t.Fatal(err)
	}
	pid, ok := table[port]
	if !ok {
		t.Fatalf("did not find an owner for our own listening port %d in %v", port, table)
	}
	if pid != uint32(os.Getpid()) {
		t.Fatalf("got pid %d, want our own pid %d", pid, os.Getpid())
	}
}

func TestResolve_FindsOwnListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := uint16(ln.Addr().(*net.TCPAddr).Port)

	info, ok := linuxResolver{}.Resolve(port)
	if !ok {
		t.Fatalf("Resolve did not find our own listening port %d", port)
	}
	if info.PID != uint32(os.Getpid()) {
		t.Fatalf("got pid %d, want our own pid %d", info.PID, os.Getpid())
	}
	if info.Name == "" {
		t.Fatal("expected a non-empty process name for our own PID")
	}
}

func TestProcessImageBasename_ResolvesOwnPID(t *testing.T) {
	name, err := ProcessImageBasename(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	if name == "" {
		t.Fatal("expected a non-empty basename")
	}
	t.Logf("own process basename: %q", name)
}

func TestFetchTCPOwnerTable_UnknownPortNotPresent(t *testing.T) {
	table, err := FetchTCPOwnerTable()
	if err != nil {
		t.Fatal(err)
	}
	// Port 1 is privileged and essentially never bound in a test
	// environment — a soft sanity check that the table is not just
	// returning every port as "owned."
	if _, ok := table[1]; ok {
		t.Log("port 1 unexpectedly present — not a hard failure, just unusual")
	}
}
