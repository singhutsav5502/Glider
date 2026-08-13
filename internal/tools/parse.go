package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseTextToolCalls extracts tool calls from model text that printed JSON
// instead of emitting OpenAI tool_calls. Supports:
//   - {"name":"...","arguments":{...}}
//   - arrays of those objects
//   - ``` / ```json fenced blocks
//   - OpenAI {"type":"function","function":{"name":"...","arguments":...}}
//   - nested {"name":"plan","arguments":{"steps":[...tool objects...]}}
func ParseTextToolCalls(text string) []ToolCallDelta {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	var out []ToolCallDelta
	seen := map[string]struct{}{}
	add := func(c ToolCallDelta) {
		c.Name = strings.TrimSpace(c.Name)
		if c.Name == "" || c.Name == "*" {
			return
		}
		if c.Arguments == "" {
			c.Arguments = "{}"
		}
		key := c.Name + "\x00" + c.Arguments
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		if c.ID == "" {
			c.ID = fmt.Sprintf("text-%d", len(out)+1)
		}
		out = append(out, c)
	}
	for _, cand := range extractJSONCandidates(text) {
		for _, c := range parseToolCallJSON(cand) {
			add(c)
		}
	}
	return out
}

func extractJSONCandidates(text string) []string {
	var cands []string
	rest := text
	for {
		start := strings.Index(rest, "```")
		if start < 0 {
			break
		}
		body := rest[start+3:]
		if nl := strings.IndexByte(body, '\n'); nl >= 0 {
			lang := strings.TrimSpace(strings.ToLower(body[:nl]))
			if lang == "json" || lang == "" || strings.HasPrefix(lang, "json") {
				body = body[nl+1:]
			}
		}
		end := strings.Index(body, "```")
		if end < 0 {
			break
		}
		if block := strings.TrimSpace(body[:end]); block != "" {
			cands = append(cands, block)
		}
		rest = body[end+3:]
	}
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		cands = append(cands, trimmed)
	}
	// Scan for embedded {"name": ...} / [{"name": ...}] objects in prose.
	for i := 0; i < len(text); i++ {
		if text[i] != '{' && text[i] != '[' {
			continue
		}
		if frag, ok := balancedJSON(text[i:]); ok {
			if strings.Contains(frag, `"name"`) {
				cands = append(cands, frag)
			}
			i += len(frag) - 1
		}
	}
	return cands
}

func balancedJSON(s string) (string, bool) {
	if s == "" || (s[0] != '{' && s[0] != '[') {
		return "", false
	}
	stack := []byte{s[0]}
	inStr, esc := false, false
	for i := 1; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			stack = append(stack, c)
		case '}':
			if len(stack) == 0 || stack[len(stack)-1] != '{' {
				return "", false
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return s[:i+1], true
			}
		case ']':
			if len(stack) == 0 || stack[len(stack)-1] != '[' {
				return "", false
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return s[:i+1], true
			}
		}
	}
	return "", false
}

func parseToolCallJSON(raw string) []ToolCallDelta {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	// Array of calls / steps.
	if strings.HasPrefix(raw, "[") {
		var items []json.RawMessage
		if json.Unmarshal([]byte(raw), &items) != nil {
			return nil
		}
		var out []ToolCallDelta
		for _, item := range items {
			out = append(out, parseOneToolCallObject(item)...)
		}
		return out
	}
	return parseOneToolCallObject(json.RawMessage(raw))
}

func parseOneToolCallObject(raw json.RawMessage) []ToolCallDelta {
	var envelope struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		ID        string `json:"id"`
		Arguments any    `json:"arguments"`
		Function  *struct {
			Name      string `json:"name"`
			Arguments any    `json:"arguments"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return nil
	}
	name := strings.TrimSpace(envelope.Name)
	args := envelope.Arguments
	id := envelope.ID
	if envelope.Function != nil {
		if n := strings.TrimSpace(envelope.Function.Name); n != "" {
			name = n
		}
		if envelope.Function.Arguments != nil {
			args = envelope.Function.Arguments
		}
	}
	if name == "" {
		return nil
	}
	argsStr := encodeToolArgs(args)

	// Nested plan / steps wrappers: expand inner tool-shaped objects.
	lower := strings.ToLower(name)
	if lower == "plan" || lower == "tool_calls" || lower == "tools" {
		var nest map[string]any
		if json.Unmarshal([]byte(argsStr), &nest) == nil {
			for _, key := range []string{"steps", "tools", "tool_calls", "calls"} {
				if steps, ok := nest[key].([]any); ok {
					var out []ToolCallDelta
					for _, step := range steps {
						b, err := json.Marshal(step)
						if err != nil {
							continue
						}
						out = append(out, parseOneToolCallObject(b)...)
					}
					if len(out) > 0 {
						return out
					}
				}
			}
		}
		// Bare "plan" with no executable steps is not a tool.
		if lower == "plan" {
			return nil
		}
	}
	return []ToolCallDelta{{ID: id, Name: name, Arguments: argsStr}}
}

func encodeToolArgs(args any) string {
	if args == nil {
		return "{}"
	}
	switch v := args.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			return "{}"
		}
		return v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "{}"
		}
		return string(b)
	}
}

// LooksLikeTruncatedToolJSON reports assistant text that looks like a tool-call
// JSON blob cut off mid-stream (unbalanced braces / open string) rather than a
// finished answer. Used to retry instead of accepting partial artifact_write JSON.
func LooksLikeTruncatedToolJSON(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	// Prefer the first JSON-looking span.
	start := strings.IndexAny(text, "{[")
	if start < 0 {
		return false
	}
	frag := text[start:]
	if !strings.Contains(frag, `"name"`) {
		return false
	}
	lower := strings.ToLower(frag)
	toolish := strings.Contains(lower, `"arguments"`) ||
		strings.Contains(lower, `"function"`) ||
		strings.Contains(lower, "artifact_write") ||
		strings.Contains(lower, "fs_write") ||
		strings.Contains(lower, "git_clone") ||
		strings.Contains(lower, "code_grep")
	if !toolish {
		return false
	}
	if _, ok := balancedJSON(frag); ok {
		return false
	}
	return true
}
