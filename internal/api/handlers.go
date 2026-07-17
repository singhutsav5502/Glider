package api

import (
	"encoding/json"
	"net/http"

	"github.com/glider-ai/glider/internal/backend"
)

// Completer handles a completion request end-to-end (route + execute).
type Completer interface {
	Complete(r *http.Request, req *backend.CompletionRequest) (<-chan backend.CompletionChunk, error)
}

// ModelLister lists available models for /v1/models.
type ModelLister interface {
	ListModelIDs() []string
}

type Handlers struct {
	Completer Completer
	Models    ModelLister
}

type openAIErrorBody struct {
	Error openAIError `json:"error"`
}

type openAIError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

func writeAPIError(w http.ResponseWriter, status int, msg, typ string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(openAIErrorBody{
		Error: openAIError{Message: msg, Type: typ},
	})
}

func (h *Handlers) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed", "invalid_request_error")
		return
	}
	var req backend.CompletionRequest
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid JSON: "+err.Error(), "invalid_request_error")
		return
	}
	if len(req.Messages) == 0 {
		writeAPIError(w, http.StatusBadRequest, "missing required field: messages", "invalid_request_error")
		return
	}
	req.Metadata.RequestID = RequestIDFromContext(r.Context())
	req.Metadata.OriginalModel = req.Model
	if req.Metadata.Priority == 0 {
		req.Metadata.Priority = backend.PriorityHigh
	}

	if h.Completer == nil {
		writeAPIError(w, http.StatusInternalServerError, "no completer configured", "server_error")
		return
	}
	chunks, err := h.Completer.Complete(r, &req)
	if err != nil {
		writeAPIError(w, http.StatusBadGateway, err.Error(), "server_error")
		return
	}
	if req.Stream {
		_ = writeSSE(w, req.Metadata.RequestID, req.Model, chunks)
		return
	}
	_ = writeNonStream(w, req.Metadata.RequestID, req.Model, chunks)
}

func (h *Handlers) ListModels(w http.ResponseWriter, r *http.Request) {
	ids := []string{}
	if h.Models != nil {
		ids = h.Models.ListModelIDs()
	}
	data := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		data = append(data, map[string]any{
			"id":      id,
			"object":  "model",
			"owned_by": "glider",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"object": "list",
		"data":   data,
	})
}

// ParseCompletionRequest exposes parsing for unit tests (T1.1.1).
func ParseCompletionRequest(body []byte) (*backend.CompletionRequest, error) {
	var req backend.CompletionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	if len(req.Messages) == 0 {
		return nil, errMissingMessages
	}
	return &req, nil
}

type missingMessagesError struct{}

func (missingMessagesError) Error() string { return "missing required field: messages" }

var errMissingMessages = missingMessagesError{}
