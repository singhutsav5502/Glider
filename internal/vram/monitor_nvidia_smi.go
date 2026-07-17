package vram

import (
	"fmt"
	"strconv"
	"strings"
)

// CommandRunner executes external commands (injectable for tests).
type CommandRunner interface {
	Run(name string, args ...string) ([]byte, error)
}

// NvidiaSmiMonitor reads GPU memory via nvidia-smi.
type NvidiaSmiMonitor struct {
	runner CommandRunner
	cache  []GPUMemoryInfo
}

// NewNvidiaSmiMonitor creates a monitor using the given command runner.
func NewNvidiaSmiMonitor(runner CommandRunner) *NvidiaSmiMonitor {
	return &NvidiaSmiMonitor{runner: runner}
}

const nvidiaSmiQuery = "memory.total,memory.used,memory.free"

// GetMemoryInfo returns memory stats for the given GPU index.
func (m *NvidiaSmiMonitor) GetMemoryInfo(gpuIndex int) (GPUMemoryInfo, error) {
	if err := m.refresh(); err != nil {
		return GPUMemoryInfo{}, err
	}
	if gpuIndex < 0 || gpuIndex >= len(m.cache) {
		return GPUMemoryInfo{}, fmt.Errorf("gpu index %d out of range (count=%d)", gpuIndex, len(m.cache))
	}
	return m.cache[gpuIndex], nil
}

// GetDeviceCount returns the number of GPUs reported by nvidia-smi.
func (m *NvidiaSmiMonitor) GetDeviceCount() int {
	if err := m.refresh(); err != nil {
		return 0
	}
	return len(m.cache)
}

func (m *NvidiaSmiMonitor) refresh() error {
	out, err := m.runner.Run("nvidia-smi",
		"--query-gpu="+nvidiaSmiQuery,
		"--format=csv,noheader,nounits",
	)
	if err != nil {
		return fmt.Errorf("nvidia-smi unavailable: %w", err)
	}
	infos, err := ParseNvidiaSmiOutput(string(out))
	if err != nil {
		return err
	}
	m.cache = infos
	return nil
}

// ParseNvidiaSmiOutput parses multi-line nvidia-smi CSV output.
func ParseNvidiaSmiOutput(output string) ([]GPUMemoryInfo, error) {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return nil, fmt.Errorf("empty nvidia-smi output")
	}
	infos := make([]GPUMemoryInfo, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		info, err := ParseNvidiaSmiLine(line)
		if err != nil {
			return nil, err
		}
		infos = append(infos, info)
	}
	if len(infos) == 0 {
		return nil, fmt.Errorf("no GPU memory data parsed")
	}
	return infos, nil
}

// ParseNvidiaSmiLine parses one CSV line of total,used,free values in MiB.
func ParseNvidiaSmiLine(line string) (GPUMemoryInfo, error) {
	line = strings.TrimSpace(line)
	line = strings.TrimSuffix(line, " MiB")
	parts := strings.Split(line, ",")
	if len(parts) != 3 {
		return GPUMemoryInfo{}, fmt.Errorf("expected 3 comma-separated values, got %d", len(parts))
	}

	total, err := parseMiB(parts[0])
	if err != nil {
		return GPUMemoryInfo{}, fmt.Errorf("parse total: %w", err)
	}
	used, err := parseMiB(parts[1])
	if err != nil {
		return GPUMemoryInfo{}, fmt.Errorf("parse used: %w", err)
	}
	free, err := parseMiB(parts[2])
	if err != nil {
		return GPUMemoryInfo{}, fmt.Errorf("parse free: %w", err)
	}

	return GPUMemoryInfo{Total: total, Used: used, Free: free}, nil
}

func parseMiB(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, " MiB")
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, err
	}
	return v * 1024 * 1024, nil
}
