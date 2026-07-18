package backend

import (
	"encoding/json"
	"strings"
)

// openAIStreamEnvelope is the subset of chat.completion.chunk we need.
type openAIStreamEnvelope struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Delta struct {
			Content   string          `json:"content"`
			ToolCalls []ToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// ParseOpenAIStreamPayload parses one SSE data payload (without the "data: " prefix).
// Returns ok=false for [DONE], empty, or unparseable lines.
func ParseOpenAIStreamPayload(payload string) (chunk CompletionChunk, ok bool) {
	payload = strings.TrimSpace(payload)
	if payload == "" || payload == "[DONE]" {
		return CompletionChunk{}, false
	}
	var envelope openAIStreamEnvelope
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return CompletionChunk{}, false
	}
	chunk = CompletionChunk{ID: envelope.ID, Model: envelope.Model}
	if len(envelope.Choices) == 0 {
		return chunk, true
	}
	c0 := envelope.Choices[0]
	chunk.Content = c0.Delta.Content
	if len(c0.Delta.ToolCalls) > 0 {
		chunk.ToolCalls = c0.Delta.ToolCalls
	}
	if c0.FinishReason != nil {
		chunk.FinishReason = *c0.FinishReason
	}
	return chunk, true
}

// MergeToolCallDeltas accumulates streaming tool_call fragments by index.
func MergeToolCallDeltas(dst *[]ToolCallDelta, deltas []ToolCallDelta) {
	if dst == nil || len(deltas) == 0 {
		return
	}
	for _, d := range deltas {
		for len(*dst) <= d.Index {
			*dst = append(*dst, ToolCallDelta{Index: len(*dst)})
		}
		cur := &(*dst)[d.Index]
		cur.Index = d.Index
		if d.ID != "" {
			cur.ID = d.ID
		}
		if d.Type != "" {
			cur.Type = d.Type
		}
		if d.Function == nil {
			continue
		}
		if cur.Function == nil {
			cur.Function = &FunctionDelta{}
		}
		if d.Function.Name != "" {
			cur.Function.Name = d.Function.Name
		}
		if d.Function.Arguments != "" {
			cur.Function.Arguments += d.Function.Arguments
		}
	}
}

// FinalizeToolCalls prepares accumulated deltas for a non-stream chat.completion message.
func FinalizeToolCalls(calls []ToolCallDelta) []map[string]any {
	if len(calls) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(calls))
	for _, c := range calls {
		if c.Function == nil || (c.Function.Name == "" && c.Function.Arguments == "") {
			continue
		}
		typ := c.Type
		if typ == "" {
			typ = "function"
		}
		item := map[string]any{
			"id":   c.ID,
			"type": typ,
			"function": map[string]any{
				"name":      c.Function.Name,
				"arguments": c.Function.Arguments,
			},
		}
		out = append(out, item)
	}
	return out
}
