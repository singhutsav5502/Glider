package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ToolCallDelta is a parsed model tool call (OpenAI-style).
type ToolCallDelta struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

// AgentLoopOpts configures the agentic tool loop.
type AgentLoopOpts struct {
	Refs     []Ref
	MaxSteps int // model↔tool rounds; <=0 uses DefaultAgentLoopMaxSteps
	// Complete is called with messages + tools JSON; returns assistant text and optional tool_calls.
	Complete func(ctx context.Context, messages []map[string]any, toolsJSON json.RawMessage) (text string, calls []ToolCallDelta, err error)
}

// DefaultAgentLoopMaxSteps is used when AgentLoopOpts.MaxSteps <= 0.
// Hoop stages override via completeWithTools (toolLoopMaxStepsStage / Parallel).
const DefaultAgentLoopMaxSteps = 20

// maxTruncatedToolJSONRetries caps "continue incomplete tool JSON" recovery rounds
// so a stuck local model cannot burn the whole MaxSteps budget.
const maxTruncatedToolJSONRetries = 2

// AgentLoopResult is the final text plus all tool invocations.
type AgentLoopResult struct {
	Text    string
	Results []Result
	Steps   int
}

// RunAgentLoop drives model ↔ tool until no tool_calls or MaxSteps.
func (r *Registry) RunAgentLoop(ctx context.Context, system, user string, opts AgentLoopOpts) (AgentLoopResult, error) {
	out := AgentLoopResult{}
	if r == nil {
		return out, fmt.Errorf("nil registry")
	}
	if opts.Complete == nil {
		return out, fmt.Errorf("Complete required")
	}
	max := opts.MaxSteps
	if max <= 0 {
		max = DefaultAgentLoopMaxSteps
	}
	toolsJSON := r.OpenAIToolsJSON(ctx, opts.Refs)
	messages := []map[string]any{
		{"role": "system", "content": system},
		{"role": "user", "content": user},
	}
	expanded := r.ExpandRefs(ctx, opts.Refs)
	refByName := map[string]Ref{}
	for _, ref := range expanded {
		refByName[ref.Name] = ref
	}

	truncatedRetries := 0
	for step := 0; step < max; step++ {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		text, calls, err := opts.Complete(ctx, messages, toolsJSON)
		if err != nil {
			return out, err
		}
		out.Steps = step + 1
		// Local models often print tool-call JSON as plain text instead of
		// OpenAI tool_calls — recover and execute those so the loop continues.
		if len(calls) == 0 {
			calls = ParseTextToolCalls(text)
		}
		if len(calls) == 0 {
			// Truncated artifact_write / TOOL_CALLS JSON must not become the "final answer".
			if LooksLikeTruncatedToolJSON(text) && truncatedRetries < maxTruncatedToolJSONRetries && step+1 < max {
				truncatedRetries++
				messages = append(messages,
					map[string]any{"role": "assistant", "content": text},
					map[string]any{
						"role": "user",
						"content": "Your previous tool-call JSON was truncated mid-arguments (incomplete JSON). " +
							"Re-emit ONE complete tool call as valid JSON only (full arguments), " +
							"or finish with a plain-text stage answer if no further tools are needed. " +
							"Do not repeat partial content or treat the truncated blob as final.",
					},
				)
				continue
			}
			out.Text = text
			return out, nil
		}
		// Record assistant turn with tool_calls.
		asst := map[string]any{"role": "assistant", "content": text}
		tcPayload := make([]map[string]any, 0, len(calls))
		for _, c := range calls {
			tcPayload = append(tcPayload, map[string]any{
				"id":   c.ID,
				"type": "function",
				"function": map[string]any{
					"name":      c.Name,
					"arguments": c.Arguments,
				},
			})
		}
		asst["tool_calls"] = tcPayload
		messages = append(messages, asst)

		for _, c := range calls {
			name := strings.TrimSpace(c.Name)
			if name == "*" {
				name = "list_tools" // never CallTool("*")
			}
			ref, ok := refByName[name]
			if !ok {
				// Do not invent builtins from text-parsed JSON (e.g. parallel workers
				// emitting git_clone / nested plan→git_clone when clone is not in refs).
				// Rejection is soft: loop continues so the model can finish without that tool.
				res := Result{
					Name: name, Kind: KindBuiltin, OK: false,
					Err: fmt.Sprintf(
						"tool %q not allowed in this stage — continue with allowed tools or finish your answer; do not copy this rejection into the plan/report",
						name,
					),
				}
				out.Results = append(out.Results, res)
				messages = append(messages, map[string]any{
					"role":         "tool",
					"tool_call_id": c.ID,
					"name":         c.Name,
					"content":      "error: " + res.Err,
				})
				continue
			}
			input := FlattenToolArgs(name, c.Arguments)
			res, invErr := r.Invoke(ctx, ref, input)
			if invErr != nil && res.Err == "" {
				res.Err = invErr.Error()
				res.OK = false
			}
			out.Results = append(out.Results, res)
			content := res.Output
			if !res.OK && res.Err != "" {
				content = "error: " + res.Err + "\n" + content
			}
			messages = append(messages, map[string]any{
				"role":         "tool",
				"tool_call_id": c.ID,
				"name":         c.Name,
				"content":      content,
			})
		}
	}
	out.Text = fmt.Sprintf(
		"tool loop budget exhausted after %d steps (max %d); model kept requesting tools without a final answer — stop tools and emit the stage output (e.g. SCORE: x for critics)",
		max, max,
	)
	return out, nil
}
