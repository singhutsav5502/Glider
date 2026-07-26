package config

import "strings"

type Config struct {
	Server        ServerConfig        `yaml:"server" json:"server"`
	Thresholds    ThresholdConfig     `yaml:"thresholds" json:"thresholds"`
	VRAM          VRAMConfig          `yaml:"vram" json:"vram"`
	Models        []ModelConfig       `yaml:"models" json:"models"`
	ModelAliases  map[string]string   `yaml:"model_aliases" json:"model_aliases"`
	Routing       RoutingConfig       `yaml:"routing" json:"routing"`
	Orchestration OrchestrationConfig `yaml:"orchestration,omitempty" json:"orchestration,omitempty"`
	Cloud         CloudConfig         `yaml:"cloud" json:"cloud"`
	Backends      []BackendConfig     `yaml:"backends" json:"backends"`
	Dashboard     DashboardConfig     `yaml:"dashboard" json:"dashboard"`
	Transform     TransformConfig     `yaml:"transform" json:"transform"`
	MITM          MITMConfig          `yaml:"mitm" json:"mitm"`
	// Context tunes the hybrid contextgraph warm store (optional).
	Context ContextConfig `yaml:"context,omitempty" json:"context,omitempty"`
}

// ContextConfig controls contextgraph retention / warm reload.
type ContextConfig struct {
	// MaxEvents is the in-memory event ring cap (0 → 4096).
	MaxEvents int `yaml:"max_events,omitempty" json:"max_events,omitempty"`
	// RetainDays is how long events-*.jsonl files are kept on disk (0 → 14).
	RetainDays int `yaml:"retain_days,omitempty" json:"retain_days,omitempty"`
	// WarmLoadDays is how many days of JSONL to replay on startup (0 → 2).
	WarmLoadDays int `yaml:"warm_load_days,omitempty" json:"warm_load_days,omitempty"`
}

