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

// Resolve looks up the owning PID for a local port via
// GetExtendedTcpTable(TCP_TABLE_OWNER_PID_ALL) — the same technique
// `netstat -b` and Process Explorer use — then resolves that PID's
// executable basename via OpenProcess + QueryFullProcessImageNameW. Both
// confirmed reliable live (2026-07-26), unlike the PEB-memory-read
// approach this package's doc comment explains was tried and removed. No
// caching here (internal/mitm's redirector does its own caching around
// repeated lookups on its hot path; this package stays a plain, stateless
// lookup and lets callers cache if they need to).
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

// FetchTCPOwnerTable returns a snapshot of local-port -> owning-PID for all
// current IPv4 TCP connections. Exported so internal/mitm's transparent
// redirector can call it directly for its own cached lookups (a different
// TTL policy than this package's stateless Resolve) instead of keeping a
// second, drifting copy of the same GetExtendedTcpTable call.
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
		// The port occupies the low 16 bits of the DWORD, stored in network
		// byte order within those two bytes — a well-known MIB_TCPROW_OWNER_PID
		// quirk, not a bug: swap them to get the actual port number.
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
