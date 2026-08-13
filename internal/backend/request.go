package backend

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ParseToolNames extracts tool/function names from a tools JSON array.
func ParseToolNames(raw json.RawMessage) []string {
	s := bytes.TrimSpace(raw)
	if len(s) == 0 || bytes.Equal(s, []byte("null")) || bytes.Equal(s, []byte("[]")) {
		return nil
	}
	var arr []map[string]any
	if err := json.Unmarshal(s, &arr); err != nil {
		return nil
	}
	out := make([]string, 0, len(arr))
	seen := map[string]struct{}{}
	for _, item := range arr {
		name := ""
		if n, ok := item["name"].(string); ok {
			name = n
		}
		if name == "" {
			if fn, ok := item["function"].(map[string]any); ok {
				if n, ok := fn["name"].(string); ok {
					name = n
				}
			}
		}
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}

// AttachTools copies tools / tool_choice onto an OpenAI-compat request body map.
func AttachTools(body map[string]any, req *CompletionRequest) {
	if body == nil || req == nil {
		return
	}
	if req.HasTools() {
		var tools any
		if err := json.Unmarshal(req.Tools, &tools); err == nil {
			body["tools"] = tools
		}
	}
	if tc := bytes.TrimSpace(req.ToolChoice); len(tc) > 0 && !bytes.Equal(tc, []byte("null")) {
		var choice any
		if err := json.Unmarshal(req.ToolChoice, &choice); err == nil {
			body["tool_choice"] = choice
		}
	}
}

// AttachFormat copies Ollama-native format onto the request body and mirrors an
// OpenAI-compat response_format when possible (json_object or json_schema).
func AttachFormat(body map[string]any, req *CompletionRequest) {
	if body == nil || req == nil {
		return
	}
	raw := bytes.TrimSpace(req.Format)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return
	}
	var format any
	if err := json.Unmarshal(raw, &format); err != nil {
		return
	}
	body["format"] = format
	switch v := format.(type) {
	case string:
		if strings.EqualFold(strings.TrimSpace(v), "json") {
			body["response_format"] = map[string]any{"type": "json_object"}
		}
	case map[string]any:
		body["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "glider_response",
				"schema": v,
			},
		}
	}
}

// FormatIsJSONMode reports whether Format is the literal "json" string.
func FormatIsJSONMode(format json.RawMessage) bool {
	raw := bytes.TrimSpace(format)
	if len(raw) == 0 {
		return false
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(s), "json")
}

// CriticEvalFormat returns the preferred Ollama format schema for critic stages:
// {"score": number, "reason": string}. Callers may fall back to CriticEvalFormatJSON.
func CriticEvalFormat() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"score":{"type":"number"},"reason":{"type":"string"}},"required":["score","reason"]}`)
}

// CriticEvalFormatJSON is the version-safe fallback: format:"json" plus a prompt
// that asks for {"score","reason"}.
func CriticEvalFormatJSON() json.RawMessage {
	return json.RawMessage(`"json"`)
}
