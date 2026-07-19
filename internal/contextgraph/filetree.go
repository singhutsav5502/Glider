package contextgraph

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// IndexFileTree walks root and records EXTRACTED dir/file entities + contains edges.
// Lightweight Graphify-adjacent ingest (no tree-sitter). Caps depth and file count.
func (s *Store) IndexFileTree(turnID, root string, maxDepth, maxFiles int) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("contextgraph: nil store")
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return 0, fmt.Errorf("contextgraph: empty root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return 0, err
	}
	if !info.IsDir() {
		return 0, fmt.Errorf("contextgraph: not a directory: %s", abs)
	}
	if maxDepth <= 0 {
		maxDepth = 4
	}
	if maxDepth > 8 {
		maxDepth = 8
	}
	if maxFiles <= 0 {
		maxFiles = 200
	}
	if maxFiles > 2000 {
		maxFiles = 2000
	}

	rootID := "dir:" + filepath.ToSlash(abs)
	s.RecordFact(turnID, Fact{
		ID:         rootID,
		Kind:       KindDir,
		Label:      filepath.Base(abs),
		Provenance: ProvenanceExtracted,
		Attrs: map[string]string{
			"path": filepath.ToSlash(abs),
			"root": "true",
		},
	})
	n := 1
	skipNames := map[string]bool{
		".git": true, "node_modules": true, "vendor": true, ".glider": true,
		"dist": true, "build": true, "__pycache__": true, ".venv": true,
	}

	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == abs {
			return nil
		}
		rel, _ := filepath.Rel(abs, path)
		rel = filepath.ToSlash(rel)
		depth := strings.Count(rel, "/") + 1
		name := d.Name()
		if d.IsDir() && skipNames[name] {
			return fs.SkipDir
		}
		if depth > maxDepth {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if n >= maxFiles {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		parent := filepath.Dir(path)
		parentID := "dir:" + filepath.ToSlash(parent)
		if parent == abs {
			parentID = rootID
		}
		if d.IsDir() {
			id := "dir:" + filepath.ToSlash(path)
			s.RecordFact(turnID, Fact{
				ID:         id,
				Kind:       KindDir,
				Label:      name,
				Provenance: ProvenanceExtracted,
				Attrs:      map[string]string{"path": filepath.ToSlash(path), "rel": rel},
			})
			s.RecordEdge(turnID, parentID+"-contains-"+id, parentID, id, RelContains, ProvenanceExtracted, nil)
			n++
			return nil
		}
		id := "file:" + filepath.ToSlash(path)
		s.RecordFact(turnID, Fact{
			ID:         id,
			Kind:       KindFile,
			Label:      name,
			Provenance: ProvenanceExtracted,
			Attrs: map[string]string{
				"path": filepath.ToSlash(path),
				"rel":  rel,
				"ext":  strings.ToLower(filepath.Ext(name)),
			},
		})
		s.RecordEdge(turnID, parentID+"-contains-"+id, parentID, id, RelContains, ProvenanceExtracted, nil)
		n++
		return nil
	})
	return n, err
}
