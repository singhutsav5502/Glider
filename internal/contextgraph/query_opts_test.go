package contextgraph_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/contextgraph"
)

func TestQueryWithFilters(t *testing.T) {
	dir := t.TempDir()
	s := contextgraph.New(dir)
	s.RecordFact("t1", contextgraph.Fact{
		ID: "w0", Kind: contextgraph.KindWave, Label: "wave 0",
		Provenance: contextgraph.ProvenanceRuntime,
		Attrs:      map[string]string{"wave": "0"},
	})
	s.RecordFact("t1", contextgraph.Fact{
		ID: "f1", Kind: contextgraph.KindFile, Label: "main.go",
		Provenance: contextgraph.ProvenanceExtracted,
		Attrs:      map[string]string{"path": "cmd/main.go"},
	})
	s.RecordEdge("t1", "e1", "w0", "f1", contextgraph.RelSeeds, contextgraph.ProvenanceInferred, nil)

	q := s.QueryWith(contextgraph.QueryOpts{TurnID: "t1", Provenance: contextgraph.ProvenanceExtracted, Limit: 10})
	if !strings.Contains(q, "EXTRACTED") || !strings.Contains(q, "main.go") {
		t.Fatalf("prov filter: %s", q)
	}
	if strings.Contains(q, "wave 0") && strings.Contains(q, "[entity:RUNTIME]") {
		// RUNTIME should be excluded when filtering EXTRACTED
		t.Fatalf("unexpected runtime entity: %s", q)
	}
	path := s.QueryWith(contextgraph.QueryOpts{TurnID: "t1", From: "w0", To: "f1", Limit: 10})
	if !strings.Contains(path, "path") && !strings.Contains(path, "seeds") {
		t.Fatalf("path: %s", path)
	}
	neigh := s.QueryWith(contextgraph.QueryOpts{TurnID: "t1", Neighborhood: "w0", Limit: 10})
	if !strings.Contains(neigh, "neigh") {
		t.Fatalf("neigh: %s", neigh)
	}
	opts := contextgraph.ParseQueryInput("t1 prov=EXTRACTED main")
	if opts.TurnID != "t1" || opts.Provenance != contextgraph.ProvenanceExtracted || opts.Keyword != "main" {
		t.Fatalf("%+v", opts)
	}
}

func TestIndexFileTree(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "pkg"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "pkg", "a.go"), []byte("package pkg"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "README.md"), []byte("# x"), 0o644)

	s := contextgraph.New(t.TempDir())
	n, err := s.IndexFileTree("audit", root, 3, 50)
	if err != nil {
		t.Fatal(err)
	}
	if n < 3 {
		t.Fatalf("indexed=%d", n)
	}
	q := s.Query("audit", "readme", 20)
	if !strings.Contains(strings.ToLower(q), "readme") {
		t.Fatalf("%s", q)
	}
	ents := s.Entities("audit", 50)
	var sawFile, sawDir bool
	for _, e := range ents {
		if e.Kind == contextgraph.KindFile {
			sawFile = true
		}
		if e.Kind == contextgraph.KindDir {
			sawDir = true
		}
	}
	if !sawFile || !sawDir {
		t.Fatalf("kinds file=%v dir=%v", sawFile, sawDir)
	}
}
