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
		&artifactWrite{root: root},
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

// resolveToolPath scopes rel under the active run layout (ctx) when present.
// Bare paths → work; kind=out → out. Without a layout, joins under root only.
func resolveToolPath(ctx context.Context, root, rel string, kind ArtifactKind) (string, error) {
	if l, ok := RunLayoutFrom(ctx); ok {
		return l.ResolveAbs(rel, kind)
	}
	if root == "" {
		root = DefaultWorkspaceDir()
	}
	return safeJoin(root, rel)
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
func (t *fsRead) Call(ctx context.Context, input string, _ json.RawMessage) (Result, error) {
	path, err := resolveToolPath(ctx, t.root, firstField(input), RelWork)
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
func (t *fsWrite) Call(ctx context.Context, input string, _ json.RawMessage) (Result, error) {
	pathLine, content, _ := strings.Cut(input, "\n")
	path, err := resolveToolPath(ctx, t.root, strings.TrimSpace(pathLine), RelWork)
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
func (t *fsList) Call(ctx context.Context, input string, _ json.RawMessage) (Result, error) {
	path, err := resolveToolPath(ctx, t.root, firstField(input), RelWork)
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

type artifactWrite struct{ root string }

func (t *artifactWrite) Name() string { return "artifact_write" }
func (t *artifactWrite) Description() string {
	return "Write a file under run work (default) or out (kind=out). Input: path\\ncontent, or JSON {path,content,kind}."
}
func (t *artifactWrite) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"},"kind":{"type":"string","enum":["work","out"]}},"required":["path","content"]}`)
}
func (t *artifactWrite) Call(ctx context.Context, input string, args json.RawMessage) (Result, error) {
	pathStr, content, kind := "", "", RelWork
	parseObj := func(raw []byte) bool {
		var m struct {
			Path    string `json:"path"`
			Content string `json:"content"`
			Kind    string `json:"kind"`
		}
		if err := json.Unmarshal(raw, &m); err != nil || strings.TrimSpace(m.Path) == "" {
			return false
		}
		pathStr, content = m.Path, m.Content
		if strings.TrimSpace(m.Kind) != "" {
			kind = ArtifactKind(strings.ToLower(strings.TrimSpace(m.Kind)))
		}
		return true
	}
	if len(args) > 0 && string(args) != "null" && parseObj(args) {
		// ok
	} else if strings.TrimSpace(input) != "" && strings.HasPrefix(strings.TrimSpace(input), "{") && parseObj([]byte(input)) {
		// JSON in input string (agent loop)
	} else {
		pathLine, body, _ := strings.Cut(input, "\n")
		pathStr = strings.TrimSpace(pathLine)
		content = body
		// Optional first-line: kind=out path=foo.md
		if strings.Contains(pathStr, "kind=") || strings.HasPrefix(strings.ToLower(pathStr), "out ") {
			fields := strings.Fields(pathStr)
			var pathParts []string
			for _, f := range fields {
				low := strings.ToLower(f)
				if strings.HasPrefix(low, "kind=") {
					kind = ArtifactKind(strings.TrimPrefix(low, "kind="))
					continue
				}
				if low == "out" {
					kind = RelOut
					continue
				}
				if strings.HasPrefix(low, "path=") {
					pathParts = append(pathParts, strings.TrimPrefix(f, "path="))
					continue
				}
				pathParts = append(pathParts, f)
			}
			pathStr = strings.Join(pathParts, " ")
		}
	}
	if strings.TrimSpace(pathStr) == "" {
		return fail(t.Name(), fmt.Errorf("path required"))
	}
	if kind != RelOut && kind != RelWork {
		if kind == "output" {
			kind = RelOut
		} else {
			kind = RelWork
		}
	}
	path, err := resolveToolPath(ctx, t.root, pathStr, kind)
	if err != nil {
		return fail(t.Name(), err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fail(t.Name(), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fail(t.Name(), err)
	}
	return ok(t.Name(), fmt.Sprintf("wrote %s (%s)", path, kind)), nil
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
	dir := t.root
	if work, err := resolveToolPath(ctx, t.root, ".", RelWork); err == nil {
		dir = work
	}
	if dir != "" {
		c.Dir = dir
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
	dest, err := resolveToolPath(ctx, t.root, dir, RelWork)
	if err != nil {
		return fail(t.Name(), err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
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

func (t *contextQuery) Name() string { return "context_query" }
func (t *contextQuery) Description() string {
	return "Query dual-layer contextgraph. Input: turn_id [prov=RUNTIME|EXTRACTED|INFERRED] [path=from->to] [neigh=id] keyword"
}
func (t *contextQuery) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"turn_id":{"type":"string"},"query":{"type":"string"},"prov":{"type":"string"},"path":{"type":"string"},"neigh":{"type":"string"}}}`)
}
func (t *contextQuery) Call(_ context.Context, input string, args json.RawMessage) (Result, error) {
	if t.store == nil {
		return Result{Name: t.Name(), Kind: KindBuiltin, OK: true, Stubbed: true, Output: "no context store"}, nil
	}
	input = strings.TrimSpace(input)
	// Prefer structured args when present.
	if len(args) > 0 && string(args) != "null" {
		var m map[string]any
		if json.Unmarshal(args, &m) == nil {
			var parts []string
			if v, ok := m["turn_id"].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, strings.TrimSpace(v))
			}
			if v, ok := m["prov"].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, "prov="+strings.TrimSpace(v))
			}
			if v, ok := m["path"].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, "path="+strings.TrimSpace(v))
			}
			if v, ok := m["neigh"].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, "neigh="+strings.TrimSpace(v))
			}
			if v, ok := m["query"].(string); ok && strings.TrimSpace(v) != "" {
				parts = append(parts, strings.TrimSpace(v))
			}
			if len(parts) > 0 {
				input = strings.Join(parts, " ")
			}
		}
	}
	if raw, okStore := t.store.(interface{ QueryRaw(string) string }); okStore {
		return ok(t.Name(), raw.QueryRaw(input)), nil
	}
	// Fallback: simple turn_id + keyword.
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