// OrchestrationConfig holds routing/fan-out feature flags.
type OrchestrationConfig struct {
	FanOut      FanOutConfig      `yaml:"fan_out,omitempty" json:"fan_out,omitempty"`
	Concurrency ConcurrencyConfig `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
	// Tools configures shell_exec allowlist and related agent tool policy.
	Tools ToolsConfig `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// ToolsConfig gates dangerous builtins (shell_exec default off) and sandbox root.
type ToolsConfig struct {
	// Workspace is the sandbox root for fs_*, git_*, shell_exec (absolute or relative to process cwd).
	// Empty → ~/.glider/workspace (created on start). Use "." only if you intentionally want the Glider repo.
	Workspace      string   `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	AllowShell     bool     `yaml:"allow_shell,omitempty" json:"allow_shell,omitempty"`
	ShellAllowlist []string `yaml:"shell_allowlist,omitempty" json:"shell_allowlist,omitempty"`
	// AllowHosts limits http_fetch / web_fetch (empty = allow any host).
	AllowHosts []string `yaml:"allow_hosts,omitempty" json:"allow_hosts,omitempty"`
	// WebSearch configures web_search provider + caps (agent-loop only; not blind-safe).
	WebSearch WebSearchConfig `yaml:"web_search,omitempty" json:"web_search,omitempty"`
}

// WebSearchConfig selects the web_search backend for local/Path A tool loops.
type WebSearchConfig struct {
	// Provider: auto|duckduckgo|brave|tavily|serpapi|searxng (default auto).
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	// MaxResults caps ranked hits (default 5).
	MaxResults int `yaml:"max_results,omitempty" json:"max_results,omitempty"`
	// BraveAPIKeyEnv defaults to BRAVE_SEARCH_API_KEY (also accepts BRAVE_API_KEY).
	BraveAPIKeyEnv string `yaml:"brave_api_key_env,omitempty" json:"brave_api_key_env,omitempty"`
	// TavilyAPIKeyEnv defaults to TAVILY_API_KEY.
	TavilyAPIKeyEnv string `yaml:"tavily_api_key_env,omitempty" json:"tavily_api_key_env,omitempty"`
	// SerpAPIKeyEnv defaults to SERPAPI_KEY.
	SerpAPIKeyEnv string `yaml:"serpapi_key_env,omitempty" json:"serpapi_key_env,omitempty"`
	// SearXNGURL is a self-hosted SearXNG base URL (or set SEARXNG_URL).
	SearXNGURL string `yaml:"searxng_url,omitempty" json:"searxng_url,omitempty"`
	// FetchMaxBytes caps web_fetch body size (default 65536).
	FetchMaxBytes int `yaml:"fetch_max_bytes,omitempty" json:"fetch_max_bytes,omitempty"`
}


// FanOutConfig enables StrategyFanOut on the gateway executor (max 2–4 workers).
type FanOutConfig struct {
	Enabled    bool `yaml:"enabled" json:"enabled"`
	MaxWorkers int  `yaml:"max_workers,omitempty" json:"max_workers,omitempty"`
}

// ConcurrencyConfig sizes backpressure channels for swarm/fan-out helpers.
// Zero values fall back to swarm package defaults.
type ConcurrencyConfig struct {
	WorkerQueueSize int `yaml:"worker_queue_size,omitempty" json:"worker_queue_size,omitempty"`
	ResultChanSize  int `yaml:"result_chan_size,omitempty" json:"result_chan_size,omitempty"`
	MaxInflight     int `yaml:"max_inflight,omitempty" json:"max_inflight,omitempty"`
}

type MITMConfig struct {
	Enabled            bool     `yaml:"enabled" json:"enabled"`
	Port               int      `yaml:"port" json:"port"`
	CACert             string   `yaml:"ca_cert" json:"ca_cert"`
	CAKey              string   `yaml:"ca_key" json:"ca_key"`
	Hosts              []string `yaml:"hosts" json:"hosts"`
	PassthroughDefault bool     `yaml:"passthrough_default" json:"passthrough_default"`
	// DebugAgentRPC enables Path B R&D observability: structured logs, optional body
	// dumps under ~/.glider/mitm-debug/, and GET /api/mitm/debug/recent.
	// Also enabled when env GLIDER_MITM_DEBUG_RPC is 1/true/yes (see ApplyMITMDebugEnv).
	DebugAgentRPC bool   `yaml:"debug_agent_rpc" json:"debug_agent_rpc"`
	DebugDumpDir  string `yaml:"debug_dump_dir,omitempty" json:"debug_dump_dir,omitempty"`
	// AgentRPCFulfill enables experimental Path B: BidiAppend context_envelope
	// extract → DecideLocal → RunSSE text fulfill when correlated. Fail-soft to
	// origin when wait times out or encode fails. Default false (safe).
	// Also: GLIDER_MITM_AGENT_RPC_FULFILL=1 (see ApplyMITMDebugEnv).
	AgentRPCFulfill bool `yaml:"agent_rpc_fulfill" json:"agent_rpc_fulfill"`
	// AgentRPCCannedOnError, when fulfill is on and CompleteLocal fails (e.g. Ollama
	// down), writes a canned RunSSE text stream instead of origin passthrough.
	// Lets Cursor UI acceptance of the Path B codec be tested without a local backend.
	// Also: GLIDER_MITM_AGENT_RPC_CANNED=1. Default false (safe).
	AgentRPCCannedOnError bool `yaml:"agent_rpc_canned_on_error" json:"agent_rpc_canned_on_error"`
	// AgentRPCCannedText is the synthetic reply used when canned-on-error fires.
	// Empty → default "pong from glider (canned Path B)".
	AgentRPCCannedText string `yaml:"agent_rpc_canned_text,omitempty" json:"agent_rpc_canned_text,omitempty"`
	// AgentRPCToolCodec enables Path B child/tool-loop RunSSE fulfill (tool_call
	// frames). Default false — keep Mode A for Agent+tools demos. Also:
	// GLIDER_MITM_AGENT_RPC_TOOL_CODEC=1. Requires AgentRPCFulfill.
	AgentRPCToolCodec bool `yaml:"agent_rpc_tool_codec" json:"agent_rpc_tool_codec"`
	// OriginOnLocalError: when CompleteLocal fails after a local arm and canned is
	// off, fail-soft to Cursor origin (hybrid default). Set false for pure-local
	// so Cursor sees a clear Glider/Ollama error via RunSSE instead of subscription.
	// Nil → true (origin fail-soft).
	OriginOnLocalError *bool `yaml:"origin_on_local_error,omitempty" json:"origin_on_local_error,omitempty"`
	// RequireLocalHealthy gates ArmLocal on a live Ollama (or local) health check.
	// When true and local is down, Path B does not arm local (avoids silent origin).
	RequireLocalHealthy bool `yaml:"require_local_healthy,omitempty" json:"require_local_healthy,omitempty"`

	// Transparent enables OS-level packet interception (see
	// planning/transparent_redirector_design.md) — the primary transport for
	// vendors that don't cooperate via a proxy setting or base-URL env var
	// (and, per that design doc, the default even for ones that do). Windows
	// only for now (WinDivert); other OSes return a clear "not implemented"
	// error from Proxy.Start rather than silently no-op'ing.
	Transparent bool `yaml:"transparent,omitempty" json:"transparent,omitempty"`
	// TransparentPort is Glider's local ingress for redirected connections —
	// distinct from Port (the CONNECT-based listener above).
	TransparentPort int `yaml:"transparent_port,omitempty" json:"transparent_port,omitempty"`
	// TransparentPorts are the destination ports intercepted system-wide.
	// Empty → []int{443}.
	TransparentPorts []int `yaml:"transparent_ports,omitempty" json:"transparent_ports,omitempty"`
	// WinDivertDLLPath points at WinDivert.dll (WinDivertNN.sys must sit
	// alongside it — WinDivert's own requirement). Empty → Transparent is a
	// no-op even if true, since there's nothing to load.
	WinDivertDLLPath string `yaml:"windivert_dll_path,omitempty" json:"windivert_dll_path,omitempty"`
}

// OriginOnLocalErrorOrDefault is true unless explicitly set false.
func (c MITMConfig) OriginOnLocalErrorOrDefault() bool {
	if c.OriginOnLocalError == nil {
		return true
	}
	return *c.OriginOnLocalError
}

type ServerConfig struct {
	ProxyPort     int    `yaml:"proxy_port" json:"proxy_port"`
	DashboardPort int    `yaml:"dashboard_port" json:"dashboard_port"`
	LogLevel      string `yaml:"log_level" json:"log_level"`
}

type ThresholdConfig struct {
	MaxLocalContextTokens int `yaml:"max_local_context_tokens" json:"max_local_context_tokens"`
	IdleUnloadTimeout     string `yaml:"idle_unload_timeout" json:"idle_unload_timeout"`
	// RequestTimeout is the HTTP client timeout for local backends (Ollama/vLLM Complete).
	// Raise for large local models + tool loops (e.g. 14b with 20–28 steps); default 10m.
	// Key: thresholds.request_timeout (Go duration string: 10m, 600s, …).
	RequestTimeout string `yaml:"request_timeout" json:"request_timeout"`
	// DefaultMaxTokens is max_tokens sent on hoop/swarm Completes (Ollama maps to num_predict).
	// 0 → loop.DefaultCompletionMaxTokens (8192). Raise for long audit reports / artifact_write.
	DefaultMaxTokens int `yaml:"default_max_tokens,omitempty" json:"default_max_tokens,omitempty"`
}

type VRAMConfig struct {
	Strategy        string         `yaml:"strategy" json:"strategy"`
	HeadroomMB      int            `yaml:"headroom_mb" json:"headroom_mb"`
	MaxLoadedModels int            `yaml:"max_loaded_models" json:"max_loaded_models"`
	GPUAssignments  map[string]int `yaml:"gpu_assignments" json:"gpu_assignments"`
}

type ModelConfig struct {
	Name           string   `yaml:"name" json:"name"`
	Backend        string   `yaml:"backend" json:"backend"`
	VRAMEstimateMB int      `yaml:"vram_estimate_mb" json:"vram_estimate_mb"`
	MaxContext     int      `yaml:"max_context" json:"max_context"`
	Capabilities   []string `yaml:"capabilities" json:"capabilities"`
	Adapter        string   `yaml:"adapter,omitempty" json:"adapter,omitempty"`
	KeepWarm       bool     `yaml:"keep_warm" json:"keep_warm"`
	Adapters       []struct {
		Name string `yaml:"name" json:"name"`
		Path string `yaml:"path" json:"path"`
	} `yaml:"adapters,omitempty" json:"adapters,omitempty"`
}

type RoutingConfig struct {
	Rules []RuleConfig `yaml:"rules" json:"rules"`
	// Default is "local" | "cloud" | "". When set, rewrites/injects the priority-0
	// always rule target so profiles can flip pure-local without editing every rule.
	// See configs/glider.local.yaml.
	Default string `yaml:"default,omitempty" json:"default,omitempty"`
	// DefaultLocalModel is used when Default=local and an always rule needs a model.
	DefaultLocalModel string `yaml:"default_local_model,omitempty" json:"default_local_model,omitempty"`
	// AllowCloudFallback: when false, FallbackChain never appends BYOK cloud after
	// a local primary (pure-local). Nil → true (hybrid).
	AllowCloudFallback *bool `yaml:"allow_cloud_fallback,omitempty" json:"allow_cloud_fallback,omitempty"`
	// TaskClassifier injects heuristic rules (tools → cloud, must-cloud keywords,
	// small-local keywords) when Enabled. Priorities default below explicit /cloud
	// (99) and above context_size (10). See planning/smart_routing_and_local_tools.md.
	TaskClassifier TaskClassifierConfig `yaml:"task_classifier,omitempty" json:"task_classifier,omitempty"`
	// ComplexityFrom selects the complexity score source for routing:
	//   heuristic — Glider MVP score (tools / files / prompt length / mode strings)
	//   cursor    — only Metadata.CursorComplexity when extract finds a Cursor field
	//   both      — prefer Cursor when present, else heuristic
	// Empty → heuristic. Cursor wire fields are not exposed in MITM dumps today;
	// see planning/smart_routing_and_local_tools.md §Complexity.
	ComplexityFrom string `yaml:"complexity_from,omitempty" json:"complexity_from,omitempty"`
	// Complexity tunes the injected complexity→cloud rule (optional knobs).
	Complexity ComplexityConfig `yaml:"complexity,omitempty" json:"complexity,omitempty"`
	// TurnFamilyTTL is how long a DecideLocal / explicit turn family stays open for
	// reply-summary / title follow-ons (e.g. "90s"). Empty → 90s. Not conversation-wide.
	// See planning/routing_session_policy.md.
	TurnFamilyTTL string `yaml:"turn_family_ttl,omitempty" json:"turn_family_ttl,omitempty"`
	// ToolFollowup controls per-tool-step re-decide for child RunSSE / tool loops
	// after a parent cloud|local decision. Path B logs would_local and fulfills
	// when HasRunSSEToolCodec; otherwise origin. Path A routes allowlisted tools locally.
	ToolFollowup ToolFollowupConfig `yaml:"tool_followup,omitempty" json:"tool_followup,omitempty"`
}

// ComplexityConfig controls the optional complexity→cloud routing rule.
// Disabled by default so existing classifier priorities stay unchanged until opted in.
type ComplexityConfig struct {
	// Enabled injects a complexity rule when true (default false).
	Enabled bool `yaml:"enabled" json:"enabled"`
	// CloudAbove routes to cloud when score >= this (0–100). Default 70.
	CloudAbove int `yaml:"cloud_above,omitempty" json:"cloud_above,omitempty"`
	// Priority of the injected rule. Default 75 (below must-cloud 80, above small-local 70).
	Priority     int    `yaml:"priority,omitempty" json:"priority,omitempty"`
	CloudBackend string `yaml:"cloud_backend,omitempty" json:"cloud_backend,omitempty"`
	CloudModel   string `yaml:"cloud_model,omitempty" json:"cloud_model,omitempty"`
}

// ToolFollowupConfig is the configurable methodology for tool-step routing after a
// parent turn decision (see planning/routing_session_policy.md).
type ToolFollowupConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// InheritParentDefault starts from the parent turn's cloud|local decision.
	// Nil → true.
	InheritParentDefault *bool `yaml:"inherit_parent_default,omitempty" json:"inherit_parent_default,omitempty"`
	// Reevaluate allows local offload of safe tools even when parent was cloud.
	// Nil → true when Enabled.
	Reevaluate *bool `yaml:"reevaluate,omitempty" json:"reevaluate,omitempty"`
	// LocalToolAllowlist lists tool names (case-insensitive) or glob-ish prefixes
	// that may run local when reevaluate is on (e.g. read_file, grep, Glob).
	LocalToolAllowlist []string `yaml:"local_tool_allowlist,omitempty" json:"local_tool_allowlist,omitempty"`
	// CloudToolDenylist forces cloud/origin when any tool matches (Shell, Write, …).
	CloudToolDenylist []string `yaml:"cloud_tool_denylist,omitempty" json:"cloud_tool_denylist,omitempty"`
}

// InheritParentOrDefault is true unless explicitly set false.
func (c ToolFollowupConfig) InheritParentOrDefault() bool {
	if c.InheritParentDefault == nil {
		return true
	}
	return *c.InheritParentDefault
}

// ReevaluateOrDefault is true when Enabled and Reevaluate is nil or true.
func (c ToolFollowupConfig) ReevaluateOrDefault() bool {
	if !c.Enabled {
		return false
	}
	if c.Reevaluate == nil {
		return true
	}
	return *c.Reevaluate
}

// TaskClassifierConfig controls built-in task-shape routing (M1).
type TaskClassifierConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// ToolsForceCloud routes any request with tools[] to cloud/origin (default true).
	// Set false to allow local + tool passthrough to Ollama (models must support tools).
	ToolsForceCloud *bool `yaml:"tools_force_cloud,omitempty" json:"tools_force_cloud,omitempty"`
	// MustCloudPatterns / SmallLocalPatterns override built-in regex lists when non-empty.
	MustCloudPatterns  []string `yaml:"must_cloud_patterns,omitempty" json:"must_cloud_patterns,omitempty"`
	SmallLocalPatterns []string `yaml:"small_local_patterns,omitempty" json:"small_local_patterns,omitempty"`
	LocalModel         string   `yaml:"local_model,omitempty" json:"local_model,omitempty"`
	CloudBackend       string   `yaml:"cloud_backend,omitempty" json:"cloud_backend,omitempty"`
	CloudModel         string   `yaml:"cloud_model,omitempty" json:"cloud_model,omitempty"`
	ToolsPriority      int      `yaml:"tools_priority,omitempty" json:"tools_priority,omitempty"`
	MustCloudPriority  int      `yaml:"must_cloud_priority,omitempty" json:"must_cloud_priority,omitempty"`
	SmallLocalPriority int      `yaml:"small_local_priority,omitempty" json:"small_local_priority,omitempty"`
}

// ToolsForceCloudOrDefault is true unless explicitly set false.
func (c TaskClassifierConfig) ToolsForceCloudOrDefault() bool {
	if c.ToolsForceCloud == nil {
		return true
	}
	return *c.ToolsForceCloud
}

// AllowCloudFallbackOrDefault is true unless explicitly set false (pure-local).
func (c RoutingConfig) AllowCloudFallbackOrDefault() bool {
	if c.AllowCloudFallback == nil {
		return true
	}
	return *c.AllowCloudFallback
}

// ApplyDefaultTarget rewrites/injects the priority-0 always rule from routing.default.
func (c *RoutingConfig) ApplyDefaultTarget() {
	if c == nil {
		return
	}
	want := strings.ToLower(strings.TrimSpace(c.Default))
	if want != "local" && want != "cloud" {
		return
	}
	model := strings.TrimSpace(c.DefaultLocalModel)
	if model == "" {
		model = "qwen2.5-coder:14b"
	}
	for i := range c.Rules {
		r := &c.Rules[i]
		if !strings.EqualFold(strings.TrimSpace(r.Trigger.Type), "always") {
			continue
		}
		if r.Priority > 0 {
			continue
		}
		r.Action.Target = want
		if want == "local" {
			r.Action.Backend = "ollama"
			if r.Action.Model == "" || strings.EqualFold(r.Action.Model, "gpt-4o") {
				r.Action.Model = model
			}
			if r.Name == "" || r.Name == "Default Origin" {
				r.Name = "Default Local"
			}
		} else {
			if r.Action.Backend == "" {
				r.Action.Backend = "openai"
			}
			if r.Action.Model == "" {
				r.Action.Model = "gpt-4o"
			}
			if r.Name == "" || r.Name == "Default Local" {
				r.Name = "Default Origin"
			}
		}
		return
	}
	// No always rule — inject one.
	name := "Default Origin"
	action := ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"}
	if want == "local" {
		name = "Default Local"
		action = ActionConfig{Target: "local", Backend: "ollama", Model: model}
	}
	c.Rules = append(c.Rules, RuleConfig{
		Name:     name,
		Priority: 0,
		Trigger:  TriggerConfig{Type: "always"},
		Action:   action,
	})
}

type RuleConfig struct {
	Name     string `yaml:"name" json:"name"`
	Priority int    `yaml:"priority" json:"priority"`
	// Enabled defaults to true when omitted (nil). Set false to keep the rule in config but skip it.
	Enabled *bool         `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Trigger TriggerConfig `yaml:"trigger" json:"trigger"`
	Action  ActionConfig  `yaml:"action" json:"action"`
}

// IsEnabled reports whether the rule should participate in routing.
func (r RuleConfig) IsEnabled() bool {
	return r.Enabled == nil || *r.Enabled
}

type TriggerConfig struct {
	Type     string   `yaml:"type" json:"type"`
	Commands []string `yaml:"commands,omitempty" json:"commands,omitempty"`
	Pattern  string   `yaml:"pattern,omitempty" json:"pattern,omitempty"`
	File     string   `yaml:"file,omitempty" json:"file,omitempty"`
	Operator string   `yaml:"operator,omitempty" json:"operator,omitempty"`
	Value    int      `yaml:"value,omitempty" json:"value,omitempty"`
}

type ActionConfig struct {
	Target  string `yaml:"target" json:"target"`
	Backend string `yaml:"backend,omitempty" json:"backend,omitempty"`
	Model   string `yaml:"model,omitempty" json:"model,omitempty"`
	Adapter string `yaml:"adapter,omitempty" json:"adapter,omitempty"`
}

type CloudConfig struct {
	Providers []CloudProviderConfig `yaml:"providers" json:"providers"`
	RateLimit RateLimitConfig       `yaml:"rate_limit" json:"rate_limit"`
	BudgetCap float64               `yaml:"budget_cap_usd" json:"budget_cap_usd"`
}

type CloudProviderConfig struct {
	Name      string `yaml:"name" json:"name"`
	APIKeyEnv string `yaml:"api_key_env" json:"api_key_env"`
	BaseURL   string `yaml:"base_url" json:"base_url"`
}

type RateLimitConfig struct {
	RequestsPerMinute int `yaml:"requests_per_minute" json:"requests_per_minute"`
	TokensPerMinute   int `yaml:"tokens_per_minute" json:"tokens_per_minute"`
}

type BackendConfig struct {
	Name                string `yaml:"name" json:"name"`
	Type                string `yaml:"type" json:"type"`
	URL                 string `yaml:"url" json:"url"`
	HealthCheckInterval string `yaml:"health_check_interval" json:"health_check_interval"`
}

type DashboardConfig struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	Auth    bool `yaml:"auth" json:"auth"`
}

