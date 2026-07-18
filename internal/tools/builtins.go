package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// StandardBuiltins returns the core agent tool set scoped to opts.Workspace.
func StandardBuiltins(opts Options) []Builtin {
	root := opts.Workspace
	return []Builtin{
		&fsRead{root: root},
		&fsWrite{root: root},
		&fsList{root: root},
		&fsSearch{root: root},
		&codeGrep{root: root},
		&shellExec{root: root, allow: opts.AllowShell, allowList: opts.ShellAllow},
		&httpFetch{allowHosts: opts.AllowHosts},
		&gitStatus{root: root},
		&gitDiff{root: root},
		&gitLog{root: root},
		&gitClone{root: root},
		&contextQuery{store: opts.Context},
		&datetimeTool{},
		&calculatorTool{},
	}
}

func safeJoin(root, rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		rel = "."
	}
	if root == "" {
		return filepath.Clean(rel), nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(absRoot, filepath.Clean("/"+rel))
	// Prevent escape: ensure joined is under absRoot.
	relOut, err := filepath.Rel(absRoot, joined)
	if err != nil || strings.HasPrefix(relOut, "..") {
		return "", fmt.Errorf("path escapes workspace")
	}
	return joined, nil
}

type fsRead struct{ root string }

func (t *fsRead) Name() string        { return "fs_read" }
func (t *fsRead) Description() string { return "Read a UTF-8 text file under the workspace" }
func (t *fsRead) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
}
func (t *fsRead) Call(_ context.Context, input string, _ json.RawMessage) (Result, error) {
	path, err := safeJoin(t.root, firstField(input))
	if err != nil {
		return fail(t.Name(), err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fail(t.Name(), err)
	}
	if len(b) > 64<<10 {
		b = append(b[:64<<10], []byte("\n...truncated")...)
	}
	return ok(t.Name(), string(b)), nil
}

type fsWrite struct{ root string }

func (t *fsWrite) Name() string        { return "fs_write" }
func (t *fsWrite) Description() string { return "Write a text file under the workspace (path\\ncontent)" }
func (t *fsWrite) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`)
}
func (t *fsWrite) Call(_ context.Context, input string, _ json.RawMessage) (Result, error) {
	pathLine, content, _ := strings.Cut(input, "\n")
	path, err := safeJoin(t.root, strings.TrimSpace(pathLine))
	if err != nil {
		return fail(t.Name(), err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fail(t.Name(), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fail(t.Name(), err)
	}
	return ok(t.Name(), "wrote "+path), nil
}

type fsList struct{ root string }

func (t *fsList) Name() string        { return "fs_list" }
func (t *fsList) Description() string { return "List directory entries under the workspace" }
func (t *fsList) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
}
func (t *fsList) Call(_ context.Context, input string, _ json.RawMessage) (Result, error) {
	path, err := safeJoin(t.root, firstField(input))
	if err != nil {
		return fail(t.Name(), err)
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		return fail(t.Name(), err)
	}
	var b strings.Builder
	for i, e := range ents {
		if i >= 200 {
			b.WriteString("...truncated\n")
			break
		}
		suffix := ""
		if e.IsDir() {
			suffix = "/"
		}
		b.WriteString(e.Name() + suffix + "\n")
	}
	return ok(t.Name(), b.String()), nil
}

type fsSearch struct{ root string }

func (t *fsSearch) Name() string        { return "fs_search" }
func (t *fsSearch) Description() string { return "Find files by name glob under workspace (pattern)" }
func (t *fsSearch) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"}},"required":["pattern"]}`)
}
func (t *fsSearch) Call(_ context.Context, input string, _ json.RawMessage) (Result, error) {
	pat := firstField(input)
	if pat == "" {
		return fail(t.Name(), fmt.Errorf("pattern required"))
	}
	root := t.root
	if root == "" {
		root = "."
	}
	var matches []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		okMatch, _ := filepath.Match(pat, base)
		if okMatch {
			rel, _ := filepath.Rel(root, path)
			matches = append(matches, rel)
			if len(matches) >= 100 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return ok(t.Name(), strings.Join(matches, "\n")), nil
}

type codeGrep struct{ root string }

func (t *codeGrep) Name() string        { return "code_grep" }
func (t *codeGrep) Description() string { return "Search file contents for a substring (scoped)" }
func (t *codeGrep) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
}
func (t *codeGrep) Call(_ context.Context, input string, _ json.RawMessage) (Result, error) {
	q := strings.TrimSpace(firstField(input))
	if q == "" {
		return fail(t.Name(), fmt.Errorf("query required"))
	}
	root := t.root
	if root == "" {
		root = "."
	}
	var hits []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		switch ext {
		case ".go", ".js", ".ts", ".tsx", ".py", ".md", ".yaml", ".yml", ".json", ".html", ".css", ".rs", ".java":
		default:
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil || len(b) > 1<<20 {
			return nil
		}
		if !bytes.Contains(b, []byte(q)) {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		hits = append(hits, rel)
		if len(hits) >= 50 {
			return filepath.SkipAll
		}
		return nil
	})
	return ok(t.Name(), strings.Join(hits, "\n")), nil
}

