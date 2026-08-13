package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/glider-ai/glider/internal/backend"
)

// WriteChatSSE streams OpenAI chat.completion.chunk SSE events.
func WriteChatSSE(w http.ResponseWriter, requestID, model string, chunks <-chan backend.CompletionChunk) error {
	return writeSSE(w, requestID, model, chunks)
}

// WriteChatJSON writes a non-streaming chat.completion response.
func WriteChatJSON(w http.ResponseWriter, requestID, model string, chunks <-chan backend.CompletionChunk) error {
	return writeNonStream(w, requestID, model, chunks)
}

func writeSSE(w http.ResponseWriter, requestID, model string, chunks <-chan backend.CompletionChunk) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	created := time.Now().Unix()
	for chunk := range chunks {
		id := chunk.ID
		if id == "" {
			id = requestID
		}
		m := chunk.Model
		if m == "" {
			m = model
		}
		delta := map[string]any{}
		if len(chunk.ToolCalls) > 0 {
			delta["tool_calls"] = chunk.ToolCalls
			if chunk.Content != "" {
				delta["content"] = chunk.Content
			}
		} else {
			delta["content"] = chunk.Content
		}
		payload := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   m,
			"choices": []map[string]any{
				{
					"index":         0,
					"delta":         delta,
					"finish_reason": nilOrString(chunk.FinishReason),
				},
			},
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return err
		}
		flusher.Flush()
	}
	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeNonStream(w http.ResponseWriter, requestID, model string, chunks <-chan backend.CompletionChunk) error {
	var content string
	var toolCalls []backend.ToolCallDelta
	finishReason := "stop"
	for chunk := range chunks {
		content += chunk.Content
		backend.MergeToolCallDeltas(&toolCalls, chunk.ToolCalls)
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.FinishReason != "" {
			finishReason = chunk.FinishReason
		}
	}
	message := map[string]any{
		"role":    "assistant",
		"content": content,
	}
	if finalized := backend.FinalizeToolCalls(toolCalls); len(finalized) > 0 {
		message["tool_calls"] = finalized
		if finishReason == "stop" {
			finishReason = "tool_calls"
		}
	}
	resp := map[string]any{
		"id":      requestID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index":         0,
				"message":       message,
				"finish_reason": finishReason,
			},
		},
	}
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(resp)
}

func nilOrString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
