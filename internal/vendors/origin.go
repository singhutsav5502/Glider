package vendors

import (
	"net"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/glider-ai/glider/internal/procinfo"
)

// originResolver is package-level (like defaultResumeStore/
// defaultWorkspaceStore) — every HTTP-facing caller resolves against the
// same OS lookup, and procinfo.Resolver is stateless/cheap to share.
var originResolver = procinfo.NewResolver()

// resolveOriginProcess extracts and resolves the connecting process from
// an HTTP request's RemoteAddr — the shared step behind both
// ResolveOriginPID and ResolveOriginVendorName below. ok=false for a
// malformed RemoteAddr, a non-Windows host, or a lookup failure.
func resolveOriginProcess(remoteAddr string) (procinfo.ProcessInfo, bool) {
	_, portStr, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return procinfo.ProcessInfo{}, false
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return procinfo.ProcessInfo{}, false
	}
	return originResolver.Resolve(uint16(port))
}

// ResolveOriginPID extracts the connecting process's PID from an HTTP
// request's RemoteAddr — the "origin CLI" identity ResolveDelegate keys
// workspace-directory lookups on (see workspace.go's doc comment for why
// this exists at all: no wire protocol Glider intercepts carries a
// filesystem path). Returns 0 if unresolvable, and callers/ResolveDelegate
// treat 0 as "cannot resolve," not an error, since there is a defined
// fallback for that case (the old os.Getwd()-based behavior).
func ResolveOriginPID(remoteAddr string) uint32 {
	info, ok := resolveOriginProcess(remoteAddr)
	if !ok {
		return 0
	}
	return info.PID
}

// ResolveOriginVendorName identifies which REGISTERED vendor's CLI sent
// this request, from the real OS process on the other end of the
// connection — replacing a hardcoded vendor name literal (which existed
// at both HTTP-facing call sites, flagged as a known, disclosed exception
// in planning/ngl_and_adapters.md §0 before this function existed) with a
// real, per-request answer instead of always assuming "claude".
//
// "" is a safe, valid result — not an error — for an unresolvable origin
// process, or one whose executable name matches no vendor currently in
// reg (e.g. an unrelated process, or a vendor discovery has not found yet).
// Callers pass "" straight through to ngl.LastUserInstruction, which
// already treats an unrecognized vendor name as "no scaffold stripping
// needed" rather than erroring (TestLastUserInstruction_UnknownVendorNoStripping) —
// so generalizing this call site is strictly an improvement with no new
// failure mode: the previous hardcoded "claude" default was already wrong
// (a silent no-op, not a crash) for any non-claude origin, exactly like ""
// is here.
func ResolveOriginVendorName(remoteAddr string, reg Registry) string {
	info, ok := resolveOriginProcess(remoteAddr)
	if !ok {
		return ""
	}
	for _, v := range reg.Vendors {
		if strings.EqualFold(filepath.Base(v.Path), info.Name) {
			return v.Name
		}
	}
	return ""
}
