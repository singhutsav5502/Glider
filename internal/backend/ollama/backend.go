package ollama

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/glider-ai/glider/internal/backend"
)

// Backend implements InferenceBackend, ModelManager, and HealthChecker for Ollama.
type Backend struct {
	baseURL    string
	client     *http.Client
	healthy    atomic.Bool
	name       string
}

func New(baseURL string) *Backend {
	b := &Backend{
		baseURL: strings.TrimRight(baseURL, "/"),
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
		name: "ollama",
	}
	b.healthy.Store(false)
	return b
}

func (b *Backend) Name() string              { return b.name }
func (b *Backend) Type() backend.BackendType { return backend.BackendTypeLocal }

// Compile-time interface checks (T1.2.1)
var (
	_ backend.InferenceBackend = (*Backend)(nil)
	_ backend.ModelManager     = (*Backend)(nil)
	_ backend.HealthChecker    = (*Backend)(nil)
)

func (b *Backend) Complete(ctx context.Context, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	body := map[string]any{
		"model":    req.Model,
		"messages": req.Messages,
		"stream":   true,
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.MaxTokens != nil {
		body["max_tokens"] = *req.MaxTokens
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama complete: %w", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama error: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
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
			chunk := backend.CompletionChunk{
				ID:    envelope.ID,
				Model: envelope.Model,
			}
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
	}()
	return ch, nil
}

func (b *Backend) LoadModel(ctx context.Context, model string, opts backend.LoadOptions) error {
	keepAlive := formatKeepAlive(opts.KeepAlive)
	body := map[string]any{
		"model":      model,
		"keep_alive": keepAlive,
	}
	return b.postJSON(ctx, "/api/generate", body)
}

func formatKeepAlive(d time.Duration) string {
	if d == 0 {
		return "5m"
	}
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	if d%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return d.String()
}

func (b *Backend) UnloadModel(ctx context.Context, model string) error {
	body := map[string]any{
		"model":      model,
		"keep_alive": 0,
	}
	return b.postJSON(ctx, "/api/generate", body)
}

func (b *Backend) ListLoaded(ctx context.Context) ([]backend.LoadedModel, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/api/ps", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ollama list loaded: status %d", resp.StatusCode)
	}
	var parsed struct {
		Models []struct {
			Name     string `json:"name"`
			SizeVRAM int64  `json:"size_vram"`
			Size     int64  `json:"size"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([]backend.LoadedModel, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		out = append(out, backend.LoadedModel{
			Name:     m.Name,
			SizeVRAM: m.SizeVRAM,
			SizeRAM:  m.Size,
			Backend:  b.name,
		})
	}
	return out, nil
}

func (b *Backend) Ping(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/", nil)
	if err != nil {
		b.healthy.Store(false)
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		b.healthy.Store(false)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b.healthy.Store(false)
		return fmt.Errorf("ollama unhealthy: status %d", resp.StatusCode)
	}
	b.healthy.Store(true)
	return nil
}

func (b *Backend) IsHealthy() bool { return b.healthy.Load() }

func (b *Backend) postJSON(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("ollama %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
