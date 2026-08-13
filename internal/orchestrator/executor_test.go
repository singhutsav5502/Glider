package orchestrator_test

import (
	"testing"

	"github.com/glider-ai/glider/internal/orchestrator"
)

// T3.4.0 — SimpleExecutor implements Executor interface
func TestSimpleExecutor_ImplementsExecutor(t *testing.T) {
	var _ orchestrator.Executor = (*orchestrator.SimpleExecutor)(nil)
}
