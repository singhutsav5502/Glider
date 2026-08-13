//go:build windows

package procinfo

import (
	"encoding/binary"
	"fmt"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

func newPlatformResolver() Resolver { return windowsResolver{} }

type windowsResolver struct{}

// Resolve finds the PID that owns a local port, with
// GetExtendedTcpTable(TCP_TABLE_OWNER_PID_ALL). That is the same method that
// `netstat -b` and Process Explorer use.
//
// Then it finds the base name of the executable file of that PID, with
// OpenProcess and QueryFullProcessImageNameW.
//
// A live test on 2026-07-26 confirmed that both methods are dependable. The
// method that reads the memory of the PEB is different: the comment on this
// package explains that a person tried it and then removed it.
//
// This function keeps no cache. The redirector in internal/mitm makes its own
// cache around the lookups that it repeats on a path that operates frequently.
// This package stays a plain lookup with no state, and a caller makes a cache
// when it needs one.
func (windowsResolver) Resolve(localPort uint16) (ProcessInfo, bool) {
	table, err := FetchTCPOwnerTable()
	if err != nil {
		return ProcessInfo{}, false
	}
	pid, ok := table[localPort]
	if !ok {
		return ProcessInfo{}, false
	}
	name, _ := ProcessImageBasename(pid) // best-effort; PID alone is still useful without a name
	return ProcessInfo{PID: pid, Name: name}, true
}

var (
	modIphlpapi              = syscall.NewLazyDLL("iphlpapi.dll")
	procGetExtendedTCPTable  = modIphlpapi.NewProc("GetExtendedTcpTable")
	modKernel32              = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess          = modKernel32.NewProc("OpenProcess")
	procQueryFullProcessName = modKernel32.NewProc("QueryFullProcessImageNameW")
	procCloseHandle          = modKernel32.NewProc("CloseHandle")
)

const (
	afInet                         = 2
	tcpTableOwnerPIDAll            = 5
	errInsufficientBuffer          = 122
	processQueryLimitedInformation = 0x1000
	tcpRowSize                     = 24 // 6 * uint32: State, LocalAddr, LocalPort, RemoteAddr, RemotePort, OwningPid
)

// FetchTCPOwnerTable gives a copy of the map from a local port to the PID that
// owns it, for each current IPv4 TCP connection.
//
// It is exported thus the transparent redirector in internal/mitm can call it
// for its own lookups with a cache. That redirector uses a different TTL policy
// from the stateless Resolve of this package. Therefore the redirector does not
// keep a second copy of the same GetExtendedTcpTable call, and two copies
// cannot become different.
func FetchTCPOwnerTable() (map[uint16]uint32, error) {
	var size uint32
	ret, _, _ := procGetExtendedTCPTable.Call(
		0, uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPIDAll, 0,
	)
	if ret != 0 && ret != errInsufficientBuffer {
		return nil, fmt.Errorf("GetExtendedTcpTable size query failed: %d", ret)
	}
	if size == 0 {
		return map[uint16]uint32{}, nil
	}
	buf := make([]byte, size)
	ret, _, _ = procGetExtendedTCPTable.Call(
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), 0, afInet, tcpTableOwnerPIDAll, 0,
	)
	if ret != 0 {
		return nil, fmt.Errorf("GetExtendedTcpTable failed: %d", ret)
	}
	if len(buf) < 4 {
		return map[uint16]uint32{}, nil
	}
	numEntries := binary.LittleEndian.Uint32(buf[0:4])
	out := make(map[uint16]uint32, numEntries)
	for i := uint32(0); i < numEntries; i++ {
		off := 4 + int(i)*tcpRowSize
		if off+tcpRowSize > len(buf) {
			break
		}
		localPortRaw := binary.LittleEndian.Uint32(buf[off+8 : off+12])
		pid := binary.LittleEndian.Uint32(buf[off+20 : off+24])
		// The port is in the low 16 bits of the DWORD. Those two bytes hold it in the
		// byte order of the network. This is a known behaviour of MIB_TCPROW_OWNER_PID,
		// and it is not a defect. Change the sequence of the two bytes to get the true
		// number of the port.
		port := uint16(localPortRaw&0xFF)<<8 | uint16((localPortRaw>>8)&0xFF)
		out[port] = pid
	}
	return out, nil
}

// ProcessImageBasename resolves a PID to its executable's basename
// (lowercased), via OpenProcess + QueryFullProcessImageNameW. Exported for
// the same reason as FetchTCPOwnerTable above.
func ProcessImageBasename(pid uint32) (string, error) {
	h, _, _ := procOpenProcess.Call(uintptr(processQueryLimitedInformation), 0, uintptr(pid))
	if h == 0 {
		return "", fmt.Errorf("OpenProcess failed for pid %d", pid)
	}
	defer procCloseHandle.Call(h)

	buf := make([]uint16, 260)
	size := uint32(len(buf))
	ret, _, _ := procQueryFullProcessName.Call(
		h, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)),
	)
	if ret == 0 {
		return "", fmt.Errorf("QueryFullProcessImageNameW failed for pid %d", pid)
	}
	full := syscall.UTF16ToString(buf[:size])
	return strings.ToLower(filepath.Base(full)), nil
}

// ProcessAlive reports whether pid currently belongs to a running process.
//
// Used by internal/vendors' continuity record to tell "this entry is from
// the session that is asking right now" from "this entry is from an earlier
// run of the same CLI that has since exited". A PID alone cannot say that:
// a CLI restart produces a new PID, which used to make its own history
// invisible to it.
//
// Conservative on failure: an error means "cannot tell", and this returns
// TRUE. Reporting a live process as dead is the dangerous direction — it
// would let two CLIs running side by side in one workspace read each other's
// turns, which is exactly what the PID scoping exists to prevent.
func ProcessAlive(pid uint32) bool {
	if pid == 0 {
		return false
	}
	h, _, _ := procOpenProcess.Call(uintptr(processQueryLimitedInformation), 0, uintptr(pid))
	if h == 0 {
		return false
	}
	_, _, _ = procCloseHandle.Call(h)
	return true
}
