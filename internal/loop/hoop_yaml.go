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

// HoopYAML is the portable hoop definition written under ~/.glider/hoops/*.yaml
// and samples/hoops/*.yaml.
type HoopYAML struct {
	Kind          string         `yaml:"kind"`
	ID            string         `yaml:"id"`
	Name          string         `yaml:"name,omitempty"`
	Goal          string         `yaml:"goal,omitempty"`
	Interval      string         `yaml:"interval,omitempty"`
	Cron          string         `yaml:"cron,omitempty"`
	Prompt        string         `yaml:"prompt,omitempty"`
	Route         string         `yaml:"route,omitempty"`
	Learning      bool           `yaml:"learning,omitempty"`
	Model         string         `yaml:"model,omitempty"`
	Autonomy      string         `yaml:"autonomy,omitempty"`
	HumanGate     bool           `yaml:"human_gate,omitempty"`
	MaxIterations int            `yaml:"max_iterations,omitempty"`
	FailPolicy    string         `yaml:"fail_policy,omitempty"`
	Stop          StopConditions `yaml:"stop_conditions,omitempty"`
	Stages        []StageSpec    `yaml:"stages,omitempty"`
	GraphEdges    []GraphEdge    `yaml:"graph_edges,omitempty"`
	GraphVersion  string         `yaml:"graph_version,omitempty"`
	Topology      string         `yaml:"topology,omitempty"`
	Eval          EvalSpec       `yaml:"eval,omitempty"`
	Enabled       bool           `yaml:"enabled"`
}

// SpecFromHoopYAML converts a portable YAML definition into a LoopSpec.
func SpecFromHoopYAML(h HoopYAML) (LoopSpec, error) {
	spec := LoopSpec{
		ID:            h.ID,
		Name:          h.Name,
		Goal:          h.Goal,
		Interval:      h.Interval,
		Cron:          h.Cron,
		Prompt:        h.Prompt,
		Route:         RoutePref(h.Route),
		Learning:      h.Learning,
		Model:         h.Model,
		Autonomy:      AutonomyLevel(h.Autonomy),
		HumanGate:     h.HumanGate,
		MaxIterations: h.MaxIterations,
		FailPolicy:    FailPolicy(h.FailPolicy),
		Stop:          h.Stop,
		Stages:        h.Stages,
		GraphEdges:    h.GraphEdges,
		GraphVersion:  h.GraphVersion,
		Topology:      h.Topology,
		Eval:          h.Eval,
	}
	if err := spec.Normalize(); err != nil {
		return LoopSpec{}, err
	}
	return spec, nil
}

// ReadHoopYAMLFile loads a hoop YAML file from disk.
func ReadHoopYAMLFile(path string) (LoopSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return LoopSpec{}, err
	}
	var h HoopYAML
	if err := yaml.Unmarshal(data, &h); err != nil {
		return LoopSpec{}, fmt.Errorf("yaml: %w", err)
	}
	if strings.TrimSpace(h.Kind) != "" && !strings.EqualFold(h.Kind, "hoop") {
		return LoopSpec{}, fmt.Errorf("kind %q: expected hoop", h.Kind)
	}
	return SpecFromHoopYAML(h)
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
		Kind:          "hoop",
		ID:            id,
		Name:          spec.Name,
		Goal:          goal,
		Interval:      spec.Interval,
		Cron:          spec.Cron,
		Prompt:        spec.Prompt,
		Route:         string(spec.Route),
		Learning:      spec.Learning,
		Model:         spec.Model,
		Autonomy:      string(spec.Autonomy),
		HumanGate:     spec.HumanGate,
		MaxIterations: spec.MaxIterations,
		FailPolicy:    string(spec.FailPolicy),
		Stop:          spec.Stop,
		Stages:        spec.Stages,
		GraphEdges:    spec.GraphEdges,
		GraphVersion:  spec.GraphVersion,
		Topology:      spec.Topology,
		Eval:          spec.Eval,
		Enabled:       true,
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
