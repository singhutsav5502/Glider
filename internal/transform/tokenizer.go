package transform

import (
	"github.com/glider-ai/glider/internal/backend"
	"github.com/pkoukk/tiktoken-go"
)

// Tokenizer counts tokens using the cl100k_base BPE encoding.
type Tokenizer struct {
	enc *tiktoken.Tiktoken
}

// NewTokenizer creates a tokenizer backed by cl100k_base.
func NewTokenizer() (*Tokenizer, error) {
	enc, err := tiktoken.GetEncoding("cl100k_base")
	if err != nil {
		return nil, err
	}
	return &Tokenizer{enc: enc}, nil
}

// Count returns the token count for s. Empty input returns 0.
func (t *Tokenizer) Count(s string) int {
	if s == "" {
		return 0
	}
	return len(t.enc.Encode(s, nil, nil))
}

// EstimateRequestTokens returns an estimated token count for a completion request.
func (t *Tokenizer) EstimateRequestTokens(req *backend.CompletionRequest) int {
	if req == nil {
		return 0
	}
	total := 0
	for _, msg := range req.Messages {
		total += t.Count(msg.Content)
		total += 4 // per-message formatting overhead
	}
	if len(req.Messages) > 0 {
		total += 2 // conversation framing overhead
	}
	return total
}

func (t *Tokenizer) messageTokens(msg backend.Message) int {
	return t.Count(msg.Content) + 4
}
