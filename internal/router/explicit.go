package router

import (
	"sort"
	"strings"
	"unicode"
)

// MatchExplicitCommand finds the earliest configured slash command in content.
// Matches:
//   - trimmed prefix (legacy)
//   - start of any line
//   - mid-text token (e.g. TipTap join put chrome text before "/cloud …")
//
// Requires a command boundary after the token (EOF, whitespace, or common punctuation)
// so "/cloudiness" does not match "/cloud".
func MatchExplicitCommand(content string, commands []string) (cmd, remainder string, ok bool) {
	if len(commands) == 0 {
		return "", "", false
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return "", "", false
	}

	// Longer commands first so "/cloud" wins over a hypothetical "/c".
	sorted := append([]string(nil), commands...)
	sort.Slice(sorted, func(i, j int) bool {
		return len(sorted[i]) > len(sorted[j])
	})

	type hit struct {
		cmd string
		idx int
	}
	best := hit{idx: -1}

	consider := func(c string, idx int) {
		if idx < 0 {
			return
		}
		if !commandBoundaryOK(content, idx, len(c)) {
			return
		}
		if best.idx < 0 || idx < best.idx || (idx == best.idx && len(c) > len(best.cmd)) {
			best = hit{cmd: c, idx: idx}
		}
	}

	for _, c := range sorted {
		if c == "" {
			continue
		}
		// 1) Whole-string prefix.
		if strings.HasPrefix(content, c) {
			consider(c, 0)
		}
		// 2) Line starts (TipTap often joins nodes with \n).
		offset := 0
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
			pad := len(line) - len(trimmed)
			if strings.HasPrefix(trimmed, c) {
				consider(c, offset+pad)
			}
			offset += len(line)
			if i < len(lines)-1 {
				offset++ // account for '\n'
			}
		}
		// 3) Any token occurrence.
		searchFrom := 0
		for {
			idx := strings.Index(content[searchFrom:], c)
			if idx < 0 {
				break
			}
			abs := searchFrom + idx
			if tokenStartOK(content, abs) {
				consider(c, abs)
			}
			searchFrom = abs + 1
		}
	}

	if best.idx < 0 {
		return "", "", false
	}
	before := strings.TrimSpace(content[:best.idx])
	after := strings.TrimSpace(content[best.idx+len(best.cmd):])
	switch {
	case before == "":
		return best.cmd, after, true
	case after == "":
		return best.cmd, before, true
	default:
		return best.cmd, before + "\n" + after, true
	}
}

// CloudOverrideCommands are hard-force cloud/origin slash commands.
var CloudOverrideCommands = []string{"/cloud", "/heavy"}

// LocalOverrideCommands are hard-force local slash commands.
var LocalOverrideCommands = []string{"/local", "/fast"}

// HasCloudOverride reports whether text contains /cloud or /heavy (TipTap-safe).
func HasCloudOverride(text string) bool {
	_, _, ok := MatchExplicitCommand(text, CloudOverrideCommands)
	return ok
}

// HasLocalOverride reports whether text contains /local or /fast.
func HasLocalOverride(text string) bool {
	_, _, ok := MatchExplicitCommand(text, LocalOverrideCommands)
	return ok
}

func tokenStartOK(content string, idx int) bool {
	if idx <= 0 {
		return true
	}
	prev, _ := utf8RuneBefore(content, idx)
	return unicode.IsSpace(prev) || prev == '\n' || prev == '\r'
}

func commandBoundaryOK(content string, idx, cmdLen int) bool {
	end := idx + cmdLen
	if end > len(content) {
		return false
	}
	if end == len(content) {
		return true
	}
	r := rune(content[end])
	// ASCII-fast path; slash commands are ASCII.
	if r < utf8RuneSelf {
		return unicode.IsSpace(r) || strings.ContainsRune(".,:;!?)]}\"'`", r)
	}
	return unicode.IsSpace(r)
}

const utf8RuneSelf = 0x80

func utf8RuneBefore(s string, idx int) (rune, int) {
	if idx <= 0 || idx > len(s) {
		return 0, 0
	}
	// Slash commands are ASCII; one byte back is enough for boundary checks.
	return rune(s[idx-1]), 1
}
