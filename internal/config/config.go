package config

type Config struct {
	Server       ServerConfig      `yaml:"server" json:"server"`
	Thresholds   ThresholdConfig   `yaml:"thresholds" json:"thresholds"`
	VRAM         VRAMConfig        `yaml:"vram" json:"vram"`
	Models       []ModelConfig     `yaml:"models" json:"models"`
	ModelAliases map[string]string `yaml:"model_aliases" json:"model_aliases"`
	Routing      RoutingConfig     `yaml:"routing" json:"routing"`
	Cloud        CloudConfig       `yaml:"cloud" json:"cloud"`
	Backends     []BackendConfig   `yaml:"backends" json:"backends"`
	Dashboard    DashboardConfig   `yaml:"dashboard" json:"dashboard"`
	Transform    TransformConfig   `yaml:"transform" json:"transform"`
	MITM         MITMConfig        `yaml:"mitm" json:"mitm"`
}

type MITMConfig struct {
	Enabled            bool     `yaml:"enabled" json:"enabled"`
	Port               int      `yaml:"port" json:"port"`
	CACert             string   `yaml:"ca_cert" json:"ca_cert"`
	CAKey              string   `yaml:"ca_key" json:"ca_key"`
	Hosts              []string `yaml:"hosts" json:"hosts"`
	PassthroughDefault bool     `yaml:"passthrough_default" json:"passthrough_default"`
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
}

type RuleConfig struct {
	Name     string        `yaml:"name" json:"name"`
	Priority int           `yaml:"priority" json:"priority"`
	// Enabled defaults to true when omitted (nil). Set false to keep the rule in config but skip it.
	Enabled  *bool         `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Trigger  TriggerConfig `yaml:"trigger" json:"trigger"`
	Action   ActionConfig  `yaml:"action" json:"action"`
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
			{Name: "ollama", Type: "local", URL: "http://localhost:11434", HealthCheckInterval: "30s"},
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
			Enabled:            false,
			Port:               8082,
			CACert:             "",
			CAKey:              "",
			Hosts:              []string{"api2.cursor.sh", "api3.cursor.sh", "api4.cursor.sh", "*.api5.cursor.sh"},
			PassthroughDefault: true,
		},
		ModelAliases: map[string]string{},
		Routing: RoutingConfig{
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
