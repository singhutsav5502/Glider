package loop

import (
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// LooksLikeSkillRef reports whether skill is a path or skill id (not free-form prose).
// Paths contain separators or end in .md; skill ids are single tokens [A-Za-z0-9._-].
func LooksLikeSkillRef(skill string) bool {
	s := strings.TrimSpace(skill)
	if s == "" {
		return false
	}
	if filepath.IsAbs(s) || strings.Contains(s, "/") || strings.Contains(s, `\`) {
		return true
	}
	if strings.HasSuffix(strings.ToLower(s), ".md") {
		return true
	}
	if strings.ContainsAny(s, " \t\n") || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// skillFileCandidates returns relative paths to try under each search root.
func skillFileCandidates(skill string) []string {
	skill = strings.TrimSpace(skill)
	skill = filepath.ToSlash(skill)
	if skill == "" {
		return nil
	}
	var out []string
	add := func(p string) {
		p = filepath.ToSlash(strings.TrimPrefix(p, "./"))
		if p == "" {
			return
		}
		for _, e := range out {
			if e == p {
				return
			}
		}
		out = append(out, p)
	}
	add(skill)
	lower := strings.ToLower(skill)
	if strings.HasSuffix(lower, ".md") {
		add(filepath.ToSlash(filepath.Join("skills", skill)))
		return out
	}
	add(skill + "/SKILL.md")
	add(skill + ".md")
	add(filepath.ToSlash(filepath.Join("skills", skill, "SKILL.md")))
	add(filepath.ToSlash(filepath.Join("skills", skill+".md")))
	return out
}

// ResolveSkillContent loads SKILL.md (or path) from roots when skill looks like a
// path/id. On miss or plain-string skill, returns the original skill (string fallback).
// loaded is true only when a file was read successfully.
func ResolveSkillContent(skill string, roots ...string) (content string, loaded bool) {
	skill = strings.TrimSpace(skill)
	if skill == "" {
		return "", false
	}
	if !LooksLikeSkillRef(skill) {
		return skill, false
	}
	if filepath.IsAbs(skill) {
		if data, err := os.ReadFile(skill); err == nil && len(data) > 0 {
			return string(data), true
		}
		return skill, false
	}
	cands := skillFileCandidates(skill)
	for _, root := range roots {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		for _, cand := range cands {
			path := filepath.Join(root, filepath.FromSlash(cand))
			data, err := os.ReadFile(path)
			if err != nil || len(data) == 0 {
				continue
			}
			return string(data), true
		}
	}
	return skill, false
}

// FormatSkillPrefix builds the prompt prefix for a skill string or loaded file body.
func FormatSkillPrefix(skillOrBody string, fromFile bool) string {
	skillOrBody = strings.TrimSpace(skillOrBody)
	if skillOrBody == "" {
		return ""
	}
	if fromFile {
		return "[skill]\n" + skillOrBody + "\n"
	}
	return "[skill: " + skillOrBody + "]\n"
}

// skillSearchRoots returns workspace / configured skills dir / cwd for Manager.
func (m *Manager) skillSearchRoots() []string {
	var roots []string
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" {
			return
		}
		abs, err := filepath.Abs(p)
		if err == nil {
			p = abs
		}
		for _, e := range roots {
			if e == p {
				return
			}
		}
		roots = append(roots, p)
	}
	if m != nil {
		if m.Cfg.SkillsDir != "" {
			add(m.Cfg.SkillsDir)
		}
		if m.Tools != nil {
			add(m.Tools.Workspace())
		}
	}
	add(".")
	return roots
}

// resolveSkill expands a skill field to file contents when possible.
func (m *Manager) resolveSkill(skill string) (body string, fromFile bool) {
	return ResolveSkillContent(skill, m.skillSearchRoots()...)
}
