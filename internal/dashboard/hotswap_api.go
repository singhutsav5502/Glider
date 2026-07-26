package dashboard

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/glider-ai/glider/internal/hotswap"
)

func (s *Server) handleHotSwapList(w http.ResponseWriter, r *http.Request) {
	type payload struct {
		Modules []hotswap.ModuleInfo `json:"modules"`
		Docs    map[string]string    `json:"docs"`
		Catalog []hotswap.ModuleInfo `json:"catalog"`
	}
	out := payload{
		Modules: []hotswap.ModuleInfo{},
		Docs:    hotswap.Docs(),
		Catalog: hotswap.BuiltinCatalog(),
	}
	if s.HotSwap != nil {
		out.Modules = s.HotSwap.List()
		if out.Modules == nil {
			out.Modules = []hotswap.ModuleInfo{}
		}
	}
	writeJSON(w, out)
}

func (s *Server) handleHotSwapEnable(w http.ResponseWriter, r *http.Request) {
	if s.HotSwap == nil {
		http.Error(w, "hot-swap registry not configured", http.StatusServiceUnavailable)
		return
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/hotswap/modules/"), "/")
	if name == "" {
		http.Error(w, "missing module name", http.StatusBadRequest)
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()
	if err := s.HotSwap.SetEnabled(name, body.Enabled); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"name": name, "enabled": body.Enabled})
}
