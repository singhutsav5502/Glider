package loop

import (
	"strings"

	"github.com/glider-ai/glider/internal/tools"
)

func (m *Manager) stagePrompt(st *LoopState, mod ModuleSpec, goal, plan, actor string) string {
	var b strings.Builder
	skillSrc := mod.Skill
	if skillSrc == "" {
		skillSrc = st.Spec.Skill
	}
	if skillSrc != "" {
		body, fromFile := m.resolveSkill(skillSrc)
		b.WriteString(FormatSkillPrefix(body, fromFile))
	}
	prompt := mod.Prompt
	if prompt == "" {
		body, fromFile := m.resolveSkill(st.Spec.Skill)
		prompt = st.Spec.formatEffectivePrompt(st.Checkpoint, body, fromFile)
	}
	b.WriteString(prompt)
	b.WriteString("\n\nGOAL:\n")
	b.WriteString(goal)
	if st.Checkpoint.LastEpisodeID != "" {
		b.WriteString("\n\nPRIOR_EPISODE: ")
		b.WriteString(st.Checkpoint.LastEpisodeID)
		b.WriteString(" status=")
		b.WriteString(st.Checkpoint.EvalStatus)
	}
	if p := sanitizePlanText(plan); p != "" && mod.Kind != StagePlanner {
		b.WriteString("\n\nPLAN:\n")
		b.WriteString(truncate(p, promptPlanCap))
	}
	if actor != "" && mod.Kind == StageCritic {
		b.WriteString("\n\nACTOR_OUTPUT:\n")
		b.WriteString(truncate(actor, promptActorCap))
	}
	// Shared CONTEXT for every LLM stage (including parallel rolePrompt built from this).
	if ctxBlock := m.stageContextBlock(st, goal, plan); ctxBlock != "" {
		b.WriteString("\n\n")
		b.WriteString(ctxBlock)
	}
	if feedBlock := m.feedsPromptBlock(st, mod); feedBlock != "" {
		b.WriteString("\n\n")
		b.WriteString(feedBlock)
	}
	if m.Tools != nil {
		lay := tools.LayoutForRun(m.Tools.Workspace(), st.Spec.ID)
		b.WriteString("\n\n")
		b.WriteString(lay.PromptHint())
	}
	return b.String()
}

// StagePrompt builds a stage user message for tests and tooling.
func StagePrompt(goal string, stage StageSpec, prior string, eval EvalSpec) string {
	var b strings.Builder
	b.WriteString("[stage:")
	b.WriteString(string(stage.Kind))
	b.WriteString("]\n")
	if stage.Prompt != "" {
		b.WriteString(stage.Prompt)
		b.WriteString("\n")
	}
	b.WriteString("GOAL:\n")
	b.WriteString(goal)
	if eval.Goal != "" {
		b.WriteString("\nEval: ")
		b.WriteString(eval.Goal)
	}
	if prior != "" {
		b.WriteString("\nPRIOR:\n")
		b.WriteString(prior)
	}
	return b.String()
}
