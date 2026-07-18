package config

type Config struct {
	Server         ServerConfig         `yaml:"server" json:"server"`
	Thresholds     ThresholdConfig      `yaml:"thresholds" json:"thresholds"`
	VRAM           VRAMConfig           `yaml:"vram" json:"vram"`
	Models         []ModelConfig        `yaml:"models" json:"models"`
	ModelAliases   map[string]string    `yaml:"model_aliases" json:"model_aliases"`
	Routing        RoutingConfig        `yaml:"routing" json:"routing"`
	Orchestration  OrchestrationConfig  `yaml:"orchestration,omitempty" json:"orchestration,omitempty"`
	Cloud          CloudConfig          `yaml:"cloud" json:"cloud"`
	Backends       []BackendConfig      `yaml:"backends" json:"backends"`
	Dashboard      DashboardConfig      `yaml:"dashboard" json:"dashboard"`
	Transform      TransformConfig      `yaml:"transform" json:"transform"`
	MITM           MITMConfig           `yaml:"mitm" json:"mitm"`
}

// OrchestrationConfig holds swarm/fan-out feature flags (foundation; default off).
type OrchestrationConfig struct {
	FanOut      FanOutConfig      `yaml:"fan_out,omitempty" json:"fan_out,omitempty"`
	Concurrency ConcurrencyConfig `yaml:"concurrency,omitempty" json:"concurrency,omitempty"`
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
}

type ServerConfig struct {
	ProxyPort     int    `yaml:"proxy_port" json:"proxy_port"`
	DashboardPort int    `yaml:"dashboard_port" json:"dashboard_port"`
	LogLevel      string `yaml:"log_level" json:"log_level"`
}

type ThresholdConfig struct {
	MaxLocalContextTokens int    `yaml:"max_local_context_tokens" json:"max_local_context_tokens"`
	IdleUnloadTimeout     string `yaml:"idle_unload_timeout" json:"idle_unload_timeout"`
	RequestTimeout        string `yaml:"request_timeout" json:"request_timeout"`
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
	// TaskClassifier injects heuristic rules (tools → cloud, must-cloud keywords,
	// small-local keywords) when Enabled. Priorities default below explicit /cloud
	// (99) and above context_size (10). See planning/smart_routing_and_local_tools.md.
	TaskClassifier TaskClassifierConfig `yaml:"task_classifier,omitempty" json:"task_classifier,omitempty"`
	// TurnFamilyTTL is how long a DecideLocal / explicit turn family stays open for
	// reply-summary / title follow-ons (e.g. "90s"). Empty → 90s. Not conversation-wide.
	// See planning/routing_session_policy.md.
	TurnFamilyTTL string `yaml:"turn_family_ttl,omitempty" json:"turn_family_ttl,omitempty"`
	// ToolFollowup controls per-tool-step re-decide for child RunSSE / tool loops
	// after a parent cloud|local decision. Path B logs would_local and fulfills
	// when HasRunSSEToolCodec; otherwise origin. Path A routes allowlisted tools locally.
	ToolFollowup ToolFollowupConfig `yaml:"tool_followup,omitempty" json:"tool_followup,omitempty"`
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
			RequestTimeout:        "120s",
		},
		VRAM: VRAMConfig{
			Strategy:        "dynamic",
			HeadroomMB:      512,
			MaxLoadedModels: 3,
			GPUAssignments:  map[string]int{},
		},
		Models: []ModelConfig{
			{
				Name:           "codellama:7b",
				Backend:        "ollama",
				VRAMEstimateMB: 4200,
				MaxContext:     16384,
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
				LocalModel: "codellama:7b",
			},
			Rules: []RuleConfig{
				{
					Name:     "Explicit Local",
					Priority: 100,
					Trigger:  TriggerConfig{Type: "explicit", Commands: []string{"/local", "/fast"}},
					Action:   ActionConfig{Target: "local", Model: "codellama:7b"},
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
