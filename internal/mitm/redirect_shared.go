package mitm

import (
	"net"
	"strings"
)

// resolveAllowHosts resolves each concrete hostname in hosts to its current
// IPv4 addresses. "*.domain" wildcard entries are skipped (no single
// concrete IP to resolve). Unresolvable hosts are skipped, not fatal.
// Shared by every platform's TransparentRedirector.Start: the AllowHosts →
// IP-allowlist step is pure DNS resolution, nothing OS-specific about it.
func resolveAllowHosts(hosts []string) []string {
	var ips []string
	seen := make(map[string]bool)
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if h == "" || strings.HasPrefix(h, "*.") {
			continue
		}
		addrs, err := net.LookupIP(h)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if v4 := a.To4(); v4 != nil {
				s := v4.String()
				if !seen[s] {
					seen[s] = true
					ips = append(ips, s)
				}
			}
		}
	}
	return ips
}

// wildcardHosts returns the "*.domain" entries in hosts — the ones
// resolveAllowHosts above silently skips, since a wildcard has no single
// concrete IP to add to an IP-based allowlist on any platform.
func wildcardHosts(hosts []string) []string {
	var out []string
	for _, h := range hosts {
		h = strings.TrimSpace(h)
		if strings.HasPrefix(h, "*.") {
			out = append(out, h)
		}
	}
	return out
}

// expandProcessNameCandidates lowercases each configured process name and,
// for script-wrapped CLIs (.cmd/.bat/.ps1 — cursor-agent.cmd is the known
// live Windows case), also adds "node.exe" as a best-effort fallback: a
// wrapper script doesn't itself own the TCP connection, the interpreter it
// spawns does, and for a Node-based CLI that's node.exe. Documented as an
// approximation, not a general solution for arbitrary wrapper chains. The
// .cmd/.bat/.ps1 suffixes never match on non-Windows process names, so
// this is a harmless no-op fallback there — kept shared rather than
// duplicated so the matching semantics can't drift between platforms.
func expandProcessNameCandidates(names []string) map[string]bool {
	out := make(map[string]bool, len(names))
	for _, n := range names {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		out[n] = true
		if strings.HasSuffix(n, ".cmd") || strings.HasSuffix(n, ".bat") || strings.HasSuffix(n, ".ps1") {
			out["node.exe"] = true
		}
	}
	return out
}

// mapKeys returns the keys of a string-set map — a small logging/display
// helper shared by every platform's Start().
func mapKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
