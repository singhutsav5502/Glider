package orchestrator_test

import (
	"testing"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/orchestrator"
)

func TestApplyModelAlias(t *testing.T) {
	req := &backend.CompletionRequest{Model: "gpt-4o"}
	orchestrator.ApplyModelAlias(req, map[string]string{"gpt-4o": "codellama:7b"})
	if req.Model != "codellama:7b" {
		t.Fatalf("model=%s", req.Model)
	}
	if req.Metadata.OriginalModel != "gpt-4o" {
		t.Fatalf("original=%s", req.Metadata.OriginalModel)
	}
}

func TestApplyModelAliasNoMatch(t *testing.T) {
	req := &backend.CompletionRequest{Model: "claude-3"}
	orchestrator.ApplyModelAlias(req, map[string]string{"gpt-4o": "codellama:7b"})
	if req.Model != "claude-3" {
		t.Fatalf("model=%s", req.Model)
	}
}
