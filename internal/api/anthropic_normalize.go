package api

import (
	"bytes"
	"encoding/json"
	"strings"
)

// CursorCustomModelPrefix opens the Override Base URL path. Cursor sends a
// model name that it recognises to api2.cursor.sh regardless of that setting;
// a prefix it does not recognise forces the OpenAI-compatible route instead.
const CursorCustomModelPrefix = "cus-"

// NormalizeGatewayModel strips known Cursor custom-model prefixes so harness
// rules and upstream backends see the real model id.
func NormalizeGatewayModel(model string) string {
	m := strings.TrimSpace(model)
	for _, p := range []string{CursorCustomModelPrefix, "glider-"} {
		if strings.HasPrefix(strings.ToLower(m), p) {
			return m[len(p):]
		}
	}
	return m
}

// NormalizeAnthropicShapedJSON mutates Cursor Agent JSON that uses Anthropic
// conventions (top-level system, content blocks, input_schema tools) into a
// shape closer to OpenAI chat.completions before ParseCompletionRequest.
//
// Necessary when Agent tools reach a custom OpenAI Base URL. Best-effort: on
// failure returns the original body.
//
// Path A tool bridge: tool_use blocks → assistant.tool_calls; tool_result
// blocks → role=tool messages with tool_call_id.
func NormalizeAnthropicShapedJSON(body []byte) []byte {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return body
	}
	changed := false

	if raw, ok := root["model"]; ok {
		var model string
		if json.Unmarshal(raw, &model) == nil {
			norm := NormalizeGatewayModel(model)
			if norm != model {
				b, _ := json.Marshal(norm)
				root["model"] = b
				changed = true
			}
		}
	}

	if raw, ok := root["system"]; ok {
		var sys string
		if json.Unmarshal(raw, &sys) == nil && sys != "" {
			var msgs []json.RawMessage
			if mraw, ok := root["messages"]; ok {
				_ = json.Unmarshal(mraw, &msgs)
			}
			sysMsg, _ := json.Marshal(map[string]string{"role": "system", "content": sys})
			msgs = append([]json.RawMessage{sysMsg}, msgs...)
			b, _ := json.Marshal(msgs)
			root["messages"] = b
			delete(root, "system")
			changed = true
		}
	}

	if raw, ok := root["messages"]; ok {
		normalized, ok2 := normalizeMessageArray(raw)
		if ok2 {
			root["messages"] = normalized
			changed = true
		}
	}

	if raw, ok := root["tools"]; ok {
		cleaned, ok2 := cleanToolsArray(raw)
		if ok2 {
			root["tools"] = cleaned
			changed = true
		}
	}

	if !changed {
		return body
	}
	out, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return out
}

