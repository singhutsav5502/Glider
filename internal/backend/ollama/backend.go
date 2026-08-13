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
	baseURL string
	client  *http.Client
	healthy atomic.Bool
	name    string
}

func New(baseURL string) *Backend {
	return NewWithTimeout(baseURL, 10*time.Minute)
}

// NewWithTimeout builds an Ollama backend with an explicit HTTP client timeout.
// Local 14b + tool loops routinely exceed 2 minutes awaiting first tokens; default is 10m.
// Wired from thresholds.request_timeout in glider.yaml (see registerBackends).
func NewWithTimeout(baseURL string, timeout time.Duration) *Backend {
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	b := &Backend{
		baseURL: strings.TrimRight(baseURL, "/"),
		client: &http.Client{
			Timeout: timeout,
		},
		name: "ollama",
	}
	b.healthy.Store(false)
	return b
}

// SetRequestTimeout updates the HTTP client timeout used for Complete / LoadModel / etc.
func (b *Backend) SetRequestTimeout(timeout time.Duration) {
	if b == nil || timeout <= 0 {
		return
	}
	b.client = &http.Client{Timeout: timeout}
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
	ch, err := b.complete(ctx, req)
	// Schema format is version-dependent; fall back to format:"json" once.
	if err != nil && req != nil && len(bytes.TrimSpace(req.Format)) > 0 && !backend.FormatIsJSONMode(req.Format) && isFormatUnsupported(err) {
		fallback := *req
		fallback.Format = backend.CriticEvalFormatJSON()
		return b.complete(ctx, &fallback)
	}
	return ch, err
}

func (b *Backend) complete(ctx context.Context, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error) {
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
	// Pass tools through when present. Ollama OpenAI-compat accepts tools for
	// models that support tool-calling. Models without tools reject with a
	// tools-unsupported error → FallbackChain tries BYOK cloud (documented Path
	// A fallback). ParseOpenAIStreamPayload parses the tool_calls of a stream,
	// through the M2 bridge.
	backend.AttachTools(body, req)
	// Ollama structured outputs: native format ("json" | JSON schema) + OpenAI response_format.
	backend.AttachFormat(body, req)
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
		trimmed := strings.TrimSpace(string(msg))
		err := fmt.Errorf("ollama error: status %d: %s", resp.StatusCode, trimmed)
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

func isFormatUnsupported(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "format") ||
		strings.Contains(msg, "json_schema") ||
		strings.Contains(msg, "response_format") ||
		strings.Contains(msg, "structured") ||
		strings.Contains(msg, "invalid schema")
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

// ListTags returns model names available on the Ollama host (/api/tags).
func (b *Backend) ListTags(ctx context.Context) ([]string, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("ollama tags: status %d", resp.StatusCode)
	}
	var parsed struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(parsed.Models))
	for _, m := range parsed.Models {
		if m.Name != "" {
			out = append(out, m.Name)
		}
	}
	return out, nil
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
