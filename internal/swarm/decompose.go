package swarm

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/glider-ai/glider/internal/backend"
)

var (
	reNumbered = regexp.MustCompile(`(?m)^\s*(?:\d+[.)]\s+|[-*•]\s+)(.+)$`)
	reStepLine = regexp.MustCompile(`(?i)^\s*(?:step\s*\d+[:.\s]+|task\s*\d+[:.\s]+)(.+)$`)
	// Role invent: [role:security], role=exec, @research, (model=foo)
	reRoleBracket = regexp.MustCompile(`(?i)\[(?:role[=:\s]+)([a-z][a-z0-9_-]{0,31})\]`)
	reRoleEq      = regexp.MustCompile(`(?i)\brole\s*[=:]\s*([a-z][a-z0-9_-]{0,31})\b`)
	reAtRole      = regexp.MustCompile(`(?i)@([a-z][a-z0-9_-]{0,31})\b`)
	reModelEq     = regexp.MustCompile(`(?i)\bmodel\s*[=:]\s*([a-zA-Z0-9._:/-]{1,64})\b`)
)

// DecomposeSubTasks parses planner / plan-role text into bounded SubTasks.
// Caps at maxN (default 4). Empty / unparseable → nil (caller keeps role FanOut).
// When lines include role= / [role:] / @role, Target is set (free-spawn invent).
func DecomposeSubTasks(plannerText string, maxN int) []backend.SubTask {
	if maxN <= 0 {
		maxN = 4
	}
	if maxN > 4 {
		maxN = 4
	}
	text := strings.TrimSpace(plannerText)
	if text == "" {
		return nil
	}
	var lines []string
	for _, m := range reNumbered.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 {
			continue
		}
		if p := strings.TrimSpace(m[1]); p != "" {
			lines = append(lines, p)
		}
	}
	if len(lines) == 0 {
		for _, line := range strings.Split(text, "\n") {
			if m := reStepLine.FindStringSubmatch(line); len(m) >= 2 {
				if p := strings.TrimSpace(m[1]); p != "" {
					lines = append(lines, p)
				}
			}
		}
	}
	if len(lines) == 0 {
		for _, part := range splitClauses(text) {
			if p := strings.TrimSpace(part); p != "" {
				lines = append(lines, p)
			}
		}
	}
	if len(lines) == 0 {
		return nil
	}
	if len(lines) > maxN {
		lines = lines[:maxN]
	}
	out := make([]backend.SubTask, 0, len(lines))
	for _, raw := range lines {
		st := parseSubTaskLine(raw)
		if st.Prompt == "" {
			continue
		}
		out = append(out, st)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// parseSubTaskLine extracts optional role/model markers then cleans the prompt.
func parseSubTaskLine(raw string) backend.SubTask {
	st := backend.SubTask{Target: "worker"}
	s := strings.TrimSpace(raw)
	if m := reModelEq.FindStringSubmatch(s); len(m) >= 2 {
		st.Model = strings.TrimSpace(m[1])
		s = reModelEq.ReplaceAllString(s, " ")
	}
	role := ""
	if m := reRoleBracket.FindStringSubmatch(s); len(m) >= 2 {
		role = m[1]
		s = reRoleBracket.ReplaceAllString(s, " ")
	} else if m := reRoleEq.FindStringSubmatch(s); len(m) >= 2 {
		role = m[1]
		s = reRoleEq.ReplaceAllString(s, " ")
	} else if m := reAtRole.FindStringSubmatch(s); len(m) >= 2 {
		role = m[1]
		s = reAtRole.ReplaceAllString(s, " ")
	}
	if role != "" {
		st.Target = sanitizeRole(role)
	}
	st.Prompt = cleanSubPrompt(s)
	return st
}

func sanitizeRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	var b strings.Builder
	for _, r := range role {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" {
		return "worker"
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}

func cleanSubPrompt(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "`\"'")
	s = strings.Join(strings.Fields(s), " ")
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return ""
	}
	if len(s) > 400 {
		s = s[:400]
	}
	return s
}

func splitClauses(text string) []string {
	text = strings.ReplaceAll(text, " then ", ";")
	text = strings.ReplaceAll(text, " Then ", ";")
	parts := strings.Split(text, ";")
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || strings.Count(p, " ") < 2 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// FormatSubTaskPrompt builds a wave/worker prompt from a SubTask + goal.
func FormatSubTaskPrompt(goal string, idx int, st backend.SubTask) string {
	p := strings.TrimSpace(st.Prompt)
	if p == "" {
		p = fmt.Sprintf("subtask %d", idx+1)
	}
	role := strings.TrimSpace(st.Target)
	if role == "" {
		role = "worker"
	}
	goal = strings.TrimSpace(goal)
	head := fmt.Sprintf("[subtask %d role=%s]\n%s", idx+1, role, p)
	if goal == "" {
		return head
	}
	return goal + "\n\n" + head
}

// RolesFromSubTasks returns capped role list for free-spawn FanOut.
func RolesFromSubTasks(tasks []backend.SubTask, maxN int) []string {
	if maxN <= 0 {
		maxN = 4
	}
	if maxN > 4 {
		maxN = 4
	}
	var out []string
	seen := map[string]int{}
	for _, st := range tasks {
		if len(out) >= maxN {
			break
		}
		r := sanitizeRole(st.Target)
		if r == "" {
			r = "worker"
		}
		// Deduplicate by appending index suffix when same role appears twice.
		if n := seen[r]; n > 0 {
			r = fmt.Sprintf("%s%d", r, n+1)
		}
		seen[sanitizeRole(st.Target)]++
		out = append(out, r)
	}
	return out
}

// ModelsFromSubTasks returns per-worker models aligned with RolesFromSubTasks.
func ModelsFromSubTasks(tasks []backend.SubTask, maxN int) []string {
	if maxN <= 0 {
		maxN = 4
	}
	if maxN > 4 {
		maxN = 4
	}
	var out []string
	for _, st := range tasks {
		if len(out) >= maxN {
			break
		}
		out = append(out, strings.TrimSpace(st.Model))
	}
	return out
}
