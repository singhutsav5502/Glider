package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToolResultPromptCap is the per-tool output budget when injecting results back into
// the model (FormatToolResults). Audit clones / greps routinely exceed 4k; keep a
// hard cap but high enough that mid-sentence clipping is rare.
const ToolResultPromptCap = 24000

// FlattenToolArgs turns OpenAI-style JSON arguments into the line-oriented input
// builtins expect, while preserving raw JSON objects for Call(args) / MCP.
func FlattenToolArgs(name, arguments string) string {
	input := arguments
	if input == "" {
		return "{}"
	}
	var argsMap map[string]any
	if json.Unmarshal([]byte(input), &argsMap) != nil {
		return input
	}
	// Keep full JSON for web tools so Call can read query/url/limit together.
	switch strings.TrimSpace(name) {
	case "web_search", "web_fetch", "http_fetch":
		return input
	}
	if v, ok := argsMap["input"].(string); ok {
		return v
	}
	if path, ok := argsMap["path"].(string); ok {
		if content, ok := argsMap["content"].(string); ok {
			if kind, ok := argsMap["kind"].(string); ok && strings.TrimSpace(kind) != "" {
				return strings.TrimSpace(kind) + " " + path + "\n" + content
			}
			return path + "\n" + content
		}
		return path
	}
	if url, ok := argsMap["url"].(string); ok {
		dir, _ := argsMap["dir"].(string)
		if strings.TrimSpace(dir) == "" {
			// Common model alias for git_clone destination.
			if td, ok := argsMap["targetDir"].(string); ok {
				dir = td
			}
		}
		if strings.TrimSpace(dir) != "" {
			return strings.TrimSpace(url) + " " + strings.TrimSpace(dir)
		}
		return url
	}
	for _, key := range []string{"query", "command", "expr", "pattern"} {
		if v, ok := argsMap[key].(string); ok {
			return v
		}
	}
	_ = name
	return input
}

// FormatToolResults builds a prompt injection block from Invoke results.
func FormatToolResults(results []Result) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[tool_results]\n")
	for _, r := range results {
		b.WriteString("- ")
		b.WriteString(r.Name)
		b.WriteString(" ok=")
		b.WriteString(fmt.Sprintf("%t", r.OK))
		if r.Stubbed {
			b.WriteString(" stubbed")
		}
		b.WriteByte('\n')
		out := r.Output
		if len(out) > ToolResultPromptCap {
			out = out[:ToolResultPromptCap] + "\n...truncated"
		}
		if out != "" {
			b.WriteString(out)
			b.WriteByte('\n')
		}
		if r.Err != "" {
			b.WriteString("err: ")
			b.WriteString(r.Err)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