type shellExec struct {
	root      string
	allow     bool
	allowList []string
}

func (t *shellExec) Name() string        { return "shell_exec" }
func (t *shellExec) Description() string { return "Run an allowlisted shell command in the workspace" }
func (t *shellExec) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`)
}
func (t *shellExec) Call(ctx context.Context, input string, _ json.RawMessage) (Result, error) {
	if !t.allow {
		return Result{Name: t.Name(), Kind: KindBuiltin, OK: false, Err: "shell_exec disabled"}, fmt.Errorf("shell disabled")
	}
	line := strings.TrimSpace(input)
	if line == "" {
		return fail(t.Name(), fmt.Errorf("command required"))
	}
	parts := strings.Fields(line)
	cmdName := parts[0]
	if len(t.allowList) > 0 && !containsFold(t.allowList, cmdName) {
		return fail(t.Name(), fmt.Errorf("command %q not allowlisted", cmdName))
	}
	c := exec.CommandContext(ctx, parts[0], parts[1:]...)
	if t.root != "" {
		c.Dir = t.root
	}
	out, err := c.CombinedOutput()
	text := string(out)
	if len(text) > 32<<10 {
		text = text[:32<<10] + "\n...truncated"
	}
	if err != nil {
		return Result{Name: t.Name(), Kind: KindBuiltin, OK: false, Output: text, Err: err.Error()}, nil
	}
	return ok(t.Name(), text), nil
}

type httpFetch struct{ allowHosts []string }

func (t *httpFetch) Name() string        { return "http_fetch" }
func (t *httpFetch) Description() string { return "HTTP GET a URL (optional host allowlist)" }
func (t *httpFetch) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`)
}
func (t *httpFetch) Call(ctx context.Context, input string, _ json.RawMessage) (Result, error) {
	u := strings.TrimSpace(firstField(input))
	if u == "" || !strings.HasPrefix(u, "http") {
		return fail(t.Name(), fmt.Errorf("url required"))
	}
	if len(t.allowHosts) > 0 {
		okHost := false
		for _, h := range t.allowHosts {
			if strings.Contains(u, h) {
				okHost = true
				break
			}
		}
		if !okHost {
			return fail(t.Name(), fmt.Errorf("host not allowlisted"))
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return fail(t.Name(), err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fail(t.Name(), err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return ok(t.Name(), fmt.Sprintf("status=%d\n%s", resp.StatusCode, string(b))), nil
}

type gitStatus struct{ root string }

func (t *gitStatus) Name() string        { return "git_status" }
func (t *gitStatus) Description() string { return "git status --short in workspace" }
func (t *gitStatus) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *gitStatus) Call(ctx context.Context, _ string, _ json.RawMessage) (Result, error) {
	return runGit(ctx, t.Name(), t.root, "status", "--short")
}

type gitDiff struct{ root string }

func (t *gitDiff) Name() string        { return "git_diff" }
func (t *gitDiff) Description() string { return "git diff (optionally --stat)" }
func (t *gitDiff) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"stat":{"type":"boolean"}}}`)
}
func (t *gitDiff) Call(ctx context.Context, input string, _ json.RawMessage) (Result, error) {
	args := []string{"diff"}
	if strings.Contains(input, "stat") {
		args = append(args, "--stat")
	}
	return runGit(ctx, t.Name(), t.root, args...)
}

type gitLog struct{ root string }

func (t *gitLog) Name() string        { return "git_log" }
func (t *gitLog) Description() string { return "git log -n 20 --oneline" }
func (t *gitLog) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"n":{"type":"integer"}}}`)
}
func (t *gitLog) Call(ctx context.Context, input string, _ json.RawMessage) (Result, error) {
	n := "20"
	if v := strings.TrimSpace(input); v != "" {
		if _, err := strconv.Atoi(v); err == nil {
			n = v
		}
	}
	return runGit(ctx, t.Name(), t.root, "log", "-n", n, "--oneline")
}

type gitClone struct{ root string }

