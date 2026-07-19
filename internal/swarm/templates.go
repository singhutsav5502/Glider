package swarm

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Template is a reusable swarm fan-out recipe (roles + models + prompt scaffold).
type Template struct {
	ID          string      `json:"id" yaml:"id"`
	Name        string      `json:"name,omitempty" yaml:"name,omitempty"`
	Prompt      string      `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	Roles       []string    `json:"roles,omitempty" yaml:"roles,omitempty"` // plan|exec|research|worker
	Models      []string    `json:"models,omitempty" yaml:"models,omitempty"`
	MaxWorkers  int         `json:"max_workers,omitempty" yaml:"max_workers,omitempty"`
	PreferLocal bool        `json:"prefer_local,omitempty" yaml:"prefer_local,omitempty"`
	Enabled     bool        `json:"enabled" yaml:"enabled"`
	Description string      `json:"description,omitempty" yaml:"description,omitempty"`
	Waves       int         `json:"waves,omitempty" yaml:"waves,omitempty"`
	WeavePolicy WeavePolicy `json:"weave_policy,omitempty" yaml:"weave_policy,omitempty"`
	Decompose   bool        `json:"decompose,omitempty" yaml:"decompose,omitempty"`
	SubTasks    []string    `json:"subtasks,omitempty" yaml:"subtasks,omitempty"`
}

// Normalize fills defaults and validates.
func (t *Template) Normalize() error {
	if t == nil {
		return fmt.Errorf("nil template")
	}
	t.ID = sanitizeTemplateID(t.ID)
	t.Name = strings.TrimSpace(t.Name)
	t.Prompt = strings.TrimSpace(t.Prompt)
	if t.ID == "" {
		return fmt.Errorf("template id required")
	}
	if t.Name == "" {
		t.Name = t.ID
	}
	if t.MaxWorkers <= 0 {
		t.MaxWorkers = 2
	}
	if t.MaxWorkers > 4 {
		t.MaxWorkers = 4
	}
	if len(t.Roles) == 0 {
		t.Roles = []string{string(RolePlan), string(RoleExec)}
	}
	return nil
}

func sanitizeTemplateID(id string) string {
	id = strings.TrimSpace(id)
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// DefaultTemplatesDir returns ~/.glider/hoops (swarm templates + hoop YAML live here).
func DefaultTemplatesDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".glider", "hoops")
	}
	return filepath.Join(home, ".glider", "hoops")
}

// TemplateStore persists swarm templates as YAML under Dir.
type TemplateStore struct {
	mu  sync.Mutex
	Dir string
}

// NewTemplateStore creates a file-backed template store. Empty dir → DefaultTemplatesDir().
func NewTemplateStore(dir string) *TemplateStore {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultTemplatesDir()
	}
	return &TemplateStore{Dir: dir}
}

func (s *TemplateStore) ensureDir() error {
	return os.MkdirAll(s.Dir, 0o755)
}

func (s *TemplateStore) path(id string) string {
	return filepath.Join(s.Dir, sanitizeTemplateID(id)+".yaml")
}

// List returns all swarm templates (files with kind swarm_template or missing kind).
func (s *TemplateStore) List() ([]Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, err
	}
	var out []Template
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		t, err := s.readFile(filepath.Join(s.Dir, e.Name()))
		if err != nil || t == nil {
			continue
		}
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Get loads one template by id.
func (s *TemplateStore) Get(id string) (*Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readFile(s.path(id))
}

// flatHoopFile matches samples/swarms/*.yaml (fields at top level).
type flatHoopFile struct {
	Kind string `yaml:"kind"`
	Template `yaml:",inline"`
}

// nestedHoopFile matches TemplateStore.Save output (fields under template:).
type nestedHoopFile struct {
	Kind     string   `yaml:"kind"`
	Template Template `yaml:"template"`
}

func (s *TemplateStore) readFile(path string) (*Template, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var t Template
	var kind string
	var nested nestedHoopFile
	if err := yaml.Unmarshal(data, &nested); err == nil && (nested.Template.ID != "" || nested.Template.Prompt != "") {
		t = nested.Template
		kind = nested.Kind
	} else {
		var flat flatHoopFile
		if err := yaml.Unmarshal(data, &flat); err != nil {
			return nil, err
		}
		t = flat.Template
		kind = flat.Kind
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "" && kind != "swarm_template" && kind != "swarm" {
		return nil, fmt.Errorf("not a swarm template")
	}
	if t.ID == "" {
		base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		t.ID = base
	}
	_ = t.Normalize()
	return &t, nil
}

// Save writes a swarm template YAML atomically (flat, sample-compatible).
func (s *TemplateStore) Save(t *Template) error {
	if t == nil {
		return fmt.Errorf("nil template")
	}
	if err := t.Normalize(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.ensureDir(); err != nil {
		return err
	}
	payload := flatHoopFile{Kind: "swarm_template", Template: *t}
	data, err := yaml.Marshal(payload)
	if err != nil {
		return err
	}
	path := s.path(t.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Delete removes a template file.
func (s *TemplateStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.path(id))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
