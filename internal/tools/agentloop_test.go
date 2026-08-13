package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestDefaultAgentLoopMaxStepsFloor(t *testing.T) {
	if DefaultAgentLoopMaxSteps < 20 {
		t.Fatalf("DefaultAgentLoopMaxSteps=%d want >= 20", DefaultAgentLoopMaxSteps)
	}
	if ToolResultPromptCap < 16000 {
		t.Fatalf("ToolResultPromptCap=%d want >= 16000 for audit-sized tool inject", ToolResultPromptCap)
	}
}

func TestFormatToolResultsCapRaised(t *testing.T) {
	big := strings.Repeat("x", ToolResultPromptCap+500)
	out := FormatToolResults([]Result{{Name: "fs_list", OK: true, Output: big}})
	if !strings.Contains(out, "...truncated") {
		t.Fatal("expected truncate marker past cap")
	}
	if !strings.Contains(out, strings.Repeat("x", 1000)) {
		t.Fatal("expected retained body under cap")
	}
	// Must keep far more than the old 4k inject budget.
	if len(out) < 16000 {
		t.Fatalf("format too short after raise: %d", len(out))
	}
}

func TestLooksLikeTruncatedToolJSON(t *testing.T) {
	partial := `{"name":"artifact_write","arguments":{"kind":"out","path":"audit.md","content":"# Scope\nThis report cuts mid`
	if !LooksLikeTruncatedToolJSON(partial) {
		t.Fatal("expected truncated artifact_write JSON")
	}
	complete := `{"name":"datetime","arguments":{}}`
	if LooksLikeTruncatedToolJSON(complete) {
		t.Fatal("balanced JSON must not look truncated")
	}
	if LooksLikeTruncatedToolJSON("plain final answer with no tools") {
		t.Fatal("prose must not look truncated")
	}
}

func TestAgentLoopRetriesTruncatedToolJSON(t *testing.T) {
	r := NewRegistry(Options{})
	refs := []Ref{{Name: "datetime", Kind: KindBuiltin}}
	step := 0
	out, err := r.RunAgentLoop(context.Background(), "sys", "write then answer", AgentLoopOpts{
		Refs: refs, MaxSteps: 4,
		Complete: func(ctx context.Context, messages []map[string]any, toolsJSON json.RawMessage) (string, []ToolCallDelta, error) {
			step++
			if step == 1 {
				return `{"name":"artifact_write","arguments":{"content":"partial cut`, nil, nil
			}
			// After continue prompt, finish cleanly.
			for _, m := range messages {
				if m["role"] == "user" {
					c, _ := m["content"].(string)
					if strings.Contains(c, "truncated") {
						return "final audit summary SCORE: 0.9", nil, nil
					}
				}
			}
			return "unexpected", nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if step < 2 {
		t.Fatalf("expected retry after truncated JSON, steps=%d", step)
	}
	if !strings.Contains(out.Text, "final audit") {
		t.Fatalf("text=%q", out.Text)
	}
}

func TestAgentLoopContinuesAfterUndeclaredReject(t *testing.T) {
	r := NewRegistry(Options{})
	refs := []Ref{{Name: "datetime", Kind: KindBuiltin}}
	step := 0
	out, err := r.RunAgentLoop(context.Background(), "sys", "plan then answer", AgentLoopOpts{
		Refs: refs, MaxSteps: 4,
		Complete: func(ctx context.Context, messages []map[string]any, toolsJSON json.RawMessage) (string, []ToolCallDelta, error) {
			step++
			if step == 1 {
				return "", []ToolCallDelta{{ID: "1", Name: "git_clone", Arguments: `{"url":"https://example.com/r.git","dir":"x"}`}}, nil
			}
			for _, m := range messages {
				if m["role"] == "tool" {
					content, _ := m["content"].(string)
					if !strings.Contains(content, "not allowed in this stage") {
						t.Fatalf("expected soft reject content, got %q", content)
					}
					if !strings.Contains(content, "continue") {
						t.Fatalf("expected continue guidance, got %q", content)
					}
					return "1) clone_fetch owns clone\n2) audit", nil, nil
				}
			}
			return "fallback", nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Steps < 2 {
		t.Fatalf("expected loop to continue after reject, steps=%d", out.Steps)
	}
	if !strings.Contains(out.Text, "clone_fetch") {
		t.Fatalf("text=%q", out.Text)
	}
	if len(out.Results) != 1 || out.Results[0].OK {
		t.Fatalf("results=%+v", out.Results)
	}
}
