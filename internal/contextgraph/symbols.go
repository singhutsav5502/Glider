package contextgraph

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// SymbolIndexer extracts symbols + call edges into the entity store.
// Default implementation is pragmatic (go/parser + regex for JS/TS/Python).
// Swap with a tree-sitter-backed indexer later without changing IndexSymbols callers.
type SymbolIndexer interface {
	Index(turnID, root string, maxFiles int) (int, error)
}

// IndexSymbols walks root and records EXTRACTED symbol entities + defines/calls edges.
// Languages: Go (go/parser), JavaScript/TypeScript and Python (regex). Cap file count.
func (s *Store) IndexSymbols(turnID, root string, maxFiles int) (int, error) {
	return s.IndexSymbolsWith(turnID, root, maxFiles, nil)
}

// IndexSymbolsWith uses idx when non-nil; otherwise the built-in pragmatic indexer.
func (s *Store) IndexSymbolsWith(turnID, root string, maxFiles int, idx SymbolIndexer) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("contextgraph: nil store")
	}
	if idx != nil {
		return idx.Index(turnID, root, maxFiles)
	}
	return (&PragmaticSymbolIndexer{Store: s}).Index(turnID, root, maxFiles)
}

// PragmaticSymbolIndexer is the default EXTRACTED symbol ingest (tree-sitter-ready interface).
type PragmaticSymbolIndexer struct {
	Store *Store
}

