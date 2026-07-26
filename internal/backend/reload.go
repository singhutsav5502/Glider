package backend

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/glider-ai/glider/internal/config"
)

// Snapshot is a fully built backend+model set ready for ReplaceAll.
type Snapshot struct {
	Backends map[string]InferenceBackend
	Models   []ModelInfo
	Warnings []string
}

// BuildFunc constructs a Snapshot from config. Returning an error leaves the live Registry untouched.
type BuildFunc func(cfg *config.Config) (*Snapshot, error)

// ReloadStatus is the last Apply outcome for dashboard / API signals.
type ReloadStatus struct {
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	Warnings  []string  `json:"warnings,omitempty"`
	Backends  []string  `json:"backends,omitempty"`
	Models    int       `json:"models,omitempty"`
	At        time.Time `json:"at"`
	Attempted bool      `json:"attempted"`
}

// Reloader builds a Snapshot then atomically swaps the Registry.
// In-flight Complete() calls keep the previous InferenceBackend pointer until they finish.
type Reloader struct {
	Registry *Registry
	Build    BuildFunc
	// AfterSwap runs only after a successful ReplaceAll (e.g. update cloud-fallback flag).
	AfterSwap func(cfg *config.Config)
	// WarmPing soft-checks HealthChecker backends after build; failures become Warnings, not errors.
	WarmPing    bool
	PingTimeout time.Duration
	Log         *slog.Logger

	mu   sync.Mutex
	last ReloadStatus
}

// Status returns a copy of the last reload outcome.
func (r *Reloader) Status() ReloadStatus {
	if r == nil {
		return ReloadStatus{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.last
	if len(r.last.Warnings) > 0 {
		out.Warnings = append([]string{}, r.last.Warnings...)
	}
	if len(r.last.Backends) > 0 {
		out.Backends = append([]string{}, r.last.Backends...)
	}
	return out
}

// Apply builds a new Snapshot and swaps the Registry. On Build/validate failure the
// previous clients remain registered (hot-swap generation should not bump).
func (r *Reloader) Apply(cfg *config.Config) error {
	if r == nil || r.Registry == nil {
		return fmt.Errorf("backend: nil reloader")
	}
	if r.Build == nil {
		return fmt.Errorf("backend: nil Build")
	}
	if cfg == nil {
		err := fmt.Errorf("backend: nil config")
		r.setStatus(false, err.Error(), nil, nil, 0)
		return err
	}

	snap, err := r.Build(cfg)
	if err != nil {
		r.setStatus(false, err.Error(), nil, nil, 0)
		return err
	}
	if snap == nil || len(snap.Backends) == 0 {
		err = fmt.Errorf("backend: reload produced no backends")
		r.setStatus(false, err.Error(), nil, nil, 0)
		return err
	}

	warnings := append([]string{}, snap.Warnings...)
	for _, m := range snap.Models {
		if m.Backend == "" {
			continue
		}
		if _, ok := snap.Backends[m.Backend]; !ok {
			err = fmt.Errorf("backend: model %q references unknown backend %q", m.Name, m.Backend)
			r.setStatus(false, err.Error(), warnings, nil, 0)
			return err
		}
	}

	if r.WarmPing {
		timeout := r.PingTimeout
		if timeout <= 0 {
			timeout = 2 * time.Second
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		for name, b := range snap.Backends {
			hc, ok := b.(HealthChecker)
			if !ok {
				continue
			}
			if err := hc.Ping(ctx); err != nil {
				warnings = append(warnings, fmt.Sprintf("%s: ping failed: %v", name, err))
				if r.Log != nil {
					r.Log.Warn("backend reload warm ping failed", "backend", name, "err", err)
				}
			}
		}
	}

	names := make([]string, 0, len(snap.Backends))
	for name := range snap.Backends {
		names = append(names, name)
	}
	r.Registry.ReplaceAll(snap.Backends, snap.Models)
	if r.AfterSwap != nil {
		r.AfterSwap(cfg)
	}
	r.setStatus(true, "", warnings, names, len(snap.Models))
	if r.Log != nil {
		r.Log.Info("backends reloaded", "backends", names, "models", len(snap.Models), "warnings", len(warnings))
	}
	return nil
}

func (r *Reloader) setStatus(ok bool, errMsg string, warnings, backends []string, models int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = ReloadStatus{
		OK:        ok,
		Error:     errMsg,
		Warnings:  append([]string{}, warnings...),
		Backends:  append([]string{}, backends...),
		Models:    models,
		At:        time.Now().UTC(),
		Attempted: true,
	}
}