type TransformConfig struct {
	Enabled        bool   `yaml:"enabled" json:"enabled"`
	TrimContext    bool   `yaml:"trim_context" json:"trim_context"`
	AugmentPrepend string `yaml:"augment_prepend" json:"augment_prepend"`
	AugmentAppend  string `yaml:"augment_append" json:"augment_append"`
	// LocalContext controls how messages are bounded when fulfilling on a local
	// backend (Path A gateway / legacy StreamChat). Path B Bidi extract already
	// sends a single TipTap latest-turn user message — this is a no-op there.
	//   "" / "full"     — leave messages unchanged (legacy)
	//   "latest_turn"   — leading system (bounded) + latest user turn (+ tool loop)
	// Applied in PipelineCompleter only after a local routing decision so cloud
	// / sticky origin paths keep the full client body. See planning docs.
	LocalContext string `yaml:"local_context,omitempty" json:"local_context,omitempty"`
	// LocalSystemMaxChars caps leading system content under latest_turn (default 4000).
	LocalSystemMaxChars int `yaml:"local_system_max_chars,omitempty" json:"local_system_max_chars,omitempty"`
	// LocalEpisodeCount injects up to N prior compressed episodes as a system
	// preamble for local fulfills (0 → disabled; default 3 when unset in yaml use 0
	// unless set). Keeps locals off TipTap mega-dumps.
	LocalEpisodeCount int `yaml:"local_episode_count,omitempty" json:"local_episode_count,omitempty"`
	// LocalEpisodeMaxChars caps the episode preamble (default 1500).
	LocalEpisodeMaxChars int `yaml:"local_episode_max_chars,omitempty" json:"local_episode_max_chars,omitempty"`
}

