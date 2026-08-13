package mitm

import (
	"strings"
)

// HostMatcher matches CONNECT targets against an allowlist of exact hosts or *.domain patterns.
type HostMatcher struct {
	patterns []string
}

func NewHostMatcher(patterns []string) *HostMatcher {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		p = strings.TrimSpace(strings.ToLower(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return &HostMatcher{patterns: out}
}

// Patterns returns the raw allowlist entries (exact hosts and *.domain
// wildcards) — used by TransparentRedirector to resolve concrete IPs to
// scope its packet filter down to just these hosts, instead of diverting
// every outbound :443 connection on the machine.
func (m *HostMatcher) Patterns() []string {
	if m == nil {
		return nil
	}
	return append([]string(nil), m.patterns...)
}

// Match reports whether host (without port) is allowlisted for MITM decrypt.
func (m *HostMatcher) Match(host string) bool {
	if m == nil || len(m.patterns) == 0 {
		return false
	}
	host = strings.ToLower(stripPort(host))
	for _, p := range m.patterns {
		if matchHostPattern(p, host) {
			return true
		}
	}
	return false
}

func stripPort(hostport string) string {
	if i := strings.LastIndex(hostport, ":"); i != -1 {
		// IPv6 [::1]:443 — keep simple: if after colon is all digits, strip
		port := hostport[i+1:]
		if port != "" && isDigits(port) {
			return hostport[:i]
		}
	}
	return hostport
}

func isDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func matchHostPattern(pattern, host string) bool {
	if pattern == host {
		return true
	}
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".cursor.sh"
		if strings.HasSuffix(host, suffix) {
			return true
		}
		// also allow exact apex: cursor.sh for *.cursor.sh
		if host == pattern[2:] {
			return true
		}
	}
	return false
}
