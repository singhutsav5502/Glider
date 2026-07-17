package transform

import (
	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
)

// Transformer applies optional request transformations.
type Transformer struct {
	cfg       config.TransformConfig
	tokenizer *Tokenizer
}

// NewTransformer creates a transformer with the given config and tokenizer.
func NewTransformer(cfg config.TransformConfig, tokenizer *Tokenizer) *Transformer {
	return &Transformer{cfg: cfg, tokenizer: tokenizer}
}

// Apply runs the transform pipeline. When disabled, the request is returned unchanged.
func (tr *Transformer) Apply(req *backend.CompletionRequest, maxContext int) *backend.CompletionRequest {
	if req == nil {
		return nil
	}
	if !tr.cfg.Enabled {
		return cloneRequest(req)
	}

	out := cloneRequest(req)
	if tr.cfg.TrimContext {
		out = tr.TrimContext(out, maxContext)
	}
	if tr.cfg.AugmentPrepend != "" || tr.cfg.AugmentAppend != "" {
		out = tr.Augment(out)
	}
	return out
}

// TrimContext preserves the first and last messages, truncates middle context,
// and ensures the result fits within limit tokens.
func (tr *Transformer) TrimContext(req *backend.CompletionRequest, limit int) *backend.CompletionRequest {
	if req == nil || len(req.Messages) == 0 {
		return cloneRequest(req)
	}
	if tr.tokenizer.EstimateRequestTokens(req) <= limit {
		return cloneRequest(req)
	}

	out := cloneRequest(req)
	msgs := out.Messages
	if len(msgs) == 1 {
		return out
	}

	first := msgs[0]
	last := msgs[len(msgs)-1]
	middle := append([]backend.Message(nil), msgs[1:len(msgs)-1]...)

	kept := []backend.Message{first}
	keptTokens := tr.tokenizer.messageTokens(first)

	lastTokens := tr.tokenizer.messageTokens(last)

	// Keep the most recent middle messages that fit under the limit.
	type indexed struct {
		idx int
		msg backend.Message
	}
	recent := make([]indexed, 0, len(middle))
	for i := len(middle) - 1; i >= 0; i-- {
		msg := middle[i]
		msgTokens := tr.tokenizer.messageTokens(msg)
		if keptTokens+msgTokens+lastTokens <= limit {
			recent = append(recent, indexed{idx: i, msg: msg})
			keptTokens += msgTokens
		}
	}
	for i := len(recent) - 1; i >= 0; i-- {
		kept = append(kept, recent[i].msg)
	}
	kept = append(kept, last)

	if keptTokens+lastTokens > limit {
		kept = []backend.Message{first, truncateMessage(last, limit-keptTokens, tr.tokenizer)}
	}

	out.Messages = kept
	return out
}

// Augment prepends and/or appends user-defined instructions without removing
// existing system prompts. Prepend is inserted immediately after the first message.
func (tr *Transformer) Augment(req *backend.CompletionRequest) *backend.CompletionRequest {
	if req == nil {
		return nil
	}
	if tr.cfg.AugmentPrepend == "" && tr.cfg.AugmentAppend == "" {
		return cloneRequest(req)
	}

	out := cloneRequest(req)
	insertAt := 0
	if tr.cfg.AugmentPrepend != "" {
		augment := backend.Message{Role: "system", Content: tr.cfg.AugmentPrepend}
		if len(out.Messages) > 0 && out.Messages[0].Role == "system" {
			insertAt = 1
		}
		out.Messages = append(out.Messages[:insertAt], append([]backend.Message{augment}, out.Messages[insertAt:]...)...)
	}
	if tr.cfg.AugmentAppend != "" {
		out.Messages = append(out.Messages, backend.Message{Role: "system", Content: tr.cfg.AugmentAppend})
	}
	return out
}

func cloneRequest(req *backend.CompletionRequest) *backend.CompletionRequest {
	if req == nil {
		return nil
	}
	out := *req
	if req.Messages != nil {
		out.Messages = append([]backend.Message(nil), req.Messages...)
	}
	return &out
}

func truncateMessage(msg backend.Message, maxTokens int, tok *Tokenizer) backend.Message {
	if maxTokens <= 4 {
		return backend.Message{Role: msg.Role, Content: ""}
	}
	contentBudget := maxTokens - 4
	content := msg.Content
	if tok.Count(content) <= contentBudget {
		return msg
	}

	low, high := 0, len(content)
	for low < high {
		mid := (low + high + 1) / 2
		if tok.Count(content[:mid]) <= contentBudget {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return backend.Message{Role: msg.Role, Content: content[:low]}
}
