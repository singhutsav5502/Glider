package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// --- Filesystem tools ---

type fsRead struct {
	root string
	reg  *Registry
}

func (t *fsRead) Name() string { return "fs_read" }
func (t *fsRead) Description() string {
	return "Read a UTF-8 text file under the workspace. With an active run, bare paths resolve under runs/<id>/work/ (same as git_clone)."
}
func (t *fsRead) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)
}
func (t *fsRead) Call(_ context.Context, input string, _ json.RawMessage) (Result, error) {
	rel := firstField(input)
	if t.reg != nil {
		rel = t.reg.ScopeRel(rel)
	}
	path, err := safeJoin(t.root, rel)
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

type fsWrite struct {
	root string
	reg  *Registry
}

func (t *fsWrite) Name() string { return "fs_write" }
func (t *fsWrite) Description() string {
	return "Write a UTF-8 text file under the tools workspace. Input: relative/path\\nfile contents. Parents are created. With an active run, bare paths land under runs/<id>/work/. Prefer artifact_write for kind=work|out."
}
func (t *fsWrite) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`)
}
func (t *fsWrite) Call(_ context.Context, input string, _ json.RawMessage) (Result, error) {
	pathLine, content, _ := strings.Cut(input, "\n")
	pathLine = strings.TrimSpace(pathLine)
	if looksLikeProsePath(pathLine) {
		return fail(t.Name(), fmt.Errorf("fs_write needs relative/path then newline content (got prose/goal text as path)"))
	}
	if t.reg != nil {
		pathLine = t.reg.ScopeRel(pathLine)
	}
	path, err := safeJoin(t.root, pathLine)
	if err != nil {
		return fail(t.Name(), err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fail(t.Name(), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fail(t.Name(), err)
	}
	rel := RelFromAbs(t.root, path)
	if rel == "" {
		rel = path
	}
	return ok(t.Name(), "wrote "+rel), nil
}

type fsList struct {
	root string
	reg  *Registry
}

func (t *fsList) Name() string { return "fs_list" }
func (t *fsList) Description() string {
	return "List directory entries under the workspace. With an active run, bare paths (including .) resolve under runs/<id>/work/."
}
func (t *fsList) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
}
func (t *fsList) Call(_ context.Context, input string, _ json.RawMessage) (Result, error) {
	rel := firstField(input)
	// Blind pre-invoke may pass the hoop goal; treat prose as workspace root / run work.
	if looksLikeProsePath(strings.TrimSpace(strings.SplitN(input, "\n", 2)[0])) {
		rel = "."
	}
	if t.reg != nil {
		rel = t.reg.ScopeRel(rel)
	}
	path, err := safeJoin(t.root, rel)
	if err != nil {
		return fail(t.Name(), err)
	}
	ents, err := os.ReadDir(path)
	if err != nil {
		return fail(t.Name(), err)
	}
	var b strings.Builder
	b.WriteString("path: ")
	b.WriteString(filepath.ToSlash(rel))
	b.WriteString("\n")
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

type fsSearch struct {
	root string
	reg  *Registry
}

func (t *fsSearch) Name() string { return "fs_search" }
func (t *fsSearch) Description() string {
	return "Find files by name glob under workspace (pattern). With an active run, searches runs/<id>/work/."
}
func (t *fsSearch) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"}},"required":["pattern"]}`)
}
func (t *fsSearch) Call(_ context.Context, input string, _ json.RawMessage) (Result, error) {
	pat := firstField(input)
	if pat == "" {
		return fail(t.Name(), fmt.Errorf("pattern required"))
	}
	walkRoot, relBase := t.walkRoots()
	var matches []string
	_ = filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == ".glider" {
				return filepath.SkipDir
			}
			return nil
		}
		base := filepath.Base(path)
		okMatch, _ := filepath.Match(pat, base)
		if okMatch {
			rel, _ := filepath.Rel(relBase, path)
			matches = append(matches, filepath.ToSlash(rel))
			if len(matches) >= 100 {
				return filepath.SkipAll
			}
		}
		return nil
	})
	return ok(t.Name(), strings.Join(matches, "\n")), nil
}

func (t *fsSearch) walkRoots() (walkRoot, relBase string) {
	walkRoot = t.root
	if walkRoot == "" {
		walkRoot = "."
	}
	relBase = walkRoot
	if t.reg != nil && t.reg.RunID() != "" {
		if abs, err := safeJoin(t.root, t.reg.ScopeRel(".")); err == nil {
			walkRoot = abs
			relBase = abs
		}
	}
	return walkRoot, relBase
}

// --- Code search ---

type codeGrep struct {
	root string
	reg  *Registry
}

func (t *codeGrep) Name() string { return "code_grep" }
func (t *codeGrep) Description() string {
	return "Search file contents for a substring under the tools workspace (not the Glider source tree unless workspace is .). With an active run, searches runs/<id>/work/."
}
func (t *codeGrep) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}`)
}
func (t *codeGrep) Call(_ context.Context, input string, _ json.RawMessage) (Result, error) {
	q := strings.TrimSpace(firstField(input))
	if q == "" {
		return fail(t.Name(), fmt.Errorf("query required"))
	}
	walkRoot, relBase := t.walkRoots()
	var hits []string
	_ = filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "node_modules" || name == "vendor" || name == ".glider" {
				return filepath.SkipDir
			}
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
		rel, _ := filepath.Rel(relBase, path)
		hits = append(hits, filepath.ToSlash(rel))
		if len(hits) >= 50 {
			return filepath.SkipAll
		}
		return nil
	})
	if len(hits) == 0 {
		return ok(t.Name(), "(no matches under workspace "+walkRoot+")"), nil
	}
	return ok(t.Name(), strings.Join(hits, "\n")), nil
}

func (t *codeGrep) walkRoots() (walkRoot, relBase string) {
	walkRoot = t.root
	if walkRoot == "" {
		walkRoot = "."
	}
	relBase = walkRoot
	if t.reg != nil && t.reg.RunID() != "" {
		if abs, err := safeJoin(t.root, t.reg.ScopeRel(".")); err == nil {
			walkRoot = abs
			relBase = abs
		}
	}
	return walkRoot, relBase
}