func (p *PragmaticSymbolIndexer) Index(turnID, root string, maxFiles int) (int, error) {
	if p == nil || p.Store == nil {
		return 0, fmt.Errorf("contextgraph: nil indexer")
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
	if maxFiles <= 0 {
		maxFiles = 100
	}
	if maxFiles > 500 {
		maxFiles = 500
	}

	skipNames := map[string]bool{
		".git": true, "node_modules": true, "vendor": true, ".glider": true,
		"dist": true, "build": true, "__pycache__": true, ".venv": true,
	}
	n := 0
	err = filepath.WalkDir(abs, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			if d != nil && d.IsDir() && skipNames[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if n >= maxFiles {
			return fs.SkipAll
		}
		ext := strings.ToLower(filepath.Ext(d.Name()))
		var count int
		var perr error
		switch ext {
		case ".go":
			count, perr = p.indexGoFile(turnID, abs, path)
		case ".js", ".jsx", ".ts", ".tsx", ".mjs", ".cjs":
			count, perr = p.indexJSFile(turnID, abs, path)
		case ".py":
			count, perr = p.indexRegexFile(turnID, abs, path, "py", rePyDef, rePyCall)
		default:
			return nil
		}
		if perr != nil {
			return nil // skip unreadable / parse errors
		}
		n += count
		return nil
	})
	if err == fs.SkipAll {
		err = nil
	}
	return n, err
}

var (
	reJSFuncDecl = regexp.MustCompile(`(?m)(?:export\s+)?(?:async\s+)?function\s+(\w+)`)
	reJSFuncExpr = regexp.MustCompile(`(?m)(?:export\s+)?(?:const|let|var)\s+(\w+)\s*=\s*(?:async\s*)?\(`)
	reJSCall     = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]{1,64})\s*\(`)
	rePyDef      = regexp.MustCompile(`(?m)^\s*(?:async\s+)?def\s+(\w+)\s*\(`)
	rePyCall     = regexp.MustCompile(`\b([A-Za-z_][A-Za-z0-9_]{1,64})\s*\(`)
)

func (p *PragmaticSymbolIndexer) indexGoFile(turnID, root, path string) (int, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return 0, err
	}
	fileID := ensureFileEntity(p.Store, turnID, root, path)
	n := 0
	defined := map[string]string{} // name -> symbol id
	var calls [][2]string          // fromSym, toName

	ast.Inspect(f, func(node ast.Node) bool {
		switch x := node.(type) {
		case *ast.FuncDecl:
			if x.Name == nil {
				return true
			}
			name := x.Name.Name
			recv := ""
			if x.Recv != nil && len(x.Recv.List) > 0 {
				recv = exprString(x.Recv.List[0].Type)
			}
			label := name
			if recv != "" {
				label = recv + "." + name
			}
			sid := recordSymbol(p.Store, turnID, fileID, path, label, "func", "go")
			defined[name] = sid
			if recv != "" {
				defined[label] = sid
			}
			n++
			ast.Inspect(x.Body, func(bn ast.Node) bool {
				ce, ok := bn.(*ast.CallExpr)
				if !ok {
					return true
				}
				if cn := callName(ce.Fun); cn != "" {
					calls = append(calls, [2]string{sid, cn})
				}
				return true
			})
		case *ast.GenDecl:
			if x.Tok != token.TYPE {
				return true
			}
			for _, spec := range x.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok || ts.Name == nil {
					continue
				}
				sid := recordSymbol(p.Store, turnID, fileID, path, ts.Name.Name, "type", "go")
				defined[ts.Name.Name] = sid
				n++
			}
		}
		return true
	})

	for _, c := range calls {
		toID, ok := defined[c[1]]
		if !ok {
			// External / unresolved call — still record a lightweight target symbol.
			toID = "sym:ext:" + c[1]
			p.Store.RecordFact(turnID, Fact{
				ID:         toID,
				Kind:       KindSymbol,
				Label:      c[1],
				Provenance: ProvenanceExtracted,
				Attrs:      map[string]string{"name": c[1], "kind": "ext", "lang": "go"},
			})
		}
		if c[0] == toID {
			continue
		}
		p.Store.RecordEdge(turnID, c[0]+"-calls-"+toID, c[0], toID, RelCalls, ProvenanceExtracted, nil)
		n++
	}
	return n, nil
}

func (p *PragmaticSymbolIndexer) indexJSFile(turnID, root, path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(data) > 512*1024 {
		return 0, nil
	}
	text := string(data)
	fileID := ensureFileEntity(p.Store, turnID, root, path)
	n := 0
	defined := map[string]string{}
	addDef := func(name string) {
		if name == "" || len(name) < 2 {
			return
		}
		if _, ok := defined[name]; ok {
			return
		}
		sid := recordSymbol(p.Store, turnID, fileID, path, name, "func", "js")
		defined[name] = sid
		n++
	}
	for _, m := range reJSFuncDecl.FindAllStringSubmatch(text, 80) {
		if len(m) >= 2 {
			addDef(m[1])
		}
	}
	for _, m := range reJSFuncExpr.FindAllStringSubmatch(text, 80) {
		if len(m) >= 2 {
			addDef(m[1])
		}
	}
	for fromName, fromID := range defined {
		for _, m := range reJSCall.FindAllStringSubmatch(text, 200) {
			if len(m) < 2 {
				continue
			}
			toName := m[1]
			if toName == fromName {
				continue
			}
			toID, ok := defined[toName]
			if !ok {
				continue
			}
			p.Store.RecordEdge(turnID, fromID+"-calls-"+toID, fromID, toID, RelCalls, ProvenanceExtracted, nil)
			n++
		}
	}
	return n, nil
}

func (p *PragmaticSymbolIndexer) indexRegexFile(turnID, root, path, lang string, defRe, callRe *regexp.Regexp) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	if len(data) > 512*1024 {
		return 0, nil
	}
	text := string(data)
	fileID := ensureFileEntity(p.Store, turnID, root, path)
	n := 0
	defined := map[string]string{}
	for _, m := range defRe.FindAllStringSubmatch(text, 80) {
		name := ""
		for i := 1; i < len(m); i++ {
			if m[i] != "" {
				name = m[i]
				break
			}
		}
		if name == "" || len(name) < 2 {
			continue
		}
		sid := recordSymbol(p.Store, turnID, fileID, path, name, "func", lang)
		defined[name] = sid
		n++
	}
	// Call edges only among defined symbols in the same file (keeps graph dense, not noisy).
	for fromName, fromID := range defined {
		for _, m := range callRe.FindAllStringSubmatch(text, 200) {
			if len(m) < 2 {
				continue
			}
			toName := m[1]
			if toName == fromName {
				continue
			}
			toID, ok := defined[toName]
			if !ok {
				continue
			}
			p.Store.RecordEdge(turnID, fromID+"-calls-"+toID, fromID, toID, RelCalls, ProvenanceExtracted, nil)
			n++
		}
	}
	return n, nil
}

func ensureFileEntity(s *Store, turnID, root, path string) string {
	id := "file:" + filepath.ToSlash(path)
	rel, _ := filepath.Rel(root, path)
	s.RecordFact(turnID, Fact{
		ID:         id,
		Kind:       KindFile,
		Label:      filepath.Base(path),
		Provenance: ProvenanceExtracted,
		Attrs: map[string]string{
			"path": filepath.ToSlash(path),
			"rel":  filepath.ToSlash(rel),
			"ext":  strings.ToLower(filepath.Ext(path)),
		},
	})
	return id
}

func recordSymbol(s *Store, turnID, fileID, path, label, kind, lang string) string {
	sid := "sym:" + filepath.ToSlash(path) + "#" + label
	s.RecordFact(turnID, Fact{
		ID:         sid,
		Kind:       KindSymbol,
		Label:      label,
		Provenance: ProvenanceExtracted,
		Attrs: map[string]string{
			"name": label,
			"kind": kind,
			"lang": lang,
			"file": filepath.ToSlash(path),
		},
	})
	s.RecordEdge(turnID, fileID+"-defines-"+sid, fileID, sid, RelDefines, ProvenanceExtracted, nil)
	return sid
}

func callName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if x.Sel != nil {
			return x.Sel.Name
		}
	}
	return ""
}

func exprString(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return exprString(x.X)
	case *ast.IndexExpr:
		return exprString(x.X)
	}
	return ""
}