func (t *gitClone) Name() string        { return "git_clone" }
func (t *gitClone) Description() string { return "git clone URL into workspace subdirectory" }
func (t *gitClone) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"url":{"type":"string"},"dir":{"type":"string"}},"required":["url"]}`)
}
func (t *gitClone) Call(ctx context.Context, input string, _ json.RawMessage) (Result, error) {
	fields := strings.Fields(strings.TrimSpace(input))
	if len(fields) == 0 {
		return fail(t.Name(), fmt.Errorf("url required"))
	}
	url := fields[0]
	dir := "repo"
	if len(fields) > 1 {
		dir = fields[1]
	}
	dest, err := safeJoin(t.root, dir)
	if err != nil {
		return fail(t.Name(), err)
	}
	c := exec.CommandContext(ctx, "git", "clone", "--depth", "1", url, dest)
	out, err := c.CombinedOutput()
	text := string(out)
	if err != nil {
		return Result{Name: t.Name(), Kind: KindBuiltin, OK: false, Output: text, Err: err.Error()}, nil
	}
	return ok(t.Name(), text+"\ncloned to "+dest), nil
}

type contextQuery struct{ store ContextStore }

func (t *contextQuery) Name() string        { return "context_query" }
func (t *contextQuery) Description() string { return "Query shared contextgraph for a turn/session" }
func (t *contextQuery) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"turn_id":{"type":"string"},"query":{"type":"string"}}}`)
}
func (t *contextQuery) Call(_ context.Context, input string, _ json.RawMessage) (Result, error) {
	if t.store == nil {
		return Result{Name: t.Name(), Kind: KindBuiltin, OK: true, Stubbed: true, Output: "no context store"}, nil
	}
	turnID, q, _ := strings.Cut(input, " ")
	turnID = strings.TrimSpace(turnID)
	q = strings.TrimSpace(q)
	if q == "" {
		q = turnID
		turnID = ""
	}
	return ok(t.Name(), t.store.Query(turnID, q, 20)), nil
}

type datetimeTool struct{}

func (t *datetimeTool) Name() string        { return "datetime" }
func (t *datetimeTool) Description() string { return "Current UTC time RFC3339" }
func (t *datetimeTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (t *datetimeTool) Call(context.Context, string, json.RawMessage) (Result, error) {
	return ok(t.Name(), time.Now().UTC().Format(time.RFC3339)), nil
}

type calculatorTool struct{}

func (t *calculatorTool) Name() string        { return "calculator" }
func (t *calculatorTool) Description() string { return "Evaluate simple a+b a-b a*b a/b expressions" }
func (t *calculatorTool) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"expr":{"type":"string"}}}`)
}
func (t *calculatorTool) Call(_ context.Context, input string, _ json.RawMessage) (Result, error) {
	expr := strings.ReplaceAll(strings.TrimSpace(input), " ", "")
	for _, op := range []string{"+", "-", "*", "/"} {
		if i := strings.Index(expr, op); i > 0 {
			a, err1 := strconv.ParseFloat(expr[:i], 64)
			b, err2 := strconv.ParseFloat(expr[i+1:], 64)
			if err1 != nil || err2 != nil {
				return fail(t.Name(), fmt.Errorf("parse"))
			}
			var v float64
			switch op {
			case "+":
				v = a + b
			case "-":
				v = a - b
			case "*":
				v = a * b
			case "/":
				if b == 0 {
					return fail(t.Name(), fmt.Errorf("div0"))
				}
				v = a / b
			}
			return ok(t.Name(), strconv.FormatFloat(v, 'f', -1, 64)), nil
		}
	}
	return fail(t.Name(), fmt.Errorf("unsupported expr"))
}

func runGit(ctx context.Context, name, root string, args ...string) (Result, error) {
	c := exec.CommandContext(ctx, "git", args...)
	if root != "" {
		c.Dir = root
	}
	out, err := c.CombinedOutput()
	text := string(out)
	if len(text) > 32<<10 {
		text = text[:32<<10] + "\n...truncated"
	}
	if err != nil {
		return Result{Name: name, Kind: KindBuiltin, OK: false, Output: text, Err: err.Error()}, nil
	}
	return ok(name, text), nil
}

func firstField(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\t "); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

func containsFold(list []string, v string) bool {
	for _, x := range list {
		if strings.EqualFold(x, v) {
			return true
		}
	}
	return false
}

func ok(name, out string) Result {
	return Result{Name: name, Kind: KindBuiltin, OK: true, Output: out}
}

func fail(name string, err error) (Result, error) {
	return Result{Name: name, Kind: KindBuiltin, OK: false, Err: err.Error()}, err
}
