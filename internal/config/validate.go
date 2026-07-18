package config

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ModelCatalog is a set of known model IDs from backends + config (for optional validation).
type ModelCatalog map[string]struct{}

// NewModelCatalog builds a catalog from model name strings.
func NewModelCatalog(names ...string) ModelCatalog {
	c := make(ModelCatalog, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" {
			c[n] = struct{}{}
		}
	}
	return c
}

func (c ModelCatalog) Has(name string) bool {
	if c == nil {
		return false
	}
	_, ok := c[name]
	return ok
}

func (c ModelCatalog) Names() []string {
	out := make([]string, 0, len(c))
	for n := range c {
		out = append(out, n)
	}
	return out
}

// ValidateOptions controls optional catalog-aware checks.
type ValidateOptions struct {
	// Catalog, when non-nil and non-empty, checks that local model references exist.
	Catalog ModelCatalog
	// GPUCount, when > 0, checks gpu_assignments indices are in range [0, GPUCount).
	GPUCount int
	// Soft when true collects model-missing issues as warnings rather than hard errors.
	// Structural issues remain hard errors regardless.
	Soft bool
}

// ValidationResult holds hard errors and soft warnings.
type ValidationResult struct {
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

func (r ValidationResult) Err() error {
	if len(r.Errors) == 0 {
		return nil
	}
	return fmt.Errorf("validation error: %s", strings.Join(r.Errors, "; "))
}

func (r ValidationResult) HasIssues() bool {
	return len(r.Errors) > 0 || len(r.Warnings) > 0
}

// Validate performs structural validation (used by ParseConfig).
func Validate(cfg *Config) error {
	res := ValidateDetailed(cfg, ValidateOptions{})
	return res.Err()
}

// ValidateWithCatalog validates structure plus optional discovered-model / GPU checks.
func ValidateWithCatalog(cfg *Config, catalog ModelCatalog, gpuCount int) error {
	res := ValidateDetailed(cfg, ValidateOptions{Catalog: catalog, GPUCount: gpuCount})
	return res.Err()
}

// ValidateDetailed returns structured errors and warnings.
func ValidateDetailed(cfg *Config, opts ValidateOptions) ValidationResult {
	var res ValidationResult
	if cfg == nil {
		res.Errors = append(res.Errors, "config is nil")
		return res
	}

	if cfg.Server.ProxyPort == 0 {
		res.Errors = append(res.Errors, "missing required field server.proxy_port")
	}
	if cfg.Server.ProxyPort < 0 || cfg.Server.ProxyPort > 65535 {
		res.Errors = append(res.Errors, "server.proxy_port must be between 1 and 65535")
	}
	if cfg.Server.DashboardPort < 0 || cfg.Server.DashboardPort > 65535 {
		res.Errors = append(res.Errors, "server.dashboard_port must be between 0 and 65535")
	}
	if lvl := strings.ToLower(strings.TrimSpace(cfg.Server.LogLevel)); lvl != "" {
		switch lvl {
		case "debug", "info", "warn", "warning", "error":
		default:
			res.Errors = append(res.Errors, fmt.Sprintf("server.log_level %q is invalid (use debug|info|warn|error)", cfg.Server.LogLevel))
		}
	}

	if cfg.Thresholds.MaxLocalContextTokens < 0 {
		res.Errors = append(res.Errors, "thresholds.max_local_context_tokens must be >= 0")
	}
	if cfg.Thresholds.IdleUnloadTimeout != "" {
		if _, err := time.ParseDuration(cfg.Thresholds.IdleUnloadTimeout); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("thresholds.idle_unload_timeout invalid: %v", err))
		}
	}
	if cfg.Thresholds.RequestTimeout != "" {
		if _, err := time.ParseDuration(cfg.Thresholds.RequestTimeout); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("thresholds.request_timeout invalid: %v", err))
		}
	}

	switch strings.ToLower(cfg.VRAM.Strategy) {
	case "", "dynamic", "hybrid", "static":
	default:
		res.Errors = append(res.Errors, fmt.Sprintf("vram.strategy %q is invalid (use dynamic|hybrid|static)", cfg.VRAM.Strategy))
	}
	if cfg.VRAM.HeadroomMB < 0 {
		res.Errors = append(res.Errors, "vram.headroom_mb must be >= 0")
	}
	if cfg.VRAM.MaxLoadedModels < 0 {
		res.Errors = append(res.Errors, "vram.max_loaded_models must be >= 0")
	}

	backendNames := make(map[string]struct{}, len(cfg.Backends))
	for i, b := range cfg.Backends {
		if strings.TrimSpace(b.Name) == "" {
			res.Errors = append(res.Errors, fmt.Sprintf("backends[%d].name is required", i))
		} else {
			backendNames[b.Name] = struct{}{}
		}
		if strings.TrimSpace(b.URL) == "" {
			res.Errors = append(res.Errors, fmt.Sprintf("backends[%d].url is required", i))
		}
		if b.HealthCheckInterval != "" {
			if _, err := time.ParseDuration(b.HealthCheckInterval); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("backends[%d].health_check_interval invalid: %v", i, err))
			}
		}
	}

	modelNames := make(map[string]struct{}, len(cfg.Models))
	for i, m := range cfg.Models {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			res.Errors = append(res.Errors, fmt.Sprintf("models[%d].name is required", i))
			continue
		}
		if _, dup := modelNames[name]; dup {
			res.Errors = append(res.Errors, fmt.Sprintf("models: duplicate name %q", name))
		}
		modelNames[name] = struct{}{}
		if m.VRAMEstimateMB < 0 {
			res.Errors = append(res.Errors, fmt.Sprintf("models[%d].vram_estimate_mb must be >= 0", i))
		}
		if m.MaxContext < 0 {
			res.Errors = append(res.Errors, fmt.Sprintf("models[%d].max_context must be >= 0", i))
		}
		if m.Backend != "" {
			if _, ok := backendNames[m.Backend]; !ok && len(backendNames) > 0 {
				res.Warnings = append(res.Warnings, fmt.Sprintf("models[%d] (%s): backend %q not listed in backends", i, name, m.Backend))
			}
		}
		if opts.Catalog != nil && len(opts.Catalog) > 0 && m.Backend != "openai" && m.Backend != "anthropic" {
			if !opts.Catalog.Has(name) {
				msg := fmt.Sprintf("models[%d]: %q not found in discovered backend models", i, name)
				if opts.Soft {
					res.Warnings = append(res.Warnings, msg)
				} else {
					res.Errors = append(res.Errors, msg)
				}
			}
		}
	}

	for alias, target := range cfg.ModelAliases {
		if strings.TrimSpace(alias) == "" {
			res.Errors = append(res.Errors, "model_aliases: empty alias key")
			continue
		}
		if strings.TrimSpace(target) == "" {
			res.Errors = append(res.Errors, fmt.Sprintf("model_aliases[%q]: empty target", alias))
			continue
		}
		if len(modelNames) > 0 {
			if _, ok := modelNames[target]; !ok {
				msg := fmt.Sprintf("model_aliases[%q]: target %q is not in models", alias, target)
				res.Warnings = append(res.Warnings, msg)
			}
		}
	}

	for model, gpu := range cfg.VRAM.GPUAssignments {
		if strings.TrimSpace(model) == "" {
			res.Errors = append(res.Errors, "vram.gpu_assignments: empty model key")
			continue
		}
		if gpu < 0 {
			res.Errors = append(res.Errors, fmt.Sprintf("vram.gpu_assignments[%q]: GPU index %d must be >= 0", model, gpu))
		}
		if opts.GPUCount > 0 && gpu >= opts.GPUCount {
			res.Errors = append(res.Errors, fmt.Sprintf("vram.gpu_assignments[%q]: GPU index %d out of range (have %d GPUs)", model, gpu, opts.GPUCount))
		}
		if len(modelNames) > 0 {
			if _, ok := modelNames[model]; !ok {
				res.Warnings = append(res.Warnings, fmt.Sprintf("vram.gpu_assignments: model %q is not in models list", model))
			}
		}
	}

	if cfg.MITM.Port < 0 || cfg.MITM.Port > 65535 {
		res.Errors = append(res.Errors, "mitm.port must be between 0 and 65535")
	}

	if cfg.Routing.TaskClassifier.Enabled {
		for i, p := range cfg.Routing.TaskClassifier.MustCloudPatterns {
			if _, err := regexp.Compile(p); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("routing.task_classifier.must_cloud_patterns[%d]: %v", i, err))
			}
		}
		for i, p := range cfg.Routing.TaskClassifier.SmallLocalPatterns {
			if _, err := regexp.Compile(p); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("routing.task_classifier.small_local_patterns[%d]: %v", i, err))
			}
		}
	}
	if from := strings.ToLower(strings.TrimSpace(cfg.Routing.ComplexityFrom)); from != "" {
		switch from {
		case "cursor", "heuristic", "both":
		default:
			res.Errors = append(res.Errors, fmt.Sprintf("routing.complexity_from %q is unknown (expected cursor|heuristic|both)", cfg.Routing.ComplexityFrom))
		}
	}
	if def := strings.ToLower(strings.TrimSpace(cfg.Routing.Default)); def != "" {
		switch def {
		case "local", "cloud":
		default:
			res.Errors = append(res.Errors, fmt.Sprintf("routing.default %q is unknown (expected local|cloud)", cfg.Routing.Default))
		}
	}
	if cfg.Routing.Complexity.Enabled {
		if cfg.Routing.Complexity.CloudAbove < 0 || cfg.Routing.Complexity.CloudAbove > 100 {
			res.Errors = append(res.Errors, "routing.complexity.cloud_above must be between 0 and 100")
		}
	}
	if lc := strings.ToLower(strings.TrimSpace(cfg.Transform.LocalContext)); lc != "" {
		switch lc {
		case "full", "latest_turn":
		default:
			res.Errors = append(res.Errors, fmt.Sprintf("transform.local_context %q is unknown (expected full|latest_turn)", cfg.Transform.LocalContext))
		}
	}
	if ttl := strings.TrimSpace(cfg.Routing.TurnFamilyTTL); ttl != "" {
		if _, err := time.ParseDuration(ttl); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("routing.turn_family_ttl invalid: %v", err))
		}
	}

	for i, r := range cfg.Routing.Rules {
		if strings.TrimSpace(r.Name) == "" {
			res.Errors = append(res.Errors, fmt.Sprintf("routing.rules[%d].name is required", i))
		}
		tt := strings.ToLower(strings.TrimSpace(r.Trigger.Type))
		switch tt {
		case "explicit", "regex", "context_size", "always", "script",
			"composer_wrapup", "wrapup_origin", "composer_wrapup_origin":
		case "":
			res.Errors = append(res.Errors, fmt.Sprintf("routing.rules[%d].trigger.type is required", i))
		default:
			res.Errors = append(res.Errors, fmt.Sprintf("routing.rules[%d].trigger.type %q is unknown", i, r.Trigger.Type))
		}
		switch tt {
		case "explicit":
			if len(r.Trigger.Commands) == 0 {
				res.Errors = append(res.Errors, fmt.Sprintf("routing.rules[%d] (%s): explicit trigger requires commands", i, r.Name))
			}
		case "regex":
			if strings.TrimSpace(r.Trigger.Pattern) == "" {
				res.Errors = append(res.Errors, fmt.Sprintf("routing.rules[%d] (%s): regex trigger requires pattern", i, r.Name))
			}
		case "script":
			if strings.TrimSpace(r.Trigger.File) == "" {
				res.Errors = append(res.Errors, fmt.Sprintf("routing.rules[%d] (%s): script trigger requires file", i, r.Name))
			}
		case "context_size":
			if r.Trigger.Operator == "" {
				res.Errors = append(res.Errors, fmt.Sprintf("routing.rules[%d] (%s): context_size trigger requires operator", i, r.Name))
			}
		}
		target := strings.ToLower(strings.TrimSpace(r.Action.Target))
		if target == "" {
			res.Errors = append(res.Errors, fmt.Sprintf("routing.rules[%d] (%s): action.target is required", i, r.Name))
		} else if target != "local" && target != "cloud" {
			res.Warnings = append(res.Warnings, fmt.Sprintf("routing.rules[%d] (%s): action.target %q is unusual (expected local|cloud)", i, r.Name, r.Action.Target))
		}
		if target == "local" && r.Action.Model != "" && len(modelNames) > 0 {
			if _, ok := modelNames[r.Action.Model]; !ok {
				res.Warnings = append(res.Warnings, fmt.Sprintf("routing.rules[%d] (%s): local action model %q is not in models", i, r.Name, r.Action.Model))
			}
		}
	}

	if dr := strings.ToLower(strings.TrimSpace(cfg.Orchestration.Loops.DefaultRoute)); dr != "" {
		switch dr {
		case "local", "cloud", "auto":
		default:
			res.Errors = append(res.Errors, fmt.Sprintf("orchestration.loops.default_route %q is invalid (use local|cloud|auto)", cfg.Orchestration.Loops.DefaultRoute))
		}
	}
	hl := cfg.Orchestration.Loops.HoopLearning
	if hl.LocalBiasStep < 0 {
		res.Errors = append(res.Errors, "orchestration.loops.hoop_learning.local_bias_step must be >= 0")
	}
	if hl.MaxBias < 0 {
		res.Errors = append(res.Errors, "orchestration.loops.hoop_learning.max_bias must be >= 0")
	}
	if hl.Window < 0 {
		res.Errors = append(res.Errors, "orchestration.loops.hoop_learning.window must be >= 0")
	}

	return res
}
