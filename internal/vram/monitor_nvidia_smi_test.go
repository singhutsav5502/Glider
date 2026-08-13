package vram_test

import (
	"errors"
	"testing"

	"github.com/glider-ai/glider/internal/vram"
)

const mib = 1024 * 1024

// T3.1.1 — Parse nvidia-smi output
func TestMonitor_ParseNvidiaSmiOutput(t *testing.T) {
	info, err := vram.ParseNvidiaSmiLine("8192, 3500, 4692")
	if err != nil {
		t.Fatalf("ParseNvidiaSmiLine: %v", err)
	}
	if info.Total != 8192*mib {
		t.Fatalf("Total: got %d want %d", info.Total, 8192*mib)
	}
	if info.Used != 3500*mib {
		t.Fatalf("Used: got %d want %d", info.Used, 3500*mib)
	}
	if info.Free != 4692*mib {
		t.Fatalf("Free: got %d want %d", info.Free, 4692*mib)
	}
}

// T3.1.2 — Handle nvidia-smi not found
func TestMonitor_NvidiaSmiNotFound(t *testing.T) {
	runner := &stubRunner{err: errors.New("exec: \"nvidia-smi\" executable file not found in %PATH%")}
	monitor := vram.NewNvidiaSmiMonitor(runner)
	_, err := monitor.GetMemoryInfo(0)
	if err == nil {
		t.Fatal("expected error when nvidia-smi is missing")
	}
}

// T3.1.3 — Multi-GPU: parse multiple GPU lines
func TestMonitor_MultiGPU(t *testing.T) {
	output := "8192, 3500, 4692\n16384, 8000, 8384"
	infos, err := vram.ParseNvidiaSmiOutput(output)
	if err != nil {
		t.Fatalf("ParseNvidiaSmiOutput: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 GPUs, got %d", len(infos))
	}
	if infos[0].Total != 8192*mib || infos[0].Used != 3500*mib || infos[0].Free != 4692*mib {
		t.Fatalf("GPU 0 memory mismatch: %+v", infos[0])
	}
	if infos[1].Total != 16384*mib || infos[1].Used != 8000*mib || infos[1].Free != 8384*mib {
		t.Fatalf("GPU 1 memory mismatch: %+v", infos[1])
	}

	runner := &stubRunner{output: []byte(output)}
	monitor := vram.NewNvidiaSmiMonitor(runner)
	if count := monitor.GetDeviceCount(); count != 2 {
		t.Fatalf("GetDeviceCount: got %d want 2", count)
	}
	gpu0, err := monitor.GetMemoryInfo(0)
	if err != nil {
		t.Fatalf("GetMemoryInfo(0): %v", err)
	}
	if gpu0.Total != 8192*mib {
		t.Fatalf("GetMemoryInfo(0) Total: got %d want %d", gpu0.Total, 8192*mib)
	}
	gpu1, err := monitor.GetMemoryInfo(1)
	if err != nil {
		t.Fatalf("GetMemoryInfo(1): %v", err)
	}
	if gpu1.Total != 16384*mib {
		t.Fatalf("GetMemoryInfo(1) Total: got %d want %d", gpu1.Total, 16384*mib)
	}
}

func TestMonitor_ParseErrors(t *testing.T) {
	if _, err := vram.ParseNvidiaSmiLine("bad"); err == nil {
		t.Fatal("expected parse error for malformed line")
	}
	if _, err := vram.ParseNvidiaSmiOutput(""); err == nil {
		t.Fatal("expected parse error for empty output")
	}
}

func TestMonitor_InvalidGPUIndex(t *testing.T) {
	runner := &stubRunner{output: []byte("8192, 3500, 4692")}
	monitor := vram.NewNvidiaSmiMonitor(runner)
	if _, err := monitor.GetMemoryInfo(1); err == nil {
		t.Fatal("expected error for out-of-range GPU index")
	}
}

type stubRunner struct {
	output []byte
	err    error
}

func (s *stubRunner) Run(_ string, _ ...string) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.output, nil
}
