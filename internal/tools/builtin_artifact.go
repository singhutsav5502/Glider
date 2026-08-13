package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// artifactWrite writes into the active run's work/ or out/ folder.
type artifactWrite struct {
	reg  *Registry
	root string
}

func (t *artifactWrite) Name() string { return "artifact_write" }
func (t *artifactWrite) Description() string {
	return "Write a run-scoped artifact. Input: work|out relative/path\\ncontent. Lands under runs/<run_id>/work or .../out."
}
func (t *artifactWrite) InputSchema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["work","out"]},"path":{"type":"string"},"content":{"type":"string"}},"required":["kind","path","content"]}`)
}
func (t *artifactWrite) Call(_ context.Context, input string, args json.RawMessage) (Result, error) {
	kind := "out"
	relPath := ""
	content := ""
	if len(args) > 0 && string(args) != "null" {
		var m map[string]any
		if json.Unmarshal(args, &m) == nil {
			if v, ok := m["kind"].(string); ok && v != "" {
				kind = strings.ToLower(strings.TrimSpace(v))
			}
			if v, ok := m["path"].(string); ok {
				relPath = strings.TrimSpace(v)
			}
			if v, ok := m["content"].(string); ok {
				content = v
			}
		}
	}
	if relPath == "" {
		first, rest, _ := strings.Cut(strings.TrimSpace(input), "\n")
		fields := strings.Fields(first)
		if len(fields) >= 2 {
			k := strings.ToLower(fields[0])
			if k == "work" || k == "out" {
				kind = k
				relPath = fields[1]
				content = rest
			} else {
				// Do not treat "Clone a target..." as kind=Clone path=a (blind goal misuse).
				return fail(t.Name(), fmt.Errorf("artifact_write needs 'work|out relative/path' then newline content (or JSON)"))
			}
		} else if len(fields) == 1 {
			relPath = fields[0]
			content = rest
		}
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "work" && kind != "out" {
		kind = "out"
	}
	relPath = strings.TrimSpace(relPath)
	if relPath == "" || looksLikeProsePath(relPath) {
		return fail(t.Name(), fmt.Errorf("path required (work|out relative/path)"))
	}
	root := t.root
	if t.reg != nil && t.reg.Workspace() != "" {
		root = t.reg.Workspace()
	}
	var lay RunLayout
	if t.reg != nil {
		if cur, ok := t.reg.CurrentLayout(); ok {
			lay = cur
		}
	}
	if lay.RelWork == "" {
		runID := "run"
		if t.reg != nil {
			if id := t.reg.RunID(); id != "" {
				runID = id
			}
		}
		lay = LayoutForRun(root, runID)
	}
	_ = lay.Ensure()
	base := lay.RelOut
	if kind == "work" {
		base = lay.RelWork
	}
	joined := filepath.ToSlash(filepath.Join(filepath.FromSlash(base), filepath.FromSlash(relPath)))
	joinRoot := root
	if lay.RootAbs != "" {
		joinRoot = lay.RootAbs
	}
	abs, err := safeJoin(joinRoot, joined)
	if err != nil {
		return fail(t.Name(), err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fail(t.Name(), err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return fail(t.Name(), err)
	}
	rel := RelFromAbs(root, abs)
	return ok(t.Name(), "wrote artifact "+rel+" ("+kind+")"), nil
}
