package contextgraph_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/contextgraph"
)

func TestIndexSymbolsGo(t *testing.T) {
	root := t.TempDir()
	src := `package demo

func Helper() int { return 1 }

func Main() {
	_ = Helper()
}
`
	_ = os.WriteFile(filepath.Join(root, "demo.go"), []byte(src), 0o644)
	s := contextgraph.New(t.TempDir())
	n, err := s.IndexSymbols("sym-turn", root, 50)
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("indexed=%d", n)
	}
	q := s.Query("sym-turn", "Helper", 30)
	if !strings.Contains(q, "Helper") {
		t.Fatalf("%s", q)
	}
	var sawSym, sawCalls bool
	for _, e := range s.Entities("sym-turn", 100) {
		if e.Kind == contextgraph.KindSymbol {
			sawSym = true
		}
		if e.Kind == contextgraph.KindEdge && e.Relation == contextgraph.RelCalls {
			sawCalls = true
		}
	}
	if !sawSym || !sawCalls {
		t.Fatalf("sym=%v calls=%v", sawSym, sawCalls)
	}
}

func TestIndexSymbolsPython(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "mod.py"), []byte("def foo():\n    bar()\ndef bar():\n    pass\n"), 0o644)
	s := contextgraph.New(t.TempDir())
	n, err := s.IndexSymbols("py", root, 20)
	if err != nil {
		t.Fatal(err)
	}
	if n < 2 {
		t.Fatalf("indexed=%d", n)
	}
}

func TestCommunitiesAndExplain(t *testing.T) {
	s := contextgraph.New(t.TempDir())
	s.RecordFact("c1", contextgraph.Fact{ID: "a", Kind: contextgraph.KindEntity, Label: "A", Provenance: contextgraph.ProvenanceRuntime})
	s.RecordFact("c1", contextgraph.Fact{ID: "b", Kind: contextgraph.KindEntity, Label: "B", Provenance: contextgraph.ProvenanceRuntime})
	s.RecordFact("c1", contextgraph.Fact{ID: "c", Kind: contextgraph.KindEntity, Label: "C", Provenance: contextgraph.ProvenanceRuntime})
	s.RecordEdge("c1", "e1", "a", "b", contextgraph.RelFollows, contextgraph.ProvenanceRuntime, nil)
	s.RecordEdge("c1", "e2", "b", "c", contextgraph.RelFollows, contextgraph.ProvenanceRuntime, nil)

	coms := s.DetectCommunities("c1", 5)
	if len(coms) < 1 || coms[0].Size < 3 {
		t.Fatalf("%+v", coms)
	}
	hubs := s.GodNodes("c1", 3)
	if len(hubs) == 0 || !strings.Contains(hubs[0], "b") {
		t.Fatalf("hubs=%v", hubs)
	}
	ex := s.Explain("c1", "b")
	if !strings.Contains(ex, "explain:") || !strings.Contains(ex, "neighbors") {
		t.Fatalf("%s", ex)
	}
	opts := contextgraph.ParseQueryInput("c1 explain=b communities=1")
	if opts.Explain != "b" || !opts.Communities {
		t.Fatalf("%+v", opts)
	}
	q := s.QueryWith(opts)
	if !strings.Contains(q, "explain:") {
		t.Fatalf("%s", q)
	}
}
