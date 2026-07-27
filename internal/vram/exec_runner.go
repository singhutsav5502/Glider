package vram

import (
	"os/exec"

	"github.com/glider-ai/glider/internal/procutil"
)

// ExecRunner runs commands via os/exec (used by NvidiaSmiMonitor in production).
type ExecRunner struct{}

func (ExecRunner) Run(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	procutil.HideWindow(cmd)
	return cmd.CombinedOutput()
}

// NewDefaultNvidiaSmiMonitor creates a monitor backed by the real nvidia-smi binary.
func NewDefaultNvidiaSmiMonitor() *NvidiaSmiMonitor {
	return NewNvidiaSmiMonitor(ExecRunner{})
}

// AllMemoryInfo returns memory for every GPU, or an empty slice if nvidia-smi is unavailable.
func (m *NvidiaSmiMonitor) AllMemoryInfo() ([]GPUMemoryInfo, error) {
	if err := m.refresh(); err != nil {
		return nil, err
	}
	out := make([]GPUMemoryInfo, len(m.cache))
	copy(out, m.cache)
	return out, nil
}
