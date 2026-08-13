package router

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/glider-ai/glider/internal/backend"
	"github.com/qri-io/starlib/re"
	"go.starlark.net/starlark"
	"go.starlark.net/starlarkstruct"
	"go.starlark.net/syntax"
)

const defaultMaxExecutionSteps = 1_000_000

// StarlarkResult is the parsed outcome of a Starlark evaluate() call.
type StarlarkResult struct {
	Matched bool
	Action  *backend.RoutingDecision
}

type cachedProgram struct {
	modTime time.Time
	size    int64
	prog    *starlark.Program
}

type fileReader interface {
	ReadFile(path string) ([]byte, error)
	Stat(path string) (os.FileInfo, error)
}

type osFileReader struct{}

func (osFileReader) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
func (osFileReader) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }

// StarlarkExecutor runs sandboxed Starlark routing scripts with compile caching.
type StarlarkExecutor struct {
	MaxExecutionSteps uint64
	files             fileReader
	readCount         map[string]int
	readCountMu       sync.Mutex

	mu    sync.Mutex
	cache map[string]*cachedProgram
}

// NewStarlarkExecutor creates an executor with default step limits and OS file access.
func NewStarlarkExecutor() *StarlarkExecutor {
	return &StarlarkExecutor{
		MaxExecutionSteps: defaultMaxExecutionSteps,
		files:             osFileReader{},
		cache:             make(map[string]*cachedProgram),
		readCount:         make(map[string]int),
	}
}

// ReadCount returns how many times a script path was read from disk (for tests).
func (e *StarlarkExecutor) ReadCount(path string) int {
	e.readCountMu.Lock()
	defer e.readCountMu.Unlock()
	return e.readCount[path]
}

func (e *StarlarkExecutor) recordRead(path string) {
	e.readCountMu.Lock()
	defer e.readCountMu.Unlock()
	e.readCount[path]++
}

// Run executes the script at scriptPath against the request.
func (e *StarlarkExecutor) Run(ctx context.Context, scriptPath string, req *backend.CompletionRequest) (*StarlarkResult, error) {
	_ = ctx

	absPath, err := filepath.Abs(scriptPath)
	if err != nil {
		return nil, fmt.Errorf("resolve script path: %w", err)
	}

	prog, err := e.getProgram(absPath)
	if err != nil {
		return nil, err
	}

	thread := &starlark.Thread{
		Name: "router",
		Load: sandboxLoad,
	}
	if e.MaxExecutionSteps > 0 {
		thread.SetMaxExecutionSteps(e.MaxExecutionSteps)
	}

	predeclared := starlark.StringDict{}
	globals, err := prog.Init(thread, predeclared)
	if err != nil {
		return nil, fmt.Errorf("init script: %w", err)
	}

	evalFn, ok := globals["evaluate"]
	if !ok {
		return nil, fmt.Errorf("script must define evaluate(request)")
	}
	callable, ok := evalFn.(starlark.Callable)
	if !ok {
		return nil, fmt.Errorf("evaluate must be a function")
	}

	reqValue, err := requestToStarlark(req)
	if err != nil {
		return nil, err
	}

	result, err := starlark.Call(thread, callable, starlark.Tuple{reqValue}, nil)
	if err != nil {
		if isStepLimitError(err) {
			return nil, fmt.Errorf("execution step limit exceeded: %w", err)
		}
		return nil, err
	}

	return parseStarlarkResult(result)
}

func (e *StarlarkExecutor) getProgram(path string) (*starlark.Program, error) {
	info, err := e.files.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat script %q: %w", path, err)
	}
	modTime := info.ModTime()
	size := info.Size()

	e.mu.Lock()
	defer e.mu.Unlock()

	// Size is compared as well as the time of the last change. The time of
	// the last change is not sufficient by itself.
	//
	// Linux gives a file the coarse clock of the kernel, and its step is a
	// small number of milliseconds. Two writes inside one step therefore
	// carry the SAME time, and a cache that compares only the time keeps the
	// program from the first write. Windows has a finer resolution, so the
	// same code behaves correctly there. A live run on 2026-08-13 found this:
	// TestT2_5_6_Starlark_CacheInvalidation passes on Windows and fails on
	// Linux, and it had never run on Linux before.
	//
	// This matters outside a test, because Glider reloads routing rules while
	// it operates. A rule file that a program rewrites, and not a person, can
	// easily receive two writes inside one step of that clock.
	//
	// Size does not close the gap completely: two versions of one file can
	// have the same size and the same time. To close it completely needs a
	// hash of the content, and that needs a read of the file on every call,
	// which is the work this cache exists to avoid.
	if cached, ok := e.cache[path]; ok && cached.size == size && cached.modTime.Equal(modTime) {
		return cached.prog, nil
	}

	e.recordRead(path)
	src, err := e.files.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read script %q: %w", path, err)
	}

	predeclared := starlark.StringDict{}
	_, prog, err := starlark.SourceProgramOptions(&syntax.FileOptions{}, path, src, predeclared.Has)
	if err != nil {
		return nil, err
	}

	e.cache[path] = &cachedProgram{modTime: modTime, size: size, prog: prog}
	return prog, nil
}

