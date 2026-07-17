package config

type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Thresholds ThresholdConfig  `yaml:"thresholds"`
	VRAM       VRAMConfig       `yaml:"vram"`
	Models     []ModelConfig    `yaml:"models"`
	Routing    RoutingConfig    `yaml:"routing"`
	Cloud      CloudConfig      `yaml:"cloud"`
	Backends   []BackendConfig  `yaml:"backends"`
	Dashboard  DashboardConfig  `yaml:"dashboard"`
	Transform  TransformConfig  `yaml:"transform"`
}

type ServerConfig struct {
	ProxyPort     int    `yaml:"proxy_port"`
	DashboardPort int    `yaml:"dashboard_port"`
	LogLevel      string `yaml:"log_level"`
}

type ThresholdConfig struct {
	MaxLocalContextTokens int    `yaml:"max_local_context_tokens"`
	IdleUnloadTimeout     string `yaml:"idle_unload_timeout"`
	RequestTimeout        string `yaml:"request_timeout"`
}

type VRAMConfig struct {
	Strategy        string         `yaml:"strategy"`
	HeadroomMB      int            `yaml:"headroom_mb"`
	MaxLoadedModels int            `yaml:"max_loaded_models"`
	GPUAssignments  map[string]int `yaml:"gpu_assignments"`
}

type ModelConfig struct {
	Name           string   `yaml:"name"`
	Backend        string   `yaml:"backend"`
	VRAMEstimateMB int      `yaml:"vram_estimate_mb"`
	MaxContext     int      `yaml:"max_context"`
	Capabilities   []string `yaml:"capabilities"`
	Adapter        string   `yaml:"adapter,omitempty"`
	KeepWarm       bool     `yaml:"keep_warm"`
	Adapters       []struct {
		Name string `yaml:"name"`
		Path string `yaml:"path"`
	} `yaml:"adapters,omitempty"`
}

type RoutingConfig struct {
	Rules []RuleConfig `yaml:"rules"`
}

type RuleConfig struct {
	Name     string         `yaml:"name"`
	Priority int            `yaml:"priority"`
	Trigger  TriggerConfig  `yaml:"trigger"`
	Action   ActionConfig   `yaml:"action"`
}

type TriggerConfig struct {
	Type     string   `yaml:"type"`
	Commands []string `yaml:"commands,omitempty"`
	Pattern  string   `yaml:"pattern,omitempty"`
	File     string   `yaml:"file,omitempty"`
	Operator string   `yaml:"operator,omitempty"`
	Value    int      `yaml:"value,omitempty"`
}

type ActionConfig struct {
	Target  string `yaml:"target"`
	Backend string `yaml:"backend,omitempty"`
	Model   string `yaml:"model,omitempty"`
	Adapter string `yaml:"adapter,omitempty"`
}

type CloudConfig struct {
	Providers []CloudProviderConfig `yaml:"providers"`
	RateLimit RateLimitConfig       `yaml:"rate_limit"`
	BudgetCap float64               `yaml:"budget_cap_usd"`
}

type CloudProviderConfig struct {
	Name      string `yaml:"name"`
	APIKeyEnv string `yaml:"api_key_env"`
	BaseURL   string `yaml:"base_url"`
}

type RateLimitConfig struct {
	RequestsPerMinute int `yaml:"requests_per_minute"`
	TokensPerMinute   int `yaml:"tokens_per_minute"`
}

type BackendConfig struct {
	Name                string `yaml:"name"`
	Type                string `yaml:"type"`
	URL                 string `yaml:"url"`
	HealthCheckInterval string `yaml:"health_check_interval"`
}

type DashboardConfig struct {
	Enabled bool `yaml:"enabled"`
	Auth    bool `yaml:"auth"`
}

type TransformConfig struct {
	Enabled   bool   `yaml:"enabled"`
	TrimContext bool `yaml:"trim_context"`
	AugmentPrepend string `yaml:"augment_prepend"`
	AugmentAppend  string `yaml:"augment_append"`
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
		Routing: RoutingConfig{
			Rules: []RuleConfig{
				{
					Name:     "Default Local",
					Priority: 0,
					Trigger:  TriggerConfig{Type: "always"},
					Action:   ActionConfig{Target: "local", Model: "codellama:7b"},
				},
			},
		},
	}
}
