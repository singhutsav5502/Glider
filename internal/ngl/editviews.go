package ngl

import (
	"fmt"
	"strings"
)

// EditViews models an edit as whichever views the source actually
// provided, plus a converter registry that computes other views on demand
// — never one canonical struct every adapter must populate, which is
// exactly the hardcoding problem three vendors' confirmed wire shapes rule
// out (planning/agent_cli_interop.md §"Proposed common envelope" §2: agy
// alone confirmed two structurally different edit shapes live, so there is
// no single lossless "pick one" superset).
type EditViews struct {
	Path string
	Raw  map[string]any // untouched vendor args — nothing is ever lossy, even for views not understood yet

	RangeReplace *RangeReplace // agy's replace_file_content, natively
	Hunks        []DiffHunk    // Claude's Edit structuredPatch, natively
	UnifiedText  string        // cursor-agent's diffString, natively
	Before       string        // cursor-agent's beforeFullFileContent, natively
	After        string        // cursor-agent's afterFullFileContent, natively
	WholeFile    *WholeFile    // agy's write_to_file AND Claude's Write, natively
}

// RangeReplace is agy's replace_file_content shape: a line-range edit with
// an optimistic-concurrency verification snapshot, structurally closer to
// an LSP TextEdit{range, newText} than to a diff.
type RangeReplace struct {
	StartLine   int
	EndLine     int
	OldSnapshot string // TargetContent — expected old lines, for verification
	New         string // ReplacementContent
}

// DiffHunk is one unified-diff hunk, matching Claude's structuredPatch
// shape natively: {oldStart,oldLines,newStart,newLines,lines}.
type DiffHunk struct {
	OldStart int
	OldLines int
	NewStart int
	NewLines int
	Lines    []string // each prefixed " " (context), "-" (removed), or "+" (added), matching structuredPatch's convention
}

// WholeFile is a full-file replace with no diff computed — confirmed
// independently by two vendors (agy's write_to_file, Claude's Write),
// genuinely distinct from RangeReplace (needs a line range) and from
// Hunks/UnifiedText/Before+After (all presuppose a diff was computed
// against a known prior state).
type WholeFile struct {
	Content   string
	Overwrite bool
}

// Get resolves view name from whatever v natively has, walking the
// Converters graph when a direct value isn't already populated. Returns
// ok=false — never a fabricated guess — when the view can't be derived
// from what's available (planning/agent_cli_interop.md: "degrade
// explicitly, never silently guess").
func (v EditViews) Get(want string) (any, bool) {
	if direct, ok := v.direct(want); ok {
		return direct, true
	}
	for have := range v.availableDirect() {
		if fn, ok := Converters[[2]string{have, want}]; ok {
			if val, ok := fn(v); ok {
				return val, true
			}
		}
	}
	return nil, false
}

func (v EditViews) direct(name string) (any, bool) {
	switch name {
	case "range_replace":
		if v.RangeReplace != nil {
			return v.RangeReplace, true
		}
	case "hunks":
		if v.Hunks != nil {
			return v.Hunks, true
		}
	case "unified_text":
		if v.UnifiedText != "" {
			return v.UnifiedText, true
		}
	case "before_after":
		if v.Before != "" || v.After != "" {
			return [2]string{v.Before, v.After}, true
		}
	case "whole_file":
		if v.WholeFile != nil {
			return v.WholeFile, true
		}
	}
	return nil, false
}

// availableDirect returns the set of view names v natively has a non-empty
// value for, used to seed the converter graph walk in Get.
func (v EditViews) availableDirect() map[string]bool {
	out := map[string]bool{}
	if v.RangeReplace != nil {
		out["range_replace"] = true
	}
	if v.Hunks != nil {
		out["hunks"] = true
	}
	if v.UnifiedText != "" {
		out["unified_text"] = true
	}
	if v.Before != "" || v.After != "" {
		out["before_after"] = true
	}
	if v.WholeFile != nil {
		out["whole_file"] = true
	}
	return out
}

// Converter computes a target view from whatever EditViews natively has.
// Returns ok=false rather than lossy-guessing when it can't derive the
// target from what it has.
type Converter func(EditViews) (value any, ok bool)

// Converters is a registry, not a fixed set — new converters slot in
// without touching EditViews or existing adapters.
var Converters = map[[2]string]Converter{
	{"hunks", "unified_text"}:        hunksToUnified,
	{"before_after", "unified_text"}: beforeAfterToUnified,
	{"whole_file", "before_after"}:   wholeFileToAfterOnly,
}

// hunksToUnified renders DiffHunks as a standard unified-diff text body
// (without the "--- a/... +++ b/..." file header, which needs the path on
// both sides and a convention this function doesn't own).
func hunksToUnified(v EditViews) (any, bool) {
	if v.Hunks == nil {
		return nil, false
	}
	var b strings.Builder
	for _, h := range v.Hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		for _, l := range h.Lines {
			b.WriteString(l)
			b.WriteString("\n")
		}
	}
	return b.String(), true
}

// beforeAfterToUnified is an honest partial result, not a full diff
// algorithm: without line-level diffing it can't produce real hunks, so it
// renders the two full snapshots as a single "replace everything" hunk
// rather than fabricate a plausible-looking line-by-line diff it didn't
// actually compute. Real line-diffing is a real follow-up, not something
// to fake here.
func beforeAfterToUnified(v EditViews) (any, bool) {
	if v.Before == "" && v.After == "" {
		return nil, false
	}
	var b strings.Builder
	b.WriteString("--- a\n+++ b\n")
	for _, line := range strings.Split(v.Before, "\n") {
		if v.Before != "" {
			b.WriteString("-" + line + "\n")
		}
	}
	for _, line := range strings.Split(v.After, "\n") {
		if v.After != "" {
			b.WriteString("+" + line + "\n")
		}
	}
	return b.String(), true
}

// wholeFileToAfterOnly treats WholeFile.Content as the post-image; Before
// stays empty unless separately fetched — an honest partial result per the
// design doc, not an error.
func wholeFileToAfterOnly(v EditViews) (any, bool) {
	if v.WholeFile == nil {
		return nil, false
	}
	return [2]string{"", v.WholeFile.Content}, true
}
