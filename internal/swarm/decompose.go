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
)

// DecomposeSubTasks parses planner / plan-role text into bounded SubTasks.
// Caps at maxN (default 4). Empty / unparseable → nil (caller keeps role FanOut).
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
	var prompts []string
	for _, m := range reNumbered.FindAllStringSubmatch(text, -1) {
		if len(m) < 2 {
			continue
		}
		p := cleanSubPrompt(m[1])
		if p != "" {
			prompts = append(prompts, p)
		}
	}
	if len(prompts) == 0 {
		for _, line := range strings.Split(text, "\n") {
			if m := reStepLine.FindStringSubmatch(line); len(m) >= 2 {
				p := cleanSubPrompt(m[1])
				if p != "" {
					prompts = append(prompts, p)
				}
			}
		}
	}
	if len(prompts) == 0 {
		// Fallback: split on semicolons / "then" clauses when short plan.
		for _, part := range splitClauses(text) {
			p := cleanSubPrompt(part)
			if p != "" {
				prompts = append(prompts, p)
			}
		}
	}
	if len(prompts) == 0 {
		return nil
	}
	if len(prompts) > maxN {
		prompts = prompts[:maxN]
	}
	out := make([]backend.SubTask, len(prompts))
	for i, p := range prompts {
		out[i] = backend.SubTask{
			Prompt: p,
			Target: "worker",
		}
	}
	return out
}

func cleanSubPrompt(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "`\"'")
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
	goal = strings.TrimSpace(goal)
	if goal == "" {
		return fmt.Sprintf("[subtask %d]\n%s", idx+1, p)
	}
	return fmt.Sprintf("%s\n\n[subtask %d]\n%s", goal, idx+1, p)
}