func sandboxLoad(thread *starlark.Thread, module string) (starlark.StringDict, error) {
	switch module {
	case re.ModuleName:
		return re.LoadModule()
	default:
		return nil, fmt.Errorf("module %q not available in sandbox", module)
	}
}

func requestToStarlark(req *backend.CompletionRequest) (starlark.Value, error) {
	messages := make([]starlark.Value, len(req.Messages))
	for i, msg := range req.Messages {
		messages[i] = starlarkstruct.FromStringDict(starlark.String("message"), starlark.StringDict{
			"role":    starlark.String(msg.Role),
			"content": starlark.String(msg.Content),
		})
	}

	data := starlark.StringDict{
		"model":             starlark.String(req.Model),
		"stream":            starlark.Bool(req.Stream),
		"estimated_tokens":  starlark.MakeInt(req.Metadata.EstimatedTokens),
		"messages":          starlark.NewList(messages),
		"has_tools":         starlark.Bool(req.HasTools()),
		"complexity_score":  starlark.MakeInt(req.Metadata.ComplexityScore),
		"complexity_source": starlark.String(req.Metadata.ComplexitySource),
	}
	if req.Temperature != nil {
		data["temperature"] = starlark.Float(*req.Temperature)
	}
	if req.MaxTokens != nil {
		data["max_tokens"] = starlark.MakeInt(*req.MaxTokens)
	}

	return starlarkstruct.FromStringDict(starlark.String("request"), data), nil
}

func parseStarlarkResult(val starlark.Value) (*StarlarkResult, error) {
	dict, ok := val.(*starlark.Dict)
	if !ok {
		return nil, fmt.Errorf("evaluate must return a dict, got %s", val.Type())
	}

	matchedVal, _, err := dict.Get(starlark.String("matched"))
	if err != nil {
		return nil, err
	}
	matched, ok := matchedVal.(starlark.Bool)
	if !ok {
		return nil, fmt.Errorf("matched must be a bool")
	}
	if !bool(matched) {
		return &StarlarkResult{Matched: false}, nil
	}

	actionVal, found, err := dict.Get(starlark.String("action"))
	if err != nil {
		return nil, err
	}
	if !found || actionVal == starlark.None {
		return &StarlarkResult{Matched: true, Action: &backend.RoutingDecision{Strategy: backend.StrategySingle}}, nil
	}

	actionDict, ok := actionVal.(*starlark.Dict)
	if !ok {
		return nil, fmt.Errorf("action must be a dict")
	}

	decision := &backend.RoutingDecision{Strategy: backend.StrategySingle}
	if v, ok, _ := actionDict.Get(starlark.String("target")); ok {
		decision.Target = string(v.(starlark.String))
	}
	if v, ok, _ := actionDict.Get(starlark.String("backend")); ok {
		decision.BackendName = string(v.(starlark.String))
	}
	if v, ok, _ := actionDict.Get(starlark.String("model")); ok {
		decision.Model = string(v.(starlark.String))
	}
	if v, ok, _ := actionDict.Get(starlark.String("adapter")); ok {
		decision.Adapter = string(v.(starlark.String))
	}
	if v, ok, _ := actionDict.Get(starlark.String("strategy")); ok {
		decision.Strategy = backend.ExecutionStrategy(string(v.(starlark.String)))
	}

	return &StarlarkResult{Matched: true, Action: decision}, nil
}

func isStepLimitError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "cancelled") ||
		strings.Contains(msg, "max steps") ||
		strings.Contains(msg, "step limit")
}
