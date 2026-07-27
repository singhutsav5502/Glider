package safego_test

import (
	"log/slog"
	"sync"
	"testing"

	"github.com/glider-ai/glider/internal/safego"
)

func TestGo_RecoversPanicWithoutCrashingProcess(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	safego.Go("test-panicker", slog.Default(), func() {
		defer wg.Done()
		panic("boom")
	})
	wg.Wait() // if this test process is still running, the panic was recovered
}

func TestGo_NilLoggerFallsBackToDefault(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	safego.Go("test-nil-logger", nil, func() {
		defer wg.Done()
		panic("boom")
	})
	wg.Wait()
}

func TestGo_NormalCompletionRunsNormally(t *testing.T) {
	done := make(chan int, 1)
	safego.Go("test-normal", slog.Default(), func() {
		done <- 42
	})
	if got := <-done; got != 42 {
		t.Fatalf("got %d", got)
	}
}
