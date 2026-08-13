package vllm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/glider-ai/glider/internal/backend"
)

// Backend implements InferenceBackend, LoRAManager, ModelManager, HealthChecker for vLLM.
type Backend struct {
	baseURL  string
	client   *http.Client
	healthy  atomic.Bool
	name     string
	mu       sync.Mutex
	adapters map[string]string
}

func New(baseURL string) *Backend {
	return NewWithTimeout(baseURL, 10*time.Minute)
}

// NewWithTimeout builds a vLLM backend with an explicit HTTP client timeout.
func NewWithTimeout(baseURL string, timeout time.Duration) *Backend {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	b := &Backend{
		baseURL:  strings.TrimRight(baseURL, "/"),
		client:   &http.Client{Timeout: timeout},
		name:     "vllm",
		adapters: make(map[string]string),
	}
	b.healthy.Store(false)
	return b
}

func (b *Backend) Name() string              { return b.name }
func (b *Backend) Type() backend.BackendType { return backend.BackendTypeLocal }

var (
	_ backend.InferenceBackend = (*Backend)(nil)
	_ backend.LoRAManager      = (*Backend)(nil)
	_ backend.ModelManager     = (*Backend)(nil)
	_ backend.HealthChecker    = (*Backend)(nil)
)

func (b *Backend) Complete(ctx context.Context, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
	model := req.Model
	if req.Metadata.Adapter != "" {
		model = req.Metadata.Adapter
	}
	body := map[string]any{
		"model":    model,
		"messages": req.Messages,
		"stream":   true,
	}
	backend.AttachTools(body, req)
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/v1/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if req.Metadata.Adapter != "" {
		httpReq.Header.Set("X-LoRA-Adapter", req.Metadata.Adapter)
	}

	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("vllm complete: %w", err)
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		trimmed := strings.TrimSpace(string(msg))
		err := fmt.Errorf("vllm error: status %d: %s", resp.StatusCode, trimmed)
		if req.HasTools() && backend.IsToolsUnsupported(err) {
			return nil, &backend.ToolsUnsupportedError{Backend: b.name, Message: trimmed}
		}
		return nil, err
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
			chunk, ok := backend.ParseOpenAIStreamPayload(payload)
			if !ok {
				continue
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

func (b *Backend) LoadAdapter(ctx context.Context, name string, path string) error {
	body := map[string]any{"lora_name": name, "lora_path": path}
	if err := b.postJSON(ctx, "/v1/load_lora_adapter", body); err != nil {
		return err
	}
	b.mu.Lock()
	b.adapters[name] = path
	b.mu.Unlock()
	return nil
}

func (b *Backend) UnloadAdapter(ctx context.Context, name string) error {
	body := map[string]any{"lora_name": name}
	if err := b.postJSON(ctx, "/v1/unload_lora_adapter", body); err != nil {
		return err
	}
	b.mu.Lock()
	delete(b.adapters, name)
	b.mu.Unlock()
	return nil
}

func (b *Backend) ListAdapters(ctx context.Context) ([]string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, 0, len(b.adapters))
	for name := range b.adapters {
		out = append(out, name)
	}
	return out, nil
}

func (b *Backend) LoadModel(ctx context.Context, model string, opts backend.LoadOptions) error {
	// vLLM typically keeps a base model loaded; treat as no-op success for interface compliance.
	return nil
}

func (b *Backend) UnloadModel(ctx context.Context, model string) error {
	return nil
}

func (b *Backend) ListLoaded(ctx context.Context) ([]backend.LoadedModel, error) {
	return []backend.LoadedModel{}, nil
}

// ListModels returns model IDs advertised by the vLLM OpenAI-compatible /v1/models endpoint.
func (b *Backend) ListModels(ctx context.Context) ([]string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("vllm models: status %d", resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		if m.ID != "" {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

func (b *Backend) Ping(ctx context.Context) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/health", nil)
	if err != nil {
		b.healthy.Store(false)
		return err
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		// fallback to root
		httpReq2, _ := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/v1/models", nil)
		resp2, err2 := client.Do(httpReq2)
		if err2 != nil {
			b.healthy.Store(false)
			return err
		}
		defer resp2.Body.Close()
		if resp2.StatusCode >= 400 {
			b.healthy.Store(false)
			return fmt.Errorf("vllm unhealthy: status %d", resp2.StatusCode)
		}
		b.healthy.Store(true)
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b.healthy.Store(false)
		return fmt.Errorf("vllm unhealthy: status %d", resp.StatusCode)
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
		return fmt.Errorf("vllm %s: status %d: %s", path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
