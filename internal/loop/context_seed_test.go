package loop

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/contextgraph"
	"github.com/glider-ai/glider/internal/tools"
)

func TestSeedSharedContextRecordsHoopKeys(t *testing.T) {
	g := contextgraph.New("")
	dir := t.TempDir()
	reg := tools.NewRegistry(tools.Options{Workspace: dir})
	if _, err := reg.EnsureRunLayout("ctx-seed"); err != nil {
		t.Fatal(err)
	}
	mgr := &Manager{Graph: g, Tools: reg}
	st := &LoopState{Spec: LoopSpec{ID: "ctx-seed", Goal: "seed goal"}}
	turnID := "loop:ctx-seed"
	mod := ModuleSpec{Kind: StageContext, ID: "context_seed"}

	digest := mgr.seedSharedContext(st, mod, turnID, "Audit the clone", "1) verify 2) fan-out", "CLONE_OK\nREADME.md")
	if !strings.Contains(digest, "[context_digest]") {
		t.Fatalf("digest missing marker: %q", digest)
	}
	if !strings.Contains(digest, "clone_path:") {
		t.Fatalf("digest missing clone_path: %q", digest)
	}
	if !strings.Contains(digest, "Clone verified: YES") {
		t.Fatalf("digest missing Clone verified: %q", digest)
	}
	wantRel := "runs/ctx-seed/work/audit-target"
	got, ok := g.LookupHoopContext(turnID, contextgraph.HoopKeyClonePath)
	if !ok || got != wantRel {
		t.Fatalf("clone_path=%q ok=%v want %q", got, ok, wantRel)
	}
	goal, ok := g.LookupHoopContext(turnID, contextgraph.HoopKeyGoal)
	if !ok || !strings.Contains(goal, "Audit the clone") {
		t.Fatalf("goal=%q ok=%v", goal, ok)
	}
	plan, ok := g.LookupHoopContext(turnID, contextgraph.HoopKeyPlan)
	if !ok || !strings.Contains(plan, "verify") {
		t.Fatalf("plan=%q ok=%v", plan, ok)
	}
}

func TestRecordClonePathFromGitCloneOutput(t *testing.T) {
	g := contextgraph.New("")
	dir := t.TempDir()
	reg := tools.NewRegistry(tools.Options{Workspace: dir})
	if _, err := reg.EnsureRunLayout("clone1"); err != nil {
		t.Fatal(err)
	}
	mgr := &Manager{Graph: g, Tools: reg}
	st := &LoopState{Spec: LoopSpec{ID: "clone1"}}
	turnID := "loop:clone1"
	results := []tools.Result{{
		Name:   "git_clone",
		OK:     true,
		Output: "cloned to runs/clone1/work/audit-target",
	}}
	mgr.recordClonePathsFromTools(st, turnID, results)
	got := mgr.canonicalClonePath(st)
	if got != "runs/clone1/work/audit-target" {
		t.Fatalf("canonical=%q", got)
	}
	v, ok := g.LookupHoopContext(turnID, "clone_path")
	if !ok || v != got {
		t.Fatalf("LookupHoopContext=%q ok=%v", v, ok)
	}
	if len(st.Artifacts) == 0 || st.Artifacts[0] != got {
		t.Fatalf("artifacts=%v", st.Artifacts)
	}
}

