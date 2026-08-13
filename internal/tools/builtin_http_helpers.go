package tools

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
)

func parseURLArg(input string, args json.RawMessage) string {
	if len(args) > 0 {
		var m map[string]any
		if json.Unmarshal(args, &m) == nil {
			if u, ok := m["url"].(string); ok {
				return strings.TrimSpace(u)
			}
		}
	}
	in := strings.TrimSpace(input)
	if strings.HasPrefix(in, "{") {
		var m map[string]any
		if json.Unmarshal([]byte(in), &m) == nil {
			if u, ok := m["url"].(string); ok {
				return strings.TrimSpace(u)
			}
		}
	}
	return strings.TrimSpace(firstField(in))
}

func stripTags(s string) string {
	s = tagRE.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = spaceRE.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func htmlToReadableText(s string) string {
	s = tagRE.ReplaceAllString(s, "\n")
	s = html.UnescapeString(s)
	lines := strings.Split(s, "\n")
	var keep []string
	for _, ln := range lines {
		ln = strings.TrimSpace(spaceRE.ReplaceAllString(ln, " "))
		if ln == "" {
			continue
		}
		keep = append(keep, ln)
	}
	return strings.Join(keep, "\n")
}

func looksLikeHTML(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, "<html") || strings.Contains(low, "<body") || strings.Contains(low, "<div") || strings.Contains(low, "<p>")
}

// checkHostAllowed returns nil when allowlist is empty (open) or URL host matches an entry.
func checkHostAllowed(rawURL string, allowHosts []string) error {
	if len(allowHosts) == 0 {
		return nil
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid url")
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range allowHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		if host == h || strings.HasSuffix(host, "."+h) || strings.Contains(strings.ToLower(rawURL), h) {
			return nil
		}
	}
	return fmt.Errorf("host %q not allowlisted", host)
}

func truncateErrBody(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "..."
	}
	return s
}
