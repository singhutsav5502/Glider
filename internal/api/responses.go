package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/backend"
)

// LooksLikeResponses reports whether a JSON body uses the OpenAI Responses API shape
// (has "input" and lacks "messages") — common Cursor Agent quirk when Override Base URL is set.
func LooksLikeResponses(body []byte) bool {
	var probe struct {
		Input    json.RawMessage `json:"input"`
		Messages json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return len(probe.Input) > 0 && len(probe.Messages) == 0
}

// ResponsesToCompletion converts a Responses API request body into CompletionRequest.
func ResponsesToCompletion(body []byte) (*backend.CompletionRequest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	req := &backend.CompletionRequest{}
	if v, ok := raw["model"]; ok {
		_ = json.Unmarshal(v, &req.Model)
	}
	if v, ok := raw["stream"]; ok {
		_ = json.Unmarshal(v, &req.Stream)
	}
	if v, ok := raw["temperature"]; ok {
		var t float64
		if err := json.Unmarshal(v, &t); err == nil {
			req.Temperature = &t
		}
	}
	if v, ok := raw["max_output_tokens"]; ok {
		var n int
		if err := json.Unmarshal(v, &n); err == nil {
			req.MaxTokens = &n
		}
	} else if v, ok := raw["max_tokens"]; ok {
		var n int
		if err := json.Unmarshal(v, &n); err == nil {
			req.MaxTokens = &n
		}
	}

	inputRaw, ok := raw["input"]
	if !ok || len(inputRaw) == 0 {
		// Fallback: already chat-completions shaped
		if v, ok := raw["messages"]; ok {
			if err := json.Unmarshal(v, &req.Messages); err != nil {
				return nil, err
			}
			if len(req.Messages) == 0 {
				return nil, errMissingMessages
			}
			return req, nil
		}
		return nil, fmt.Errorf("responses request missing input")
	}

	msgs, err := inputToMessages(inputRaw)
	if err != nil {
		return nil, err
	}
	// Optional instructions → system message
	if v, ok := raw["instructions"]; ok {
		var instr string
		if err := json.Unmarshal(v, &instr); err == nil && instr != "" {
			msgs = append([]backend.Message{{Role: "system", Content: instr}}, msgs...)
		}
	}
	req.Messages = msgs
	if len(req.Messages) == 0 {
		return nil, errMissingMessages
	}
	req.Metadata.OriginalModel = req.Model
	return req, nil
}

func inputToMessages(inputRaw json.RawMessage) ([]backend.Message, error) {
	// String input
	var s string
	if err := json.Unmarshal(inputRaw, &s); err == nil {
		return []backend.Message{{Role: "user", Content: s}}, nil
	}
	// Array of messages / items
	var items []map[string]any
	if err := json.Unmarshal(inputRaw, &items); err != nil {
		return nil, fmt.Errorf("unsupported input shape")
	}
	out := make([]backend.Message, 0, len(items))
	for _, it := range items {
		role, _ := it["role"].(string)
		if role == "" {
			role = "user"
		}
		content := extractContent(it["content"])
		if content == "" {
			continue
		}
		out = append(out, backend.Message{Role: role, Content: content})
	}
	return out, nil
}

func extractContent(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var b strings.Builder
		for _, part := range c {
			m, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := m["type"].(string); t == "input_text" || t == "text" || t == "" {
				if txt, ok := m["text"].(string); ok {
					b.WriteString(txt)
				}
			}
		}
		return b.String()
	default:
		return ""
	}
}

// WriteResponsesSSE streams a minimal Responses API event stream from chat chunks.
func WriteResponsesSSE(w http.ResponseWriter, requestID, model string, chunks <-chan backend.CompletionChunk) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	id := requestID
	if id == "" {
		id = fmt.Sprintf("resp_%d", time.Now().UnixNano())
	}
	// response.created
	_ = writeResponsesEvent(w, flusher, "response.created", map[string]any{
		"type": "response.created",
		"response": map[string]any{
			"id":     id,
			"object": "response",
			"status": "in_progress",
			"model":  model,
		},
	})

	var full strings.Builder
	for chunk := range chunks {
		if chunk.Model != "" {
			model = chunk.Model
		}
		full.WriteString(chunk.Content)
		_ = writeResponsesEvent(w, flusher, "response.output_text.delta", map[string]any{
			"type":  "response.output_text.delta",
			"delta": chunk.Content,
		})
	}
	_ = writeResponsesEvent(w, flusher, "response.completed", map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":     id,
			"object": "response",
			"status": "completed",
			"model":  model,
			"output": []map[string]any{
				{
					"type": "message",
					"role": "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": full.String()},
					},
				},
			},
		},
	})
	return nil
}

func writeResponsesEvent(w http.ResponseWriter, flusher http.Flusher, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if event != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", event); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

// WriteResponsesJSON writes a non-streaming Responses API object.
func WriteResponsesJSON(w http.ResponseWriter, requestID, model string, chunks <-chan backend.CompletionChunk) error {
	var content string
	for chunk := range chunks {
		content += chunk.Content
		if chunk.Model != "" {
			model = chunk.Model
		}
	}
	id := requestID
	if id == "" {
		id = fmt.Sprintf("resp_%d", time.Now().UnixNano())
	}
	resp := map[string]any{
		"id":     id,
		"object": "response",
		"status": "completed",
		"model":  model,
		"output": []map[string]any{
			{
				"type": "message",
				"role": "assistant",
				"content": []map[string]any{
					{"type": "output_text", "text": content},
				},
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(resp)
}
