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

// Resolve looks up the owning PID for a local port via the same technique
// `ss`/`netstat` use: /proc/net/tcp for the local-port -> socket-inode
// mapping, then a scan of /proc/<pid>/fd/* for a symlink matching
// "socket:[<inode>]" to find which process holds that socket open. No
// caching here — same contract as procinfo_windows.go's Resolve, callers
// on a hot path (internal/mitm's redirectors) do their own caching.
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

// FetchTCPOwnerTable returns a snapshot of local-port -> owning-PID for all
// current IPv4 TCP connections. Exported for the same reason as its
// Windows counterpart: internal/mitm's Linux transparent redirector needs
// its own TTL-cached lookups, a different caching policy than this
// package's stateless Resolve.
//
// Two /proc reads, joined on socket inode: /proc/net/tcp gives local port
// -> inode for every socket in the kernel's TCP table (any process's, any
// state), and a scan of /proc/<pid>/fd/* for every visible PID gives inode
// -> PID by matching each fd's symlink target against "socket:[<inode>]".
// Only sockets owned by the calling user (or root) are visible under
// /proc/<pid>/fd without elevated privileges — the same "same-user
// processes only" scope OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)
// has on Windows, not a new limitation introduced here.
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
// socket-inode -> PID for every fd that's actually a socket. Processes
// this call can't list fds for (permission denied — a different user's
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
