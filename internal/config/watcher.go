package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fsnotify/fsnotify"
)

type ChangeFunc func(*Config)

// Provider holds the active config with atomic swap + hot-reload.
type Provider struct {
	value       atomic.Pointer[Config]
	path        string
	mu          sync.Mutex
	subscribers []ChangeFunc
	log         *slog.Logger
	watcher     *fsnotify.Watcher
	stopCh      chan struct{}
}

func NewProvider(cfg *Config, path string) *Provider {
	p := &Provider{path: path, log: slog.Default(), stopCh: make(chan struct{})}
	p.value.Store(cfg)
	return p
}

func (p *Provider) Get() *Config {
	return p.value.Load()
}

func (p *Provider) Watch(cb ChangeFunc) {
	p.mu.Lock()
	p.subscribers = append(p.subscribers, cb)
	p.mu.Unlock()
}

func (p *Provider) StartWatcher() error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	p.watcher = w
	dir := filepath.Dir(p.path)
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return err
	}
	go p.loop()
	return nil
}

func (p *Provider) Stop() {
	close(p.stopCh)
	if p.watcher != nil {
		_ = p.watcher.Close()
	}
}

func (p *Provider) loop() {
	var debounce <-chan time.Time
	for {
		select {
		case <-p.stopCh:
			return
		case ev, ok := <-p.watcher.Events:
			if !ok {
				return
			}
			if filepath.Clean(ev.Name) != filepath.Clean(p.path) {
				continue
			}
			if ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) == 0 {
				continue
			}
			debounce = time.After(50 * time.Millisecond)
		case err, ok := <-p.watcher.Errors:
			if !ok {
				return
			}
			p.log.Error("config watcher error", "err", err)
		case <-debounce:
			p.reload()
			debounce = nil
		}
	}
}

func (p *Provider) reload() {
	data, err := os.ReadFile(p.path)
	if err != nil {
		p.log.Error("config reload read failed", "err", err)
		return
	}
	cfg, err := ParseConfig(data)
	if err != nil {
		p.log.Error("config reload rejected invalid config", "err", err)
		return
	}
	p.value.Store(cfg)
	p.mu.Lock()
	subs := append([]ChangeFunc{}, p.subscribers...)
	p.mu.Unlock()
	for _, cb := range subs {
		cb(cfg)
	}
}

// SwapForTest atomically replaces config (tests only).
func (p *Provider) SwapForTest(cfg *Config) {
	p.value.Store(cfg)
}