// normalizeMessageArray flattens Anthropic content blocks and converts
// tool_use / tool_result into OpenAI tool_calls / role=tool messages.
func normalizeMessageArray(raw json.RawMessage) (json.RawMessage, bool) {
	var msgs []map[string]json.RawMessage
	if json.Unmarshal(raw, &msgs) != nil || len(msgs) == 0 {
		return nil, false
	}
	out := make([]map[string]json.RawMessage, 0, len(msgs))
	changed := false
	for _, msg := range msgs {
		expanded, did := expandAnthropicMessage(msg)
		if did {
			changed = true
		}
		out = append(out, expanded...)
	}
	if !changed {
		return nil, false
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return b, true
}

func expandAnthropicMessage(msg map[string]json.RawMessage) ([]map[string]json.RawMessage, bool) {
	raw, ok := msg["content"]
	if !ok {
		return []map[string]json.RawMessage{msg}, false
	}
	var asString string
	if json.Unmarshal(raw, &asString) == nil {
		return []map[string]json.RawMessage{msg}, false
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return []map[string]json.RawMessage{msg}, false
	}

	var text strings.Builder
	var toolCalls []map[string]any
	var toolResults []map[string]json.RawMessage
	for _, block := range blocks {
		var typ string
		_ = json.Unmarshal(block["type"], &typ)
		switch typ {
		case "text", "":
			var t string
			if json.Unmarshal(block["text"], &t) == nil {
				if text.Len() > 0 {
					text.WriteByte('\n')
				}
				text.WriteString(t)
			}
		case "tool_use":
			if tc := toolUseToOpenAI(block); tc != nil {
				toolCalls = append(toolCalls, tc)
			}
		case "tool_result":
			if tr := toolResultToOpenAI(block); tr != nil {
				toolResults = append(toolResults, tr)
			}
		}
	}

	// Pure tool_result user turn → one or more role=tool messages.
	if len(toolResults) > 0 && len(toolCalls) == 0 && text.Len() == 0 {
		return toolResults, true
	}

	outMsg := cloneRawMsg(msg)
	nb, _ := json.Marshal(text.String())
	outMsg["content"] = nb
	if len(toolCalls) > 0 {
		tcRaw, _ := json.Marshal(toolCalls)
		outMsg["tool_calls"] = tcRaw
	}
	result := []map[string]json.RawMessage{outMsg}
	if len(toolResults) > 0 {
		result = append(result, toolResults...)
	}
	return result, true
}

func toolUseToOpenAI(block map[string]json.RawMessage) map[string]any {
	var id, name string
	_ = json.Unmarshal(block["id"], &id)
	_ = json.Unmarshal(block["name"], &name)
	if name == "" {
		return nil
	}
	args := "{}"
	if raw, ok := block["input"]; ok && len(bytes.TrimSpace(raw)) > 0 {
		args = string(raw)
	}
	return map[string]any{
		"id":   id,
		"type": "function",
		"function": map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
}

func toolResultToOpenAI(block map[string]json.RawMessage) map[string]json.RawMessage {
	var id string
	_ = json.Unmarshal(block["tool_use_id"], &id)
	if id == "" {
		_ = json.Unmarshal(block["tool_call_id"], &id)
	}
	content := toolResultContent(block["content"])
	out := map[string]json.RawMessage{
		"role":    mustRaw(`"tool"`),
		"content": mustRawJSON(content),
	}
	if id != "" {
		out["tool_call_id"] = mustRawJSON(id)
	}
	return out
}

func toolResultContent(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, block := range blocks {
			var typ string
			_ = json.Unmarshal(block["type"], &typ)
			if typ == "text" || typ == "" {
				var t string
				if json.Unmarshal(block["text"], &t) == nil {
					if b.Len() > 0 {
						b.WriteByte('\n')
					}
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	return string(raw)
}

func cloneRawMsg(msg map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(msg))
	for k, v := range msg {
		out[k] = v
	}
	return out
}

func mustRaw(s string) json.RawMessage { return json.RawMessage(s) }

func mustRawJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func cleanToolsArray(raw json.RawMessage) (json.RawMessage, bool) {
	var tools []map[string]any
	if json.Unmarshal(raw, &tools) != nil || len(tools) == 0 {
		return nil, false
	}
	changed := false
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		// Anthropic: {name, description, input_schema} → OpenAI function wrapper
		if _, hasType := tool["type"]; !hasType {
			if _, hasName := tool["name"]; hasName {
				params, _ := tool["input_schema"].(map[string]any)
				if params == nil {
					params, _ = tool["parameters"].(map[string]any)
				}
				if params == nil {
					params = map[string]any{"type": "object", "properties": map[string]any{}}
				}
				cleanSchema(params)
				desc, _ := tool["description"].(string)
				if desc == "" {
					desc, _ = tool["desc"].(string)
				}
				name, _ := tool["name"].(string)
				out = append(out, map[string]any{
					"type": "function",
					"function": map[string]any{
						"name":        name,
						"description": desc,
						"parameters":   params,
					},
				})
				changed = true
				continue
			}
		}
		if fn, ok := tool["function"].(map[string]any); ok {
			if params, ok := fn["parameters"].(map[string]any); ok {
				if cleanSchema(params) {
					changed = true
				}
			}
			if schema, ok := tool["input_schema"]; ok && fn["parameters"] == nil {
				if params, ok := schema.(map[string]any); ok {
					cleanSchema(params)
					fn["parameters"] = params
					delete(tool, "input_schema")
					changed = true
				}
			}
		}
		if schema, ok := tool["input_schema"].(map[string]any); ok {
			cleanSchema(schema)
			changed = true
		}
		if params, ok := tool["parameters"].(map[string]any); ok {
			if cleanSchema(params) {
				changed = true
			}
		}
		out = append(out, tool)
	}
	if !changed {
		return nil, false
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return b, true
}

func cleanSchema(schema map[string]any) bool {
	if schema == nil {
		return false
	}
	changed := false
	for _, k := range []string{"additionalProperties", "$schema", "title"} {
		if _, ok := schema[k]; ok {
			delete(schema, k)
			changed = true
		}
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for _, v := range props {
			if child, ok := v.(map[string]any); ok {
				if cleanSchema(child) {
					changed = true
				}
			}
		}
	}
	if items, ok := schema["items"].(map[string]any); ok {
		if cleanSchema(items) {
			changed = true
		}
	}
	return changed
}
