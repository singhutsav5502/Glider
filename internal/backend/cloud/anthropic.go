package cloud

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/backend"
)

// AnthropicBackend translates OpenAI-format requests to Anthropic Messages API.
type AnthropicBackend struct {
	baseURL string
	apiKey  string
	client  *http.Client
	name    string
}

func NewAnthropic(baseURL, apiKey string) *AnthropicBackend {
	return &AnthropicBackend{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 120 * time.Second},
		name:    "anthropic",
	}
}

func (b *AnthropicBackend) Name() string              { return b.name }
func (b *AnthropicBackend) Type() backend.BackendType { return backend.BackendTypeCloud }

var _ backend.InferenceBackend = (*AnthropicBackend)(nil)

func (b *AnthropicBackend) Complete(ctx context.Context, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	system, messages := splitSystem(req.Messages)
	body := map[string]any{
		"model":      req.Model,
		"messages":   messages,
		"max_tokens": 4096,
		"stream":     true,
	}
	if system != "" {
		body["system"] = system
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/messages", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", b.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic complete: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &RateLimitError{
			StatusCode: 429,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After")),
			Message:    string(msg),
		}
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("anthropic error: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	ch := make(chan backend.CompletionChunk, 16)
	go func() {
		defer close(ch)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			payload := strings.TrimPrefix(line, "data: ")
			var event struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
				Message struct {
					ID    string `json:"id"`
					Model string `json:"model"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(payload), &event); err != nil {
				continue
			}
			switch event.Type {
			case "content_block_delta":
				select {
				case ch <- backend.CompletionChunk{
					ID:      event.Message.ID,
					Model:   event.Message.Model,
					Content: event.Delta.Text,
				}:
				case <-ctx.Done():
					return
				}
			case "message_stop":
				select {
				case ch <- backend.CompletionChunk{FinishReason: "stop"}:
				case <-ctx.Done():
				}
				return
			}
		}
	}()
	return ch, nil
}

func splitSystem(msgs []backend.Message) (string, []map[string]string) {
	var system string
	out := make([]map[string]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Role == "system" {
			if system == "" {
				system = m.Content
			} else {
				system += "\n" + m.Content
			}
			continue
		}
		role := m.Role
		if role != "user" && role != "assistant" {
			role = "user"
		}
		out = append(out, map[string]string{"role": role, "content": m.Content})
	}
	return system, out
}
