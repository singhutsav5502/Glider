package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Message is one chat turn. Path A preserves tool-loop fields so Cursor's
// client-side tool results round-trip to OpenAI-compat / Ollama backends.
type Message struct {
	Role       string          `json:"role"`
	Content    string          `json:"content"`
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  json.RawMessage `json:"tool_calls,omitempty"`
}

// ToolsUnsupportedError is returned when a backend rejects tools[] (common on
// Ollama models without tool-calling). FallbackChain then tries the next step
// (typically BYOK cloud). See planning/smart_routing_and_local_tools.md.
type ToolsUnsupportedError struct {
	Backend string
	Message string
}

func (e *ToolsUnsupportedError) Error() string {
	if e == nil {
		return "tools unsupported"
	}
	if e.Backend != "" {
		return e.Backend + " tools unsupported: " + e.Message
	}
	return "tools unsupported: " + e.Message
}

// IsToolsUnsupported reports whether err (or any wrapped cause) indicates the
// backend cannot handle tools on this model.
func IsToolsUnsupported(err error) bool {
	if err == nil {
		return false
	}
	var te *ToolsUnsupportedError
	if errors.As(err, &te) {
		return true
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "does not support tools") ||
		strings.Contains(s, "tool calling is not supported") ||
		strings.Contains(s, "tools are not supported") ||
		strings.Contains(s, "unsupported tool") ||
		(strings.Contains(s, "tool") && strings.Contains(s, "not support"))
}

type Priority int

const (
	PriorityLow  Priority = 0
	PriorityHigh Priority = 1
)

type CompletionRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream"`
	Temperature *float64  `json:"temperature,omitempty"`
	MaxTokens   *int      `json:"max_tokens,omitempty"`
	// Tools / ToolChoice are first-class Path A fields (OpenAI or Anthropic-normalized).
	// Stored as RawMessage so schemas pass through to Ollama/vLLM/OpenAI unchanged.
	// Stream tool_calls are parsed into CompletionChunk.ToolCalls and re-emitted on the
	// gateway SSE (M2 bridge). See planning/smart_routing_and_local_tools.md.
	Tools      json.RawMessage `json:"tools,omitempty"`
	ToolChoice json.RawMessage `json:"tool_choice,omitempty"`
	// Format is Ollama-native structured output: JSON string "json" or a JSON Schema object.
	// The Ollama backend attaches it as body["format"] (and maps response_format for
	// OpenAI-compat). Other backends ignore it. Used by local critic stages.
	Format   json.RawMessage `json:"format,omitempty"`
	Metadata RequestMetadata `json:"-"`
}

// HasTools reports whether the request includes a non-empty tools array.
func (r *CompletionRequest) HasTools() bool {
	if r == nil {
		return false
	}
	s := bytes.TrimSpace(r.Tools)
	if len(s) == 0 || bytes.Equal(s, []byte("null")) || bytes.Equal(s, []byte("[]")) {
		return false
	}
	return true
}

// ToolNames extracts function/tool names from Tools JSON (OpenAI or Anthropic-ish).
// Returns nil when Tools is empty or unparseable.
func (r *CompletionRequest) ToolNames() []string {
	if r == nil || !r.HasTools() {
		return nil
	}
	return ParseToolNames(r.Tools)
}

type RequestMetadata struct {
	RequestID       string
	EstimatedTokens int
	Priority        Priority
	OriginalModel   string
	Adapter         string
	// ComplexityScore is 0–100 after the complexity rule / scorer runs.
	ComplexityScore int
	// ComplexitySource is "cursor" | "heuristic" | "" (unset).
	ComplexitySource string
	// CursorComplexity is a Cursor-estimated score when extract finds one on the
	// wire (0–100). HasCursorComplexity distinguishes "missing" from score 0.
	// Not present in MITM dumps / BidiAppend inspect as of 2026-07-18; plug-in
	// point for when Cursor exposes complexity / max_mode / tier.
	CursorComplexity    int
	HasCursorComplexity bool
	// Path B sticky / wrap-up signals (MITM → routing engine). Not serialized.
	StickyCloudLive bool   // StickyCloud TTL map or contextgraph cloud family live
	LastRouteCloud  bool   // session/turn-family last route was cloud (may be past grace)
	ExtractSource   string // tiptap_text | printable_hint | section_fallback
	WrapupScan      string // body/hint chrome scan for composer_wrapup_origin
}

// ToolCallDelta is one OpenAI-compat streaming tool_call fragment.
type ToolCallDelta struct {
	Index    int            `json:"index"`
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"`
	Function *FunctionDelta `json:"function,omitempty"`
}

// FunctionDelta carries incremental function name/arguments for a tool call.
type FunctionDelta struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type CompletionChunk struct {
	ID           string          `json:"id"`
	Content      string          `json:"content"`
	ToolCalls    []ToolCallDelta `json:"tool_calls,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
	Model        string          `json:"model"`
}

type ExecutionStrategy string

const (
	StrategySingle   ExecutionStrategy = "single"
	StrategyFanOut   ExecutionStrategy = "fan_out"
	StrategyPipeline ExecutionStrategy = "pipeline"
	StrategyEnsemble ExecutionStrategy = "ensemble"
)

type SubTask struct {
	Prompt string
	Target string
	Model  string
}

type RoutingDecision struct {
	Strategy    ExecutionStrategy
	Target      string // "local" | "cloud"
	BackendName string
	Model       string
	Adapter     string
	RuleName    string
	Reason      string
	// Role is an optional classifier hint: plan | exec | research (empty = unset).
	Role     string
	SubTasks []SubTask
}

type BackendType string

const (
	BackendTypeLocal BackendType = "local"
	BackendTypeCloud BackendType = "cloud"
)

type LoadOptions struct {
	NumGPULayers int
	KeepAlive    time.Duration
	GPUIndex     int
}

type LoadedModel struct {
	Name      string
	SizeVRAM  int64
	SizeRAM   int64
	ExpiresAt time.Time
	Backend   string
}

type ModelState string

const (
	ModelStateCold      ModelState = "COLD"
	ModelStateLoading   ModelState = "LOADING"
	ModelStateWarm      ModelState = "WARM"
	ModelStateUnloading ModelState = "UNLOADING"
)

type ModelInfo struct {
	Name           string
	Backend        string
	VRAMEstimateMB int
	MaxContext     int
	Capabilities   []string
	Adapter        string
	KeepWarm       bool
	State          ModelState
}
