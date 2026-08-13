package mitm

import (
	"net"
	"strings"
)

// resolveAllowHosts finds the current IPv4 addresses of each exact host name in
// hosts.
//
// It does not use an entry with a wildcard, such as "*.domain", because such an
// entry has no one exact IP address. It also does not use a host that it cannot
// resolve, and that condition is not fatal.
//
// Each platform implementation of TransparentRedirector.Start uses this
// function. The step from AllowHosts to a list of permitted IP addresses is
// only DNS resolution, and no part of it belongs to one operating system.
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

// wildcardHosts gives the entries with the form "*.domain" from hosts. Those
// are the entries that resolveAllowHosts above does not use, and it gives no
// message. A wildcard has no one exact IP address to add to a list of permitted
// addresses, on any platform.
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

// expandProcessNameCandidates changes each configured process name to small
// letters. For a CLI inside a script, it also adds "node.exe" as an
// alternative. Those scripts have the endings .cmd, .bat or .ps1, and
// cursor-agent.cmd is the known live condition on Windows.
//
// The cause: a script does not own the TCP connection. The interpreter that the
// script starts owns it, and for a CLI that uses Node, that interpreter is
// node.exe.
//
// This is an approximation, and this comment says so. It is not a general
// solution for each chain of scripts.
//
// The endings .cmd, .bat and .ps1 never agree with a process name on a platform
// that is not Windows. Therefore this alternative does nothing there, and it
// does no damage.
//
// This code is shared, and it is not in two positions. Therefore the rules for
// the comparison cannot become different between two platforms.
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
