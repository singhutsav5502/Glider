package loop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/contextkit"
)

func TestLooksLikeSkillRef(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"be careful with secrets", false},
		{"skills/audit/SKILL.md", true},
		{"SKILL.md", true},
		{"security-audit", true},
		{`skills\win\SKILL.md`, true},
	}
	for _, c := range cases {
		if got := LooksLikeSkillRef(c.in); got != c.want {
			t.Fatalf("%q: got %v want %v", c.in, got, c.want)
		}
	}
}

func TestResolveSkillContentFileAndFallback(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "audit")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "# Audit skill\nAlways cite paths."
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, loaded := ResolveSkillContent("audit", root)
	if !loaded || !strings.Contains(got, "Always cite paths") {
		t.Fatalf("loaded=%v got=%q", loaded, got)
	}

	plain, loaded := ResolveSkillContent("keep secrets out of logs", root)
	if loaded || plain != "keep secrets out of logs" {
		t.Fatalf("plain fallback: loaded=%v got=%q", loaded, plain)
	}

	missing, loaded := ResolveSkillContent("no-such-skill", root)
	if loaded || missing != "no-such-skill" {
		t.Fatalf("missing id fallback: loaded=%v got=%q", loaded, missing)
	}
}

// TestResolveSkillContentSkillsDirFirst verifies configured skills_dir wins over later roots
// (Manager.skillSearchRoots order: SkillsDir → workspace → cwd).
func TestResolveSkillContentSkillsDirFirst(t *testing.T) {
	skillsDir := t.TempDir()
	workspace := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skillsDir, "skills", "sec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workspace, "skills", "sec"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "skills", "sec", "SKILL.md"), []byte("from-skills-dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "skills", "sec", "SKILL.md"), []byte("from-workspace"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, loaded := ResolveSkillContent("sec", skillsDir, workspace)
	if !loaded || got != "from-skills-dir" {
		t.Fatalf("want skills_dir first: loaded=%v got=%q", loaded, got)
	}
	// Explicit path under skills/
	got2, loaded2 := ResolveSkillContent("skills/sec/SKILL.md", skillsDir)
	if !loaded2 || !strings.Contains(got2, "from-skills-dir") {
		t.Fatalf("explicit path: loaded=%v got=%q", loaded2, got2)
	}
}

func TestManagerResolveSkillUsesSkillsDir(t *testing.T) {
	skillsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(skillsDir, "skills", "hint"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsDir, "skills", "hint", "SKILL.md"), []byte("# Hint\nUse relative paths."), 0o644); err != nil {
		t.Fatal(err)
	}
	mgr := &Manager{Cfg: RunnerConfig{SkillsDir: skillsDir}}
	body, fromFile := mgr.resolveSkill("hint")
	if !fromFile || !strings.Contains(body, "relative paths") {
		t.Fatalf("fromFile=%v body=%q", fromFile, body)
	}
	plain, fromFile := mgr.resolveSkill("plain prose skill text here")
	if fromFile || plain != "plain prose skill text here" {
		t.Fatalf("plain: fromFile=%v body=%q", fromFile, plain)
	}
}

func TestEffectivePromptUsesSkillString(t *testing.T) {
	s := LoopSpec{Skill: "plain-hint", Prompt: "do the thing"}
	out := s.EffectivePrompt(contextkit.LoopCheckpoint{})
	if !strings.Contains(out, "[skill: plain-hint]") || !strings.Contains(out, "do the thing") {
		t.Fatalf("%q", out)
	}
}

func TestFormatSkillPrefixFromFile(t *testing.T) {
	p := FormatSkillPrefix("# Title\nbody", true)
	if !strings.HasPrefix(p, "[skill]\n") || !strings.Contains(p, "body") {
		t.Fatalf("%q", p)
	}
}

func TestStageRequestsHumanGate(t *testing.T) {
	spec := LoopSpec{Autonomy: AutonomyL3}
	mod := ModuleSpec{Kind: StageActor, ID: "act", Autonomy: AutonomyL1}
	if !stageRequestsHumanGate(spec, mod) {
		t.Fatal("L3 hoop + L1 stage should gate")
	}
	mod2 := ModuleSpec{Kind: StageActor, ID: "act", HumanGate: true}
	if !stageRequestsHumanGate(LoopSpec{Autonomy: AutonomyL3}, mod2) {
		t.Fatal("stage human_gate should gate")
	}
	if stageRequestsHumanGate(LoopSpec{Autonomy: AutonomyL3}, ModuleSpec{Kind: StageActor, ID: "a"}) {
		t.Fatal("plain L3 actor should not gate")
	}
}

func TestCriticFailWantsGate(t *testing.T) {
	if !criticFailWantsGate(LoopSpec{Autonomy: AutonomyL1}, ModuleSpec{}) {
		t.Fatal("L1 hoop should gate on critic fail")
	}
	if criticFailWantsGate(LoopSpec{Autonomy: AutonomyL3}, ModuleSpec{}) {
		t.Fatal("L3 hoop without stage gates should not auto-HITL")
	}
	if !criticFailWantsGate(LoopSpec{Autonomy: AutonomyL3, HumanGate: true}, ModuleSpec{}) {
		t.Fatal("hoop human_gate should still work")
	}
	spec := LoopSpec{
		Autonomy: AutonomyL3,
		Stages:   []StageSpec{{Kind: StageActor, ID: "a", HumanGate: true}},
	}
	if !criticFailWantsGate(spec, ModuleSpec{Kind: StageCritic}) {
		t.Fatal("actor stage human_gate should open critic-fail HITL")
	}
}

func TestIsolateParallelWorkersSubdir(t *testing.T) {
	root := t.TempDir()
	isos, err := isolateParallelWorkers(root, "hoop-x", 2, true, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(isos) != 2 {
		t.Fatalf("len=%d", len(isos))
	}
	for _, iso := range isos {
		if iso.GitWorktree {
			t.Fatal("expected subdir fallback outside git repo")
		}
		st, err := os.Stat(iso.AbsWork)
		if err != nil || !st.IsDir() {
			t.Fatalf("%s: %v", iso.AbsWork, err)
		}
		if !strings.Contains(iso.RelWork, "runs/hoop-x/work/w") {
			t.Fatalf("rel=%s", iso.RelWork)
		}
	}
	none, err := isolateParallelWorkers(root, "hoop-x", 2, false, root)
	if err != nil || none != nil {
		t.Fatalf("flag off: %+v err=%v", none, err)
	}
}
