package mitm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/api"
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/metrics"
	"github.com/glider-ai/glider/internal/orchestrator"
)

// Harness is the shared Tokenizer → Router → Transform → Execute pipeline.
// MITM calls CompleteLocal so non-local decisions become origin passthrough.
type Harness interface {
	CompleteLocal(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error)
}

// Interceptor attempts local fulfillment for OpenAI-compatible (and Responses) bodies
// through the same harness as the BYOK gateway. Unrecognized or non-local decisions
// result in handled=false (origin passthrough to the original Cursor upstream Host).
type Interceptor struct {
	Harness Harness
	Metrics *metrics.Collector
	Log     *slog.Logger
	Passthrough bool // if true, never fulfill locally
}

// TryHandle implements LocalHandler.
func (i *Interceptor) TryHandle(w http.ResponseWriter, r *http.Request) (bool, error) {
	start := time.Now()
	log := i.log()
	host := r.Host
	if host == "" && r.URL != nil {
		host = r.URL.Host
	}
	path := ""
	if r.URL != nil {
		path = r.URL.Path
	}

	if i == nil || i.Passthrough || i.Harness == nil {
		return false, nil
	}
	if r.Method != http.MethodPost {
		return false, nil
	}
	isChat := strings.HasSuffix(path, "/chat/completions") || path == "/v1/chat/completions"
	isResponses := strings.HasSuffix(path, "/responses") || path == "/v1/responses"
	if !isChat && !isResponses {
		log.Debug("mitm skip non-llm path", "host", host, "path", path, "method", r.Method)
		return false, nil
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		i.observe("error", host, path, "", "", "", 0, start)
		return false, err
	}
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	r.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))

	var req *backend.CompletionRequest
	var responsesMode bool
	if isResponses {
		req, err = api.ResponsesToCompletion(body)
		if err != nil {
			log.Info("mitm skip unparsed responses body → origin", "host", host, "path", path, "err", err)
			i.observe("skip", host, path, "", "", "", 0, start)
			return false, nil
		}
		responsesMode = true
	} else if api.LooksLikeResponses(body) {
		req, err = api.ResponsesToCompletion(body)
		if err != nil {
			log.Info("mitm skip unparsed responses-shaped chat body → origin", "host", host, "path", path, "err", err)
			i.observe("skip", host, path, "", "", "", 0, start)
			return false, nil
		}
		responsesMode = true
	} else {
		req, err = api.ParseCompletionRequest(body)
		if err != nil {
			log.Info("mitm skip unparsed chat body → origin", "host", host, "path", path, "err", err)
			i.observe("skip", host, path, "", "", "", 0, start)
			return false, nil
		}
	}

	if req.Metadata.RequestID == "" {
		req.Metadata.RequestID = fmt.Sprintf("mitm_%d", time.Now().UnixNano())
	}
	req.Metadata.OriginalModel = req.Model
	if req.Metadata.Priority == 0 {
		req.Metadata.Priority = backend.PriorityHigh
	}

	log.Info("mitm intercept",
		"id", req.Metadata.RequestID,
		"host", host,
		"path", path,
		"model", req.Model,
		"stream", req.Stream,
		"responses", responsesMode,
		"bytes", len(body),
	)

	chunks, err := i.Harness.CompleteLocal(r, req)
	if errors.Is(err, orchestrator.ErrOriginPassthrough) {
		// Pipeline already recorded origin_passthrough metrics.
		log.Info("mitm origin passthrough",
			"id", req.Metadata.RequestID,
			"host", host,
			"path", path,
			"model", req.Model,
			"tokens", req.Metadata.EstimatedTokens,
			"latency_ms", float64(time.Since(start).Microseconds())/1000.0,
		)
		return false, nil
	}
	if err != nil {
		log.Error("mitm local fulfill failed", "id", req.Metadata.RequestID, "host", host, "err", err)
		http.Error(w, err.Error(), http.StatusBadGateway)
		return true, nil
	}

	log.Info("mitm local fulfill",
		"id", req.Metadata.RequestID,
		"host", host,
		"path", path,
		"model", req.Model,
		"tokens", req.Metadata.EstimatedTokens,
	)

	if responsesMode {
		if req.Stream {
			return true, api.WriteResponsesSSE(w, req.Metadata.RequestID, req.Model, chunks)
		}
		return true, api.WriteResponsesJSON(w, req.Metadata.RequestID, req.Model, chunks)
	}
	if req.Stream {
		return true, writeChatSSE(w, req.Metadata.RequestID, req.Model, chunks)
	}
	return true, writeChatJSON(w, req.Metadata.RequestID, req.Model, chunks)
}

func (i *Interceptor) log() *slog.Logger {
	if i != nil && i.Log != nil {
		return i.Log
	}
	return slog.Default()
}

func (i *Interceptor) observe(action, host, path, model, orig, rule string, tokens int, start time.Time) {
	if i == nil || i.Metrics == nil {
		return
	}
	i.Metrics.Record(metrics.RequestRecord{
		ID:            fmt.Sprintf("mitm_%d", time.Now().UnixNano()),
		Mode:          "mitm",
		Action:        action,
		Route:         action,
		Model:         model,
		OriginalModel: orig,
		Host:          host,
		Path:          path,
		Rule:          rule,
		Tokens:        tokens,
		Latency:       time.Since(start),
	})
}

func writeChatSSE(w http.ResponseWriter, id, model string, chunks <-chan backend.CompletionChunk) error {
	return api.WriteChatSSE(w, id, model, chunks)
}

func writeChatJSON(w http.ResponseWriter, id, model string, chunks <-chan backend.CompletionChunk) error {
	return api.WriteChatJSON(w, id, model, chunks)
}

// PeekJSONField extracts a top-level string field without full unmarshal (debug helper).
func PeekJSONField(body []byte, field string) string {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		return ""
	}
	raw, ok := m[field]
	if !ok {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	return s
}
