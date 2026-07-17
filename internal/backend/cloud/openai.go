package cloud

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/glider-ai/glider/internal/backend"
)

// OpenAIBackend implements InferenceBackend for OpenAI-compatible APIs.
type OpenAIBackend struct {
	baseURL string
	apiKey  string
	client  *http.Client
	name    string
	healthy atomic.Bool
}

func NewOpenAI(baseURL, apiKey string) *OpenAIBackend {
	b := &OpenAIBackend{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{Timeout: 120 * time.Second},
		name:    "openai",
	}
	return b
}

func (b *OpenAIBackend) Name() string              { return b.name }
func (b *OpenAIBackend) Type() backend.BackendType { return backend.BackendTypeCloud }

var _ backend.InferenceBackend = (*OpenAIBackend)(nil)

func (b *OpenAIBackend) Complete(ctx context.Context, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	body := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   true,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+b.apiKey)

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai complete: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		retry := parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, &RateLimitError{StatusCode: 429, RetryAfter: retry, Message: string(msg)}
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("openai error: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	ch := make(chan backend.CompletionChunk, 16)
	go streamOpenAIChunks(ctx, resp, ch)
	return ch, nil
}

func streamOpenAIChunks(ctx context.Context, resp *http.Response, ch chan<- backend.CompletionChunk) {
	defer close(ch)
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			return
		}
		var envelope struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
			continue
		}
		chunk := backend.CompletionChunk{ID: envelope.ID, Model: envelope.Model}
		if len(envelope.Choices) > 0 {
			chunk.Content = envelope.Choices[0].Delta.Content
			if envelope.Choices[0].FinishReason != nil {
				chunk.FinishReason = *envelope.Choices[0].FinishReason
			}
		}
		select {
		case ch <- chunk:
		case <-ctx.Done():
			return
		}
	}
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
