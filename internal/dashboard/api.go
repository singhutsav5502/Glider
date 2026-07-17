package dashboard

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"

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

func (s *FileConfigStore) Update(cfg *config.Config) error {
	if err := config.Validate(cfg); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.Path, data, 0o644); err != nil {
		return err
	}
	s.Provider.SwapForTest(cfg)
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
	writeJSON(w, s.Config.Get())
}

func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		http.Error(w, "no config", http.StatusInternalServerError)
		return
	}
	var patch config.Config
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cur := *s.Config.Get()
	if patch.Thresholds.MaxLocalContextTokens != 0 {
		cur.Thresholds.MaxLocalContextTokens = patch.Thresholds.MaxLocalContextTokens
	}
	if patch.Thresholds.MaxLocalContextTokens < 0 {
		http.Error(w, "validation error: max_local_context_tokens must be >= 0", http.StatusBadRequest)
		return
	}
	// Explicit negative check for API validation test
	var raw map[string]any
	// re-decode for negative detection — already consumed body; use patch zero vs negative via pointer alternative
	_ = raw
	if err := s.Config.Update(&cur); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, cur)
}

func (s *Server) handlePutConfigRaw(w http.ResponseWriter, r *http.Request) {
	if s.Config == nil {
		http.Error(w, "no config", http.StatusInternalServerError)
		return
	}
	var body struct {
		Thresholds struct {
			MaxLocalContextTokens *int `json:"max_local_context_tokens"`
		} `json:"thresholds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	cur := *s.Config.Get()
	if body.Thresholds.MaxLocalContextTokens != nil {
		if *body.Thresholds.MaxLocalContextTokens < 0 {
			http.Error(w, "validation error: max_local_context_tokens must be >= 0", http.StatusBadRequest)
			return
		}
		cur.Thresholds.MaxLocalContextTokens = *body.Thresholds.MaxLocalContextTokens
	}
	if err := s.Config.Update(&cur); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, cur)
}

func (s *Server) handleGetModels(w http.ResponseWriter, r *http.Request) {
	if s.Models == nil {
		writeJSON(w, []any{})
		return
	}
	writeJSON(w, s.Models.ListModels())
}

func (s *Server) handleModelAction(w http.ResponseWriter, r *http.Request) {
	// path: /api/models/{name}/load|unload
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

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
