package dashboard

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/backend/ollama"
	"github.com/glider-ai/glider/internal/backend/vllm"
	"github.com/glider-ai/glider/internal/config"
	"github.com/glider-ai/glider/internal/vram"
)

// DiscoveredModel is a model visible from config, registry, and/or live backends.
type DiscoveredModel struct {
	Name           string `json:"name"`
	Backend        string `json:"backend"`
	Source         string `json:"source"` // config | registry | ollama | vllm
	VRAMEstimateMB int    `json:"vram_estimate_mb,omitempty"`
	State          string `json:"state,omitempty"`
	InConfig       bool   `json:"in_config"`
	GPU            *int   `json:"gpu,omitempty"`
	SizeVRAM       int64  `json:"size_vram,omitempty"`
	Available      bool   `json:"available"` // seen on a live backend or registry
}

// GPUStatus is live (or unavailable) GPU memory.
type GPUStatus struct {
	Index      int    `json:"index"`
	TotalBytes int64  `json:"total_bytes"`
	UsedBytes  int64  `json:"used_bytes"`
	FreeBytes  int64  `json:"free_bytes"`
	TotalMB    int64  `json:"total_mb"`
	UsedMB     int64  `json:"used_mb"`
	FreeMB     int64  `json:"free_mb"`
	Error      string `json:"error,omitempty"`
}

// VRAMSnapshot is the payload for GET /api/vram.
type VRAMSnapshot struct {
	GPUs            []GPUStatus       `json:"gpus"`
	Models          []DiscoveredModel `json:"models"`
	GPUAssignments  map[string]int    `json:"gpu_assignments"`
	Strategy        string            `json:"strategy"`
	HeadroomMB      int               `json:"headroom_mb"`
	MaxLoadedModels int               `json:"max_loaded_models"`
	BackendErrors   []string          `json:"backend_errors,omitempty"`
	BackendWarnings []string          `json:"backend_warnings,omitempty"`
	Catalog         []string          `json:"catalog"`
}

// GPUInfoProvider abstracts nvidia-smi (injectable in tests).
type GPUInfoProvider interface {
	AllMemoryInfo() ([]vram.GPUMemoryInfo, error)
}

type nvidiaProvider struct {
	mon *vram.NvidiaSmiMonitor
}

func (n nvidiaProvider) AllMemoryInfo() ([]vram.GPUMemoryInfo, error) {
	return n.mon.AllMemoryInfo()
}

// DiscoverModels probes configured backends and merges with config + registry.
// When at least one local backend responds, unreachable peers are returned as
// warnings (optional backends) rather than hard errors.
func DiscoverModels(ctx context.Context, cfg *config.Config, reg *backend.Registry) (models []DiscoveredModel, catalog config.ModelCatalog, backendErrs, backendWarns []string) {
	byName := map[string]*DiscoveredModel{}
	catalog = config.NewModelCatalog()

	upsert := func(name, backendName, source string, available bool) *DiscoveredModel {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil
		}
		catalog[name] = struct{}{}
		if existing, ok := byName[name]; ok {
			if available {
				existing.Available = true
			}
			if existing.Backend == "" && backendName != "" {
				existing.Backend = backendName
			}
			if source != "" && existing.Source == "config" {
				existing.Source = source
			}
			return existing
		}
		m := &DiscoveredModel{
			Name:      name,
			Backend:   backendName,
			Source:    source,
			Available: available,
		}
		byName[name] = m
		return m
	}

	if cfg != nil {
		for _, m := range cfg.Models {
			dm := upsert(m.Name, m.Backend, "config", false)
			if dm == nil {
				continue
			}
			dm.InConfig = true
			dm.VRAMEstimateMB = m.VRAMEstimateMB
			if cfg.VRAM.GPUAssignments != nil {
				if g, ok := cfg.VRAM.GPUAssignments[m.Name]; ok {
					gCopy := g
					dm.GPU = &gCopy
				}
			}
		}
		for model, g := range cfg.VRAM.GPUAssignments {
			dm := upsert(model, "", "config", false)
			if dm != nil {
				gCopy := g
				dm.GPU = &gCopy
			}
		}
	}

	if reg != nil {
		for _, info := range reg.ListModels() {
			dm := upsert(info.Name, info.Backend, "registry", true)
			if dm == nil {
				continue
			}
			if dm.VRAMEstimateMB == 0 {
				dm.VRAMEstimateMB = info.VRAMEstimateMB
			}
			dm.State = string(info.State)
		}
	}

	var probeFails []string
	liveOK := false
	if cfg != nil {
		clientTimeout := 4 * time.Second
		for _, b := range cfg.Backends {
			bctx, cancel := context.WithTimeout(ctx, clientTimeout)
			switch {
			case b.Name == "ollama" || (b.Type == "local" && !strings.Contains(strings.ToLower(b.Name), "vllm")):
				ob := ollama.New(b.URL)
				tags, err := ob.ListTags(bctx)
				if err != nil {
					probeFails = append(probeFails, "ollama("+b.URL+"): "+err.Error())
				} else {
					liveOK = true
					for _, tag := range tags {
						upsert(tag, b.Name, "ollama", true)
					}
					if loaded, err := ob.ListLoaded(bctx); err == nil {
						for _, lm := range loaded {
							dm := upsert(lm.Name, b.Name, "ollama", true)
							if dm != nil {
								dm.SizeVRAM = lm.SizeVRAM
								if dm.State == "" {
									dm.State = string(backend.ModelStateWarm)
								}
							}
						}
					}
				}
			case b.Name == "vllm" || strings.Contains(strings.ToLower(b.Name), "vllm"):
				vb := vllm.New(b.URL)
				ids, err := vb.ListModels(bctx)
				if err != nil {
					probeFails = append(probeFails, "vllm("+b.URL+"): "+err.Error())
				} else {
					liveOK = true
					for _, id := range ids {
						upsert(id, b.Name, "vllm", true)
					}
				}
			}
			cancel()
		}
	}

	if liveOK {
		backendWarns = probeFails
	} else {
		backendErrs = probeFails
	}

	models = make([]DiscoveredModel, 0, len(byName))
	for _, m := range byName {
		models = append(models, *m)
	}
	sort.Slice(models, func(i, j int) bool {
		if models[i].InConfig != models[j].InConfig {
			return models[i].InConfig
		}
		return models[i].Name < models[j].Name
	})
	return models, catalog, backendErrs, backendWarns
}

func collectGPUStatus(provider GPUInfoProvider) []GPUStatus {
	if provider == nil {
		provider = nvidiaProvider{mon: vram.NewDefaultNvidiaSmiMonitor()}
	}
	infos, err := provider.AllMemoryInfo()
	if err != nil {
		return []GPUStatus{{Index: 0, Error: err.Error()}}
	}
	out := make([]GPUStatus, 0, len(infos))
	for i, info := range infos {
		out = append(out, GPUStatus{
			Index:      i,
			TotalBytes: info.Total,
			UsedBytes:  info.Used,
			FreeBytes:  info.Free,
			TotalMB:    info.Total / (1024 * 1024),
			UsedMB:     info.Used / (1024 * 1024),
			FreeMB:     info.Free / (1024 * 1024),
		})
	}
	return out
}