func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			ProxyPort:     8080,
			DashboardPort: 8081,
			LogLevel:      "info",
		},
		Thresholds: ThresholdConfig{
			MaxLocalContextTokens: 8000,
			IdleUnloadTimeout:     "5m",
			RequestTimeout:        "10m",
		},
		VRAM: VRAMConfig{
			Strategy:        "dynamic",
			HeadroomMB:      512,
			MaxLoadedModels: 3,
			GPUAssignments:  map[string]int{},
		},
		Models: []ModelConfig{
			{
				Name:           "qwen2.5-coder:14b",
				Backend:        "ollama",
				VRAMEstimateMB: 9000,
				MaxContext:     32768,
				Capabilities:   []string{"code"},
				KeepWarm:       true,
			},
		},
		Backends: []BackendConfig{
			{Name: "ollama", Type: "local", URL: "http://127.0.0.1:11434", HealthCheckInterval: "30s"},
		},
		Cloud: CloudConfig{
			Providers: []CloudProviderConfig{
				{Name: "openai", APIKeyEnv: "OPENAI_API_KEY", BaseURL: "https://api.openai.com/v1"},
			},
			RateLimit: RateLimitConfig{RequestsPerMinute: 30, TokensPerMinute: 100000},
			BudgetCap: 50,
		},
		Dashboard: DashboardConfig{Enabled: true},
		Transform: TransformConfig{
			LocalContext:        "latest_turn",
			LocalSystemMaxChars: 4000,
		},
		MITM: MITMConfig{
			Enabled:               false,
			Port:                  8082,
			CACert:                "",
			CAKey:                 "",
			Hosts:                 []string{"api2.cursor.sh", "api3.cursor.sh", "api4.cursor.sh", "*.api5.cursor.sh"},
			PassthroughDefault:    true,
			DebugAgentRPC:         false,
			AgentRPCFulfill:       false,
			AgentRPCCannedOnError: false,
		},
		ModelAliases: map[string]string{},
		Routing: RoutingConfig{
			TaskClassifier: TaskClassifierConfig{
				Enabled:    true,
				LocalModel: "qwen2.5-coder:14b",
			},
			Rules: []RuleConfig{
				{
					Name:     "Explicit Local",
					Priority: 100,
					Trigger:  TriggerConfig{Type: "explicit", Commands: []string{"/local", "/fast"}},
					Action:   ActionConfig{Target: "local", Model: "qwen2.5-coder:14b"},
				},
				{
					Name:     "Explicit Cloud",
					Priority: 99,
					Trigger:  TriggerConfig{Type: "explicit", Commands: []string{"/cloud", "/heavy"}},
					Action:   ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
				},
				{
					Name:     "Context Overflow",
					Priority: 10,
					Trigger:  TriggerConfig{Type: "context_size", Operator: ">", Value: 8000},
					Action:   ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
				},
				{
					Name:     "Default Origin",
					Priority: 0,
					Trigger:  TriggerConfig{Type: "always"},
					Action:   ActionConfig{Target: "cloud", Backend: "openai", Model: "gpt-4o"},
				},
			},
		},
	}
}
