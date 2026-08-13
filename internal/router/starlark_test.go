package router_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/glider-ai/glider/internal/router"
)

const refactorScript = `
def evaluate(request):
    content = ""
    for msg in request.messages:
        content = content + msg.content
    if "refactor" in content:
        return {"matched": True, "action": {"target": "local", "model": "codellama:7b"}}
    return {"matched": False}
`

func writeScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

func refactorRequest() *backend.CompletionRequest {
	return &backend.CompletionRequest{
		Model: "gpt-4o",
		Messages: []backend.Message{
			{Role: "user", Content: "Please refactor this function"},
		},
		Metadata: backend.RequestMetadata{EstimatedTokens: 5000},
	}
}

// T2.5.1 — Execute valid script and get routing decision
func TestT2_5_1_Starlark_ValidScript(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "route.star", refactorScript)

	executor := router.NewStarlarkExecutor()
	result, err := executor.Run(context.Background(), path, refactorRequest())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched {
		t.Fatal("expected match")
	}
	if result.Action.Target != "local" {
		t.Fatalf("target = %q, want local", result.Action.Target)
	}
	if result.Action.Model != "codellama:7b" {
		t.Fatalf("model = %q, want codellama:7b", result.Action.Model)
	}
}

// T2.5.2 — Script returns no match
func TestT2_5_2_Starlark_NoMatch(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "route.star", refactorScript)

	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "user", Content: "Explain how this works"},
		},
	}

	executor := router.NewStarlarkExecutor()
	result, err := executor.Run(context.Background(), path, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched {
		t.Fatal("expected no match")
	}
}

// T2.5.3 — Script with syntax error
func TestT2_5_3_Starlark_SyntaxError(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "bad.star", "def evaluate(request:\n    return {\"matched\": False}\n")

	executor := router.NewStarlarkExecutor()
	_, err := executor.Run(context.Background(), path, refactorRequest())
	if err == nil {
		t.Fatal("expected syntax error")
	}
	if !strings.Contains(err.Error(), "syntax") &&
		!strings.Contains(err.Error(), "parse") &&
		!strings.Contains(err.Error(), "want") {
		t.Fatalf("expected descriptive syntax error, got: %v", err)
	}
}

// T2.5.4 — Script exceeds execution step limit
func TestT2_5_4_Starlark_StepLimit(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "loop.star", `
def evaluate(request):
    for i in range(999999999):
        pass
    return {"matched": False}
`)

	executor := router.NewStarlarkExecutor()
	start := time.Now()
	_, err := executor.Run(context.Background(), path, refactorRequest())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected step limit error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "step") &&
		!strings.Contains(strings.ToLower(err.Error()), "cancelled") {
		t.Fatalf("expected step-limit error, got: %v", err)
	}
	if elapsed > time.Second {
		t.Fatalf("expected completion within 1s, took %v", elapsed)
	}
}

// T2.5.5 — Script caching: compiled script is reused
func TestT2_5_5_Starlark_CacheReuse(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "route.star", refactorScript)

	executor := router.NewStarlarkExecutor()
	req := refactorRequest()

	start := time.Now()
	if _, err := executor.Run(context.Background(), path, req); err != nil {
		t.Fatal(err)
	}
	first := time.Since(start)

	start = time.Now()
	if _, err := executor.Run(context.Background(), path, req); err != nil {
		t.Fatal(err)
	}
	second := time.Since(start)

	absPath := mustAbs(t, path)
	if executor.ReadCount(absPath) != 1 {
		t.Fatalf("read count = %d, want 1", executor.ReadCount(absPath))
	}
	if second >= first {
		t.Logf("first=%v second=%v (second should usually be faster with cache)", first, second)
	}
}

// T2.5.6 — Script cache invalidation on file change
func TestT2_5_6_Starlark_CacheInvalidation(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "route.star", refactorScript)

	executor := router.NewStarlarkExecutor()
	req := refactorRequest()

	result, err := executor.Run(context.Background(), path, req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched {
		t.Fatal("expected initial match")
	}

	updated := `
def evaluate(request):
    return {"matched": True, "action": {"target": "cloud", "model": "gpt-4o"}}
`
	// Sleep BEFORE the write, and not after it. The cache compares the time
	// of the last change, and only the write sets that time — a sleep that
	// follows the write moves nothing. On Linux the clock that gives a file
	// its time steps in units of a few milliseconds, so two writes with no
	// pause between them carry the same time and the cache correctly sees no
	// change. This test passed on Windows and failed on Linux for that
	// reason alone.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err = executor.Run(context.Background(), path, req)
	if err != nil {
		t.Fatal(err)
	}
	if result.Action.Target != "cloud" {
		t.Fatalf("target = %q, want cloud after invalidation", result.Action.Target)
	}
	if result.Action.Model != "gpt-4o" {
		t.Fatalf("model = %q, want gpt-4o after invalidation", result.Action.Model)
	}
}

// T2.5.7 — Script cannot access filesystem
func TestT2_5_7_Starlark_Sandbox(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "sandbox.star", `
def evaluate(request):
    return read_file("secret.txt")
`)

	executor := router.NewStarlarkExecutor()
	_, err := executor.Run(context.Background(), path, refactorRequest())
	if err == nil {
		t.Fatal("expected error for read_file")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not found") &&
		!strings.Contains(strings.ToLower(err.Error()), "undefined") {
		t.Fatalf("expected function not found error, got: %v", err)
	}
}

// T2.5.8 — Request data is passed correctly to script
func TestT2_5_8_Starlark_RequestData(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "inspect.star", `
def evaluate(request):
    return {
        "matched": True,
        "action": {
            "target": "local",
            "model": request.model,
            "adapter": str(request.estimated_tokens),
        },
    }
`)

	req := &backend.CompletionRequest{
		Model: "gpt-4o",
		Messages: []backend.Message{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi"},
		},
		Metadata: backend.RequestMetadata{EstimatedTokens: 5000},
	}

	executor := router.NewStarlarkExecutor()
	result, err := executor.Run(context.Background(), path, req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched {
		t.Fatal("expected match")
	}
	if result.Action.Model != "gpt-4o" {
		t.Fatalf("model = %q, want gpt-4o", result.Action.Model)
	}
	if result.Action.Adapter != "5000" {
		t.Fatalf("estimated_tokens passthrough = %q, want 5000", result.Action.Adapter)
	}
}

// T2.5.x — Starlib regex available in scripts
func TestStarlark_RegexModule(t *testing.T) {
	dir := t.TempDir()
	path := writeScript(t, dir, "regex.star", `
load("re.star", "re")

def evaluate(request):
    content = ""
    for msg in request.messages:
        content = content + msg.content
    pattern = re.compile(r"(?i)\b(refactor|rename|extract)\b")
    if pattern.search(content):
        return {"matched": True, "action": {"target": "local", "model": "codellama:7b"}}
    return {"matched": False}
`)

	req := &backend.CompletionRequest{
		Messages: []backend.Message{
			{Role: "user", Content: "Please refactor this class"},
		},
	}

	executor := router.NewStarlarkExecutor()
	result, err := executor.Run(context.Background(), path, req)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Matched {
		t.Fatal("expected regex module match")
	}
}