func TestStagePromptInjectsSharedContext(t *testing.T) {
	g := contextgraph.New("")
	dir := t.TempDir()
	reg := tools.NewRegistry(tools.Options{Workspace: dir})
	if _, err := reg.EnsureRunLayout("prompt1"); err != nil {
		t.Fatal(err)
	}
	mgr := &Manager{Graph: g, Tools: reg}
	st := &LoopState{Spec: LoopSpec{ID: "prompt1", Goal: "shared goal"}}
	turnID := "loop:prompt1"
	mgr.recordClonePath(st, turnID, "runs/prompt1/work/audit-target", "audit-target")
	g.RecordHoopContext(turnID, contextgraph.HoopKeyGoal, "shared goal")

	prompt := mgr.stagePrompt(st, ModuleSpec{
		Kind: StageActor, ID: "parallel_audit", Prompt: "You are one auditor.",
	}, "shared goal", "plan steps", "")
	if !strings.Contains(prompt, "CONTEXT:") || !strings.Contains(prompt, "[context_digest]") {
		t.Fatalf("missing CONTEXT block:\n%s", prompt)
	}
	if !strings.Contains(prompt, "runs/prompt1/work/audit-target") {
		t.Fatalf("missing clone_path:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Do NOT re-clone") {
		t.Fatalf("missing re-clone guard:\n%s", prompt)
	}
	if !strings.Contains(prompt, "shared goal") {
		t.Fatalf("missing goal:\n%s", prompt)
	}
	// Parallel rolePrompt is built from this prompt — same CONTEXT must be present.
	rolePrompt := "[research worker 1/3]\n" + prompt
	if !strings.Contains(rolePrompt, "clone_path:") {
		t.Fatal("rolePrompt lost clone_path")
	}
}

func TestFilterToolRefsAlwaysIncludesContextQuery(t *testing.T) {
	mgr := &Manager{}
	st := &LoopState{Spec: LoopSpec{ID: "tq"}}
	refs := mgr.filterToolRefs(st, StageActor, []ToolRef{
		{Name: "code_grep", Kind: "builtin"},
	})
	var saw bool
	for _, r := range refs {
		if r.Name == "context_query" {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("context_query missing from refs: %+v", refs)
	}
}

func TestFilterToolRefsCriticNoToolsByDefault(t *testing.T) {
	mgr := &Manager{}
	st := &LoopState{Spec: LoopSpec{ID: "crit"}}
	refs := mgr.filterToolRefs(st, StageCritic, nil)
	if len(refs) != 0 {
		t.Fatalf("default critic must have no tools, got %+v", refs)
	}
}

func TestFilterToolRefsCriticReadOnlyWhenDeclared(t *testing.T) {
	mgr := &Manager{}
	st := &LoopState{Spec: LoopSpec{ID: "crit"}}
	refs := mgr.filterToolRefs(st, StageCritic, []ToolRef{
		{Name: "git_clone", Kind: "builtin"},
		{Name: "fs_write", Kind: "builtin"},
		{Name: "artifact_write", Kind: "builtin"},
		{Name: "fs_list", Kind: "builtin"},
	})
	for _, r := range refs {
		switch r.Name {
		case "git_clone", "fs_write", "artifact_write":
			t.Fatalf("critic must not get write/clone tool %q in %+v", r.Name, refs)
		}
	}
	var sawList bool
	for _, r := range refs {
		if r.Name == "fs_list" {
			sawList = true
		}
	}
	if !sawList {
		t.Fatalf("declared fs_list missing: %+v", refs)
	}
}

func TestSanitizePlanTextDropsToolRejection(t *testing.T) {
	poison := `{"Clone result":"Error: tool 'git_clone' not allowed in this stage."}`
	if got := sanitizePlanText(poison); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	poison2 := `Error: tool 'git_clone' not allowed in this stage.`
	if got := sanitizePlanText(poison2); got != "" {
		t.Fatalf("expected empty for poison2, got %q", got)
	}
	poison3 := `{"error":"git_clone failed","findings":[]}`
	if got := sanitizePlanText(poison3); got != "" {
		t.Fatalf("expected empty for findings-error JSON, got %q", got)
	}
	// artifact_write tool-call dumps must not seed CONTEXT plan.
	for _, p := range []string{
		`{"name":"artifact_write","arguments":{"kind":"out","path":"r.md","content":"# hi"}}`,
		`{"kind":"out","path":"report.md","content":"# Security audit\n..."}`,
		`{"tool_calls":[{"function":{"name":"artifact_write","arguments":"{\"kind\":\"work\",\"path\":\"p.md\",\"content\":\"x\"}"}}]}`,
	} {
		if got := sanitizePlanText(p); got != "" {
			t.Fatalf("expected empty for artifact_write dump, got %q from %q", got, p)
		}
	}
	ok := "1) clone_fetch clones 2) fan-out audit"
	if got := sanitizePlanText(ok); got != ok {
		t.Fatalf("got %q want %q", got, ok)
	}
}

func TestSeedSharedContextSkipsPoisonedPlan(t *testing.T) {
	g := contextgraph.New("")
	dir := t.TempDir()
	reg := tools.NewRegistry(tools.Options{Workspace: dir})
	if _, err := reg.EnsureRunLayout("poison-plan"); err != nil {
		t.Fatal(err)
	}
	mgr := &Manager{Graph: g, Tools: reg}
	st := &LoopState{Spec: LoopSpec{ID: "poison-plan", Goal: "seed goal"}}
	turnID := "loop:poison-plan"
	mod := ModuleSpec{Kind: StageContext, ID: "context_seed"}
	// Pre-seed a poisoned plan (simulates prior iteration / incomplete filter).
	g.RecordHoopContext(turnID, contextgraph.HoopKeyPlan, `Error: tool 'git_clone' not allowed in this stage.`)
	poison := `Error: tool 'git_clone' not allowed in this stage.`
	digest := mgr.seedSharedContext(st, mod, turnID, "Audit the clone", poison, "CLONE_OK\nREADME.md")
	v, ok := g.LookupHoopContext(turnID, contextgraph.HoopKeyPlan)
	if !ok || !strings.Contains(v, "Clone result: OK at") {
		t.Fatalf("poisoned plan must be rewritten to Clone result OK, got %q ok=%v", v, ok)
	}
	if strings.Contains(v, "not allowed") {
		t.Fatalf("rewritten plan still poisoned: %q", v)
	}
	if _, ok := g.LookupHoopContext(turnID, contextgraph.HoopKeyClonePath); !ok {
		t.Fatal("clone_path should still be recorded")
	}
	if !strings.Contains(digest, "Clone verified: YES") {
		t.Fatalf("digest should announce verified clone: %q", digest)
	}
	if strings.Contains(digest, "not allowed in this stage") {
		t.Fatalf("digest must omit poison: %q", digest)
	}
}

func TestRecordClonePathIfPresentFromDisk(t *testing.T) {
	g := contextgraph.New("")
	dir := t.TempDir()
	reg := tools.NewRegistry(tools.Options{Workspace: dir})
	if _, err := reg.EnsureRunLayout("disk-clone"); err != nil {
		t.Fatal(err)
	}
	rel := "runs/disk-clone/work/audit-target"
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Join(abs, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := &Manager{Graph: g, Tools: reg}
	st := &LoopState{Spec: LoopSpec{ID: "disk-clone"}}
	turnID := "loop:disk-clone"
	mgr.recordClonePathIfPresent(st, turnID, "audit-target")
	got := mgr.canonicalClonePath(st)
	if got != rel {
		t.Fatalf("canonical=%q want %q", got, rel)
	}
}

func TestRecordClonePathFromFSListSuccess(t *testing.T) {
	g := contextgraph.New("")
	dir := t.TempDir()
	reg := tools.NewRegistry(tools.Options{Workspace: dir})
	if _, err := reg.EnsureRunLayout("fslist1"); err != nil {
		t.Fatal(err)
	}
	rel := "runs/fslist1/work/audit-target"
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(abs, 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := &Manager{Graph: g, Tools: reg}
	st := &LoopState{Spec: LoopSpec{ID: "fslist1"}}
	turnID := "loop:fslist1"
	mgr.recordClonePathsFromTools(st, turnID, []tools.Result{{
		Name:   "fs_list",
		OK:     true,
		Output: "path: " + rel + "\nREADME.md\npackage.json\n",
	}})
	got := mgr.canonicalClonePath(st)
	if got != rel {
		t.Fatalf("canonical=%q want %q", got, rel)
	}
}

func TestToolLoopMaxStepsFloor(t *testing.T) {
	if toolLoopMaxStepsStage < 20 {
		t.Fatalf("toolLoopMaxStepsStage=%d want >= 20", toolLoopMaxStepsStage)
	}
	if toolLoopMaxStepsParallel < 20 {
		t.Fatalf("toolLoopMaxStepsParallel=%d want >= 20", toolLoopMaxStepsParallel)
	}
	if toolLoopMaxStepsParallel < 24 || toolLoopMaxStepsParallel > 32 {
		t.Fatalf("toolLoopMaxStepsParallel=%d want in 24–32 band", toolLoopMaxStepsParallel)
	}
}

func TestIndexToolGraphResultsAfterClone(t *testing.T) {
	g := contextgraph.New("")
	dir := t.TempDir()
	reg := tools.NewRegistry(tools.Options{Workspace: dir})
	if _, err := reg.EnsureRunLayout("idx1"); err != nil {
		t.Fatal(err)
	}
	rootRel := "runs/idx1/work/audit-target"
	abs := filepath.Join(dir, filepath.FromSlash(rootRel))
	if err := os.MkdirAll(filepath.Join(abs, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(abs, "pkg", "main.go"), []byte("package pkg\nfunc Hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := &Manager{Graph: g, Tools: reg}
	turnID := "loop:idx1"
	mgr.indexToolGraphResults(turnID, []tools.Result{{
		Name:   "git_clone",
		OK:     true,
		Output: "cloned to " + rootRel,
	}})
	tree, ok := g.LookupHoopContext(turnID, contextgraph.HoopKeyFileTree)
	if !ok || !strings.Contains(tree, rootRel) {
		t.Fatalf("file-tree=%q ok=%v", tree, ok)
	}
	ents := g.Entities(turnID, 64)
	if len(ents) < 2 {
		t.Fatalf("expected indexed entities, got %d", len(ents))
	}
}

func TestCloneRepoSecurityAuditSampleHasContextNoParallelClone(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	path := filepath.Join(root, "samples", "hoops", "clone-repo-security-audit.yaml")
	spec, err := ReadHoopYAMLFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var sawCtx, sawVerify bool
	var parallel, planner, critic *StageSpec
	for i := range spec.Stages {
		st := &spec.Stages[i]
		if st.Kind == StageContext {
			sawCtx = true
		}
		if st.ID == "verify_clone" {
			sawVerify = true
		}
		if st.ID == "parallel_audit" {
			parallel = st
		}
		if st.ID == "plan_audit" {
			planner = st
		}
		if st.ID == "critic" {
			critic = st
		}
	}
	if !sawVerify || !sawCtx {
		t.Fatalf("want verify_clone + context stages, stages=%+v", spec.Stages)
	}
	// context_seed must follow verify_clone in graph_edges / stage order.
	var verifyIdx, ctxIdx, parIdx = -1, -1, -1
	for i, st := range spec.Stages {
		switch st.ID {
		case "verify_clone":
			verifyIdx = i
		case "context_seed":
			ctxIdx = i
		case "parallel_audit":
			parIdx = i
		}
	}
	if !(verifyIdx >= 0 && ctxIdx > verifyIdx && parIdx > ctxIdx) {
		t.Fatalf("order verify=%d context=%d parallel=%d", verifyIdx, ctxIdx, parIdx)
	}
	if parallel == nil || parallel.Parallel < 2 {
		t.Fatalf("parallel_audit=%+v", parallel)
	}
	for _, tr := range parallel.Tools {
		if tr.Name == "git_clone" {
			t.Fatal("parallel_audit must not declare git_clone")
		}
	}
	if planner != nil {
		for _, tr := range planner.Tools {
			if tr.Name == "git_clone" || tr.Name == "fs_list" || tr.Name == "web_search" {
				t.Fatalf("plan_audit should not declare tool %q", tr.Name)
			}
		}
		if !strings.Contains(strings.ToLower(planner.Prompt), "do not call git_clone") {
			t.Fatal("plan_audit prompt must forbid git_clone")
		}
	}
	if critic == nil {
		t.Fatal("missing critic stage")
	}
	if len(critic.Tools) != 0 {
		t.Fatalf("critic must declare no tools (completeOnce), got %+v", critic.Tools)
	}
	if !strings.Contains(critic.Prompt, "SCORE:") {
		t.Fatal("critic prompt must require SCORE")
	}
	if !strings.Contains(strings.ToLower(critic.Prompt), "do not ask") {
		t.Fatal("critic prompt must forbid questions")
	}
	if parallel != nil && !strings.Contains(strings.ToLower(parallel.Prompt), "inventing paths") {
		t.Fatal("parallel_audit must forbid inventing paths")
	}
}
