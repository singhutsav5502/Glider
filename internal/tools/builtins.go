package tools

import (
	"fmt"
	"path/filepath"
	"strings"
)

// StandardBuiltins returns the core agent tool set scoped to opts.Workspace.
func StandardBuiltins(opts Options) []Builtin {
	root := opts.Workspace
	webCfg := opts.WebSearch.normalized()
	return []Builtin{
		&fsRead{root: root},
		&fsWrite{root: root},
		&fsList{root: root},
		&fsSearch{root: root},
		&codeGrep{root: root},
		&shellExec{root: root, allow: opts.AllowShell, allowList: opts.ShellAllow},
		&httpFetch{allowHosts: opts.AllowHosts},
		&webSearchTool{cfg: webCfg},
		&webFetchTool{allowHosts: opts.AllowHosts, maxBytes: webCfg.FetchMaxBytes},
		&gitStatus{root: root},
		&gitDiff{root: root},
		&gitLog{root: root},
		&gitClone{root: root}, // Registry rebinds with SetRunID-aware clone in NewRegistry
		&contextQuery{store: opts.Context},
		&datetimeTool{},
		&calculatorTool{},
	}
}

// --- Shared helpers used across builtin implementations ---

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

func firstField(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\n\t "); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// looksLikeProsePath detects free-form goal/prompt text mistakenly used as a filesystem path.
func looksLikeProsePath(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	words := strings.Fields(path)
	if len(words) >= 6 {
		return true
	}
	if strings.Contains(path, " ") && len(path) > 48 {
		return true
	}
	return false
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
