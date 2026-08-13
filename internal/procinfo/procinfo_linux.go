//go:build linux

package procinfo

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func newPlatformResolver() Resolver { return linuxResolver{} }

type linuxResolver struct{}

// Resolve finds the PID that owns a local port. It uses the same method as `ss`
// and `netstat`:
//
//   1. /proc/net/tcp gives the map from the local port to the inode of the
//      socket.
//   2. A scan of /proc/<pid>/fd/* finds a symbolic link that agrees with
//      "socket:[<inode>]". That gives the process which holds the socket open.
//
// This function keeps no cache. The contract is the same as for Resolve in
// procinfo_windows.go. A caller on a path that operates frequently, such as a
// redirector in internal/mitm, makes its own cache.
func (linuxResolver) Resolve(localPort uint16) (ProcessInfo, bool) {
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

// FetchTCPOwnerTable gives a copy of the map from a local port to the PID that
// owns it, for each current IPv4 TCP connection.
//
// It is exported for the same cause as the equal function on Windows: the
// transparent redirector for Linux in internal/mitm needs its own lookups with
// a cache and a TTL. That policy is different from the stateless Resolve of
// this package.
//
// The function reads /proc two times and joins the results on the inode of the
// socket. /proc/net/tcp gives the local port and the inode for each socket in
// the TCP table of the kernel, for each process and each state. A scan of
// /proc/<pid>/fd/* for each visible PID gives the inode and the PID, because it
// compares the target of each symbolic link with "socket:[<inode>]".
//
// With no elevated permission, /proc/<pid>/fd shows only the sockets that the
// calling user owns, or that root owns. That is the same scope of "processes of
// the same user only" that OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION) has
// on Windows. This code adds no new limit.
func FetchTCPOwnerTable() (map[uint16]uint32, error) {
	portToInode, err := parseProcNetTCP("/proc/net/tcp")
	if err != nil {
		return nil, err
	}
	if len(portToInode) == 0 {
		return map[uint16]uint32{}, nil
	}

	inodeToPID, err := scanProcFDInodes()
	if err != nil {
		return nil, err
	}

	out := make(map[uint16]uint32, len(portToInode))
	for port, inode := range portToInode {
		if pid, ok := inodeToPID[inode]; ok {
			out[port] = pid
		}
	}
	return out, nil
}

// parseProcNetTCP reads /proc/net/tcp's fixed-column text format:
//
//	sl  local_address rem_address   st tx_queue:rx_queue tr:tm->when retrnsmt   uid  timeout inode ...
//	0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 ...
//
// local_address is "<hex IP>:<hex port>" — only the port half is needed
// here (matching Windows' table, which is keyed by local port only).
// Every row is included regardless of connection state (st column),
// mirroring TCP_TABLE_OWNER_PID_ALL on the Windows side, not just LISTEN
// or ESTABLISHED.
func parseProcNetTCP(path string) (map[uint16]uint64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	out := map[uint16]uint64{}
	scanner := bufio.NewScanner(f)
	first := true
	for scanner.Scan() {
		if first {
			first = false // header row
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 {
			continue
		}
		localAddr := fields[1]
		colon := strings.IndexByte(localAddr, ':')
		if colon < 0 {
			continue
		}
		portHex := localAddr[colon+1:]
		port, err := strconv.ParseUint(portHex, 16, 16)
		if err != nil {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil || inode == 0 {
			continue
		}
		out[uint16(port)] = inode
	}
	return out, scanner.Err()
}

// scanProcFDInodes walks /proc/<pid>/fd for every visible PID and returns
// socket-inode -> PID for every fd that is actually a socket. Processes
// this call cannot list fds for (permission denied — a different user's
// process without root) are skipped rather than treated as an error: a
// partial table is still useful, and this is exactly as visible as
// `lsof`/`ss` are without elevated privileges.
func scanProcFDInodes() (map[uint64]uint32, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	out := map[uint64]uint32{}
	for _, e := range entries {
		pid64, err := strconv.ParseUint(e.Name(), 10, 32)
		if err != nil {
			continue // not a PID directory (self, cmdline, etc.)
		}
		pid := uint32(pid64)
		fdDir := filepath.Join("/proc", e.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue // permission denied or the process exited mid-scan
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
				continue
			}
			inodeStr := target[len("socket:[") : len(target)-1]
			inode, err := strconv.ParseUint(inodeStr, 10, 64)
			if err != nil {
				continue
			}
			out[inode] = pid
		}
	}
	return out, nil
}

// ProcessImageBasename resolves a PID to its executable's basename
// (lowercased), via the /proc/<pid>/exe symlink — the Linux equivalent of
// QueryFullProcessImageNameW. Exported for the same reason as
// FetchTCPOwnerTable above.
func ProcessImageBasename(pid uint32) (string, error) {
	target, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil {
		return "", err
	}
	return strings.ToLower(filepath.Base(target)), nil
}

// ProcessAlive reports whether pid currently belongs to a running process.
// See the Windows implementation for why this exists and why "cannot tell"
// resolves to TRUE.
func ProcessAlive(pid uint32) bool {
	if pid == 0 {
		return false
	}
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}
