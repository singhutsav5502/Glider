package loop

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultHoopsDir returns ~/.glider/hoops (YAML mirrors for dashboard "create hoop").
func DefaultHoopsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".", ".glider", "hoops")
	}
	return filepath.Join(home, ".glider", "hoops")
}

// HoopYAML is the portable hoop definition written under ~/.glider/hoops/*.yaml.
type HoopYAML struct {
	Kind     string      `yaml:"kind"`
	ID       string      `yaml:"id"`
	Name     string      `yaml:"name,omitempty"`
	Goal     string      `yaml:"goal,omitempty"`
	Interval string      `yaml:"interval,omitempty"`
	Cron     string      `yaml:"cron,omitempty"`
	Prompt   string      `yaml:"prompt,omitempty"`
	Route    string      `yaml:"route,omitempty"`
	Learning bool        `yaml:"learning,omitempty"`
	Model    string      `yaml:"model,omitempty"`
	Autonomy string      `yaml:"autonomy,omitempty"`
	Stages   []StageSpec `yaml:"stages,omitempty"`
	Eval     EvalSpec    `yaml:"eval,omitempty"`
	Enabled  bool        `yaml:"enabled"`
}

// WriteHoopYAML persists a hoop mirror next to swarm templates.
func WriteHoopYAML(dir string, spec LoopSpec) error {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultHoopsDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	id := sanitizeID(spec.ID)
	if id == "" {
		return fmt.Errorf("empty hoop id")
	}
	goal := spec.Goal
	if goal == "" {
		goal = spec.Prompt
	}
	payload := HoopYAML{
		Kind:     "hoop",
		ID:       id,
		Name:     spec.Name,
		Goal:     goal,
		Interval: spec.Interval,
		Cron:     spec.Cron,
		Prompt:   spec.Prompt,
		Route:    string(spec.Route),
		Learning: spec.Learning,
		Model:    spec.Model,
		Autonomy: string(spec.Autonomy),
		Stages:   spec.Stages,
		Eval:     spec.Eval,
		Enabled:  true,
	}
	data, err := yaml.Marshal(payload)
	if err != nil {
		return err
	}
	path := filepath.Join(dir, id+".yaml")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// DeleteHoopYAML removes the YAML mirror if present.
func DeleteHoopYAML(dir, id string) error {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultHoopsDir()
	}
	err := os.Remove(filepath.Join(dir, sanitizeID(id)+".yaml"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
