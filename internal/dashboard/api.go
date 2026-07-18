package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/config"
	"gopkg.in/yaml.v3"
)

type ConfigStore interface {
	Get() *config.Config
	Update(cfg *config.Config) error
}

type ModelController interface {
	ListModels() []*backend.ModelInfo
	LoadModel(name string) error
	UnloadModel(name string) error
}

type FileConfigStore struct {
	Provider *config.Provider
	Path     string
}

func (s *FileConfigStore) Get() *config.Config { return s.Provider.Get() }

// Update validates via the same ParseConfig path as file load/hot-reload,
// persists YAML to Path, then Swap() so Watch subscribers rebuild router/aliases.
func (s *FileConfigStore) Update(cfg *config.Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	parsed, err := config.ParseConfig(data)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.Path, data, 0o644); err != nil {
		return err
	}
	s.Provider.Swap(parsed)
	return nil
}

type RegistryModelController struct {
	Registry *backend.Registry
	OnLoad   func(name string) error
	OnUnload func(name string) error
}

func (c *RegistryModelController) ListModels() []*backend.ModelInfo {
	return c.Registry.ListModels()
}

func (c *RegistryModelController) LoadModel(name string) error {
	if c.OnLoad != nil {
		if err := c.OnLoad(name); err != nil {
			return err
		}
	}
	return c.Registry.SetModelState(name, backend.ModelStateWarm)
}

func (c *RegistryModelController) UnloadModel(name string) error {
	if c.OnUnload != nil {
		if err := c.OnUnload(name); err != nil {
			return err
		}
	}
	return c.Registry.SetModelState(name, backend.ModelStateCold)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		http.Error(w, "no config", http.StatusInternalServerError)
		return
	}
	cfg := s.Config.Get()
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "yaml" || format == "yml" || strings.Contains(r.Header.Get("Accept"), "yaml") {
		data, err := yaml.Marshal(cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(data)
		return
	}
	writeJSON(w, cfg)
}

// handlePutConfig replaces the full running config.
// Accepts application/json (full Config object) or YAML (application/yaml, text/yaml, application/x-yaml).
// Validation uses config.ParseConfig — the same path as startup and file hot-reload.
// When backends are reachable, unknown local model IDs are rejected (soft catalog warnings still returned via GET /api/validate).
func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		http.Error(w, "no config", http.StatusInternalServerError)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	cfg, err := parseConfigBody(r.Header.Get("Content-Type"), body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, catalog, _ := s.discover(ctx)
	gpus := collectGPUStatus(s.GPUs)
	gpuCount := 0
	for _, g := range gpus {
		if g.Error == "" {
			gpuCount++
		}
	}
	// Soft catalog: warn in validate endpoint; hard-fail only structural + bad GPU indices.
	res := config.ValidateDetailed(cfg, config.ValidateOptions{
		Catalog:  catalog,
		GPUCount: gpuCount,
		Soft:     true,
	})
	if err := res.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.Config.Update(cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(res.Warnings) > 0 {
		w.Header().Set("X-Glider-Warnings", strings.Join(res.Warnings, " | "))
	}
	writeJSON(w, s.Config.Get())
}

func parseConfigBody(contentType string, body []byte) (*config.Config, error) {
	ct := strings.ToLower(contentType)
	switch {
	case strings.Contains(ct, "yaml"), strings.Contains(ct, "yml"):
		return config.ParseConfig(body)
	default:
		var raw any
		if err := json.Unmarshal(body, &raw); err != nil {
			if cfg, yerr := config.ParseConfig(body); yerr == nil {
				return cfg, nil
			}
			return nil, err
		}
		yml, err := yaml.Marshal(raw)
		if err != nil {
			return nil, err
		}
		return config.ParseConfig(yml)
	}
}

func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		http.Error(w, "no config", http.StatusInternalServerError)
		return
	}
	cfg := s.Config.Get()
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer r.Body.Close()
		parsed, err := parseConfigBody(r.Header.Get("Content-Type"), body)
		if err != nil {
			writeJSON(w, config.ValidationResult{Errors: []string{err.Error()}})
			return
		}
		cfg = parsed
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	_, catalog, backendErrs := s.discover(ctx)
	gpus := collectGPUStatus(s.GPUs)
	gpuCount := 0
	for _, g := range gpus {
		if g.Error == "" {
			gpuCount++
		}
	}
	res := config.ValidateDetailed(cfg, config.ValidateOptions{
		Catalog:  catalog,
		GPUCount: gpuCount,
		Soft:     true,
	})
	res.Warnings = append(res.Warnings, backendErrs...)
	writeJSON(w, res)
}

