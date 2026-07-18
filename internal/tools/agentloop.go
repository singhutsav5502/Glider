package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// OpenAIToolsJSON builds an OpenAI-compatible tools[] array from schemas / refs.
func (r *Registry) OpenAIToolsJSON(ctx context.Context, refs []Ref) json.RawMessage {
	if r == nil {
		return json.RawMessage("[]")
	}
	var schemas []Schema
	if len(refs) == 0 {
		schemas = r.Catalog(ctx)
	} else {
		cat := r.Catalog(ctx)
		byKey := map[string]Schema{}
		for _, s := range cat {
			byKey[s.Name+"|"+string(s.Kind)+"|"+s.Server] = s
			byKey[s.Name] = s
		}
		for _, ref := range refs {
			if s, ok := byKey[ref.Name+"|"+string(ref.Kind)+"|"+ref.Server]; ok {
				schemas = append(schemas, s)
				continue
			}
			if s, ok := byKey[ref.Name]; ok {
				schemas = append(schemas, s)
				continue
			}
			// Minimal schema for declared but unknown tools.
			schemas = append(schemas, Schema{
				Name: ref.Name, Kind: ref.Kind, Server: ref.Server,
				Description: "declared tool " + ref.Name,
				InputSchema: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string"}}}`),
			})
		}
	}
	type fn struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	}
	type tool struct {
		Type     string `json:"type"`
		Function fn     `json:"function"`
	}
	out := make([]tool, 0, len(schemas))
	for _, s := range schemas {
		params := s.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, tool{
			Type: "function",
			Function: fn{
				Name:        s.Name,
				Description: s.Description,
				Parameters:  params,
			},
		})
	}
	b, _ := json.Marshal(out)
	return b
}

// ToolCallDelta is a parsed model tool call (OpenAI-style).
type ToolCallDelta struct {
	ID        string `json:"id,omitempty"`
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

// AgentLoopOpts configures the agentic tool loop.
type AgentLoopOpts struct {
	Refs     []Ref
	MaxSteps int
	// Complete is called with messages + tools JSON; returns assistant text and optional tool_calls.
	Complete func(ctx context.Context, messages []map[string]any, toolsJSON json.RawMessage) (text string, calls []ToolCallDelta, err error)
}

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
		max = 8
	}
	toolsJSON := r.OpenAIToolsJSON(ctx, opts.Refs)
	messages := []map[string]any{
		{"role": "system", "content": system},
		{"role": "user", "content": user},
	}
	refByName := map[string]Ref{}
	for _, ref := range opts.Refs {
		refByName[ref.Name] = ref
	}

	for step := 0; step < max; step++ {
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		text, calls, err := opts.Complete(ctx, messages, toolsJSON)
		if err != nil {
			return out, err
		}
		out.Steps = step + 1
		if len(calls) == 0 {
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
			ref, ok := refByName[c.Name]
			if !ok {
				ref = Ref{Name: c.Name, Kind: KindBuiltin}
			}
			input := c.Arguments
			if input == "" {
				input = "{}"
			}
			// Prefer plain string "input" field when present.
			var argsMap map[string]any
			if json.Unmarshal([]byte(input), &argsMap) == nil {
				if v, ok := argsMap["input"].(string); ok {
					input = v
				} else if v, ok := argsMap["path"].(string); ok && argsMap["content"] == nil {
					input = v
				} else if v, ok := argsMap["query"].(string); ok {
					input = v
				} else if v, ok := argsMap["command"].(string); ok {
					input = v
				} else if v, ok := argsMap["url"].(string); ok {
					input = v
				} else if v, ok := argsMap["expr"].(string); ok {
					input = v
				}
			}
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
	out.Text = "tool loop budget exhausted"
	return out, nil
}

// FormatToolResults builds a prompt injection block from Invoke results.
func FormatToolResults(results []Result) string {
	if len(results) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[tool_results]\n")
	for _, r := range results {
		b.WriteString("- ")
		b.WriteString(r.Name)
		b.WriteString(" ok=")
		b.WriteString(fmt.Sprintf("%t", r.OK))
		if r.Stubbed {
			b.WriteString(" stubbed")
		}
		b.WriteByte('\n')
		out := r.Output
		if len(out) > 4000 {
			out = out[:4000] + "\n...truncated"
		}
		if out != "" {
			b.WriteString(out)
			b.WriteByte('\n')
		}
		if r.Err != "" {
			b.WriteString("err: ")
			b.WriteString(r.Err)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// InvokeAllParallel runs refs concurrently (bounded by GOMAXPROCS-ish wait group).
func (r *Registry) InvokeAllParallel(ctx context.Context, refs []Ref, input string) []Result {
	if r == nil || len(refs) == 0 {
		return nil
	}
	out := make([]Result, len(refs))
	var wg sync.WaitGroup
	for i, ref := range refs {
		wg.Add(1)
		go func(i int, ref Ref) {
			defer wg.Done()
			res, err := r.Invoke(ctx, ref, input)
			if err != nil && res.Err == "" {
				res.Err = err.Error()
				res.OK = false
			}
			out[i] = res
		}(i, ref)
	}
	wg.Wait()
	return out
}

// InvokeAll runs refs sequentially (kept for compatibility).
func (r *Registry) InvokeAll(ctx context.Context, refs []Ref, input string) []Result {
	var out []Result
	for _, ref := range refs {
		res, err := r.Invoke(ctx, ref, input)
		if err != nil && res.Err == "" {
			res.Err = err.Error()
			res.OK = false
		}
		out = append(out, res)
	}
	return out
}