func (s *Server) handleGetModels(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	models, _, errs := s.discover(ctx)
	if len(models) == 0 && s.Models != nil {
		// Fallback: registry-only list for older clients.
		writeJSON(w, s.Models.ListModels())
		return
	}
	type wrap struct {
		Models []DiscoveredModel `json:"models"`
		Errors []string          `json:"errors,omitempty"`
	}
	// Keep backward compatible: if Accept wants bare array historically tests decode []ModelInfo.
	// Tests expect []backend.ModelInfo from registry. Prefer discovered when rich=1 or always return discovered as primary.
	if r.URL.Query().Get("rich") == "1" || r.URL.Query().Get("format") == "rich" {
		writeJSON(w, wrap{Models: models, Errors: errs})
		return
	}
	if s.Models != nil && len(models) == 0 {
		writeJSON(w, s.Models.ListModels())
		return
	}
	// Default: discovered list (array) — includes config + backend tags.
	writeJSON(w, models)
}

func (s *Server) handleGetVRAM(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	models, catalog, errs := s.discover(ctx)
	cfg := &config.Config{}
	if s.Config != nil {
		cfg = s.Config.Get()
	}
	snap := VRAMSnapshot{
		GPUs:            collectGPUStatus(s.GPUs),
		Models:          models,
		GPUAssignments:  map[string]int{},
		BackendErrors:   errs,
		Catalog:         catalog.Names(),
	}
	if cfg != nil {
		snap.Strategy = cfg.VRAM.Strategy
		snap.HeadroomMB = cfg.VRAM.HeadroomMB
		snap.MaxLoadedModels = cfg.VRAM.MaxLoadedModels
		if cfg.VRAM.GPUAssignments != nil {
			snap.GPUAssignments = cfg.VRAM.GPUAssignments
		}
	}
	writeJSON(w, snap)
}

// handlePatchGPUAssignments updates vram.gpu_assignments and persists config.
func (s *Server) handlePatchGPUAssignments(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		http.Error(w, "no config", http.StatusInternalServerError)
		return
	}
	var body struct {
		Assignments map[string]int `json:"assignments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	live := s.Config.Get()
	cfg := *live
	cfg.VRAM.GPUAssignments = map[string]int{}
	for k, v := range live.VRAM.GPUAssignments {
		cfg.VRAM.GPUAssignments[k] = v
	}
	for k, v := range body.Assignments {
		if v < 0 {
			delete(cfg.VRAM.GPUAssignments, k)
			continue
		}
		cfg.VRAM.GPUAssignments[k] = v
	}
	gpus := collectGPUStatus(s.GPUs)
	gpuCount := 0
	for _, g := range gpus {
		if g.Error == "" {
			gpuCount++
		}
	}
	res := config.ValidateDetailed(&cfg, config.ValidateOptions{GPUCount: gpuCount, Soft: true})
	if err := res.Err(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Config.Update(&cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, s.Config.Get().VRAM.GPUAssignments)
}

func (s *Server) handleModelAction(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/models/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 || s.Models == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := parts[0]
	action := parts[1]
	var err error
	switch action {
	case "load":
		err = s.Models.LoadModel(name)
	case "unload":
		err = s.Models.UnloadModel(name)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		writeJSON(w, []any{})
		return
	}
	sessions, err := s.History.ListSessions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, sessions)
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	if s.History == nil {
		http.Error(w, "history unavailable", http.StatusNotFound)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/sessions/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "missing session id", http.StatusBadRequest)
		return
	}
	id := parts[0]
	if len(parts) >= 2 && parts[1] == "requests" {
		limit := 200
		if q := r.URL.Query().Get("limit"); q != "" {
			if n, err := strconv.Atoi(q); err == nil && n > 0 {
				limit = n
			}
		}
		reqs, err := s.History.ListRequests(id, limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, reqs)
		return
	}
	agg, err := s.History.Aggregates(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, agg)
}

func (s *Server) discover(ctx context.Context) ([]DiscoveredModel, config.ModelCatalog, []string) {
	var cfg *config.Config
	if s.Config != nil {
		cfg = s.Config.Get()
	}
	var reg *backend.Registry
	if rc, ok := s.Models.(*RegistryModelController); ok && rc != nil {
		reg = rc.Registry
	}
	return DiscoverModels(ctx, cfg, reg)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}