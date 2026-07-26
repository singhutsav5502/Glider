package ngl_test

import (
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/ngl"
)

func TestLastUserInstruction_PlainStringContent(t *testing.T) {
	msgs := []byte(`[{"role":"user","content":"/agy reply with pong"}]`)
	got, err := ngl.LastUserInstruction("claude", msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/agy reply with pong" {
		t.Fatalf("got %q", got)
	}
}

func TestLastUserInstruction_FiltersToolResultBlocks(t *testing.T) {
	// A tool_result block containing the flag string must never surface —
	// this was already correct pre-fix (block-type filtering existed), kept
	// as an explicit regression test now that the logic moved packages.
	msgs := []byte(`[
		{"role":"user","content":[
			{"type":"tool_result","tool_use_id":"t1","text":"grep output: found /agy usage in docs"},
			{"type":"text","text":"summarize the file above"}
		]}
	]`)
	got, err := ngl.LastUserInstruction("claude", msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "/agy") {
		t.Fatalf("tool_result content leaked into extracted instruction: %q", got)
	}
	if got != "summarize the file above" {
		t.Fatalf("got %q", got)
	}
}

// TestLastUserInstruction_StripsClaudeSystemReminder is the direct
// regression test for the live 2026-07-26 bug: a genuine type="text" block
// whose content is Claude Code's own auto-injected <system-reminder>
// scaffolding, incidentally containing the flag string, must not trigger
// delegation — reproduces the actual shape captured live in this
// investigation (e.g. "<system-reminder>\nAs you answer the user's
// questions...\n</system-reminder>").
func TestLastUserInstruction_StripsClaudeSystemReminder(t *testing.T) {
	msgs := []byte(`[
		{"role":"user","content":[
			{"type":"text","text":"<system-reminder>\nAs you answer the user's questions, you can use the following context:\n# recent tool output\nran: grep -rn /agy internal/\n</system-reminder>\n\nwhat does this file do?"}
		]}
	]`)
	got, err := ngl.LastUserInstruction("claude", msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "/agy") {
		t.Fatalf("system-reminder scaffolding was not stripped, flag string leaked through: %q", got)
	}
	if got != "what does this file do?" {
		t.Fatalf("got %q, want only the genuine human text after the stripped reminder", got)
	}
}

// TestLastUserInstruction_MultipleSystemReminderBlocks covers a message
// with more than one non-contiguous <system-reminder> block (observed live
// throughout this investigation — task notifications and memory context
// both inject separate blocks into the same turn), confirming non-greedy
// matching removes each one at its own boundary rather than spanning from
// the first opening tag to the last closing tag and eating real content
// in between.
func TestLastUserInstruction_MultipleSystemReminderBlocks(t *testing.T) {
	msgs := []byte(`[
		{"role":"user","content":[
			{"type":"text","text":"<system-reminder>first block, mentions /agy here</system-reminder>real question one<system-reminder>second block, also /agy</system-reminder>real question two"}
		]}
	]`)
	got, err := ngl.LastUserInstruction("claude", msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "/agy") {
		t.Fatalf("flag string leaked through multi-block stripping: %q", got)
	}
	if !strings.Contains(got, "real question one") || !strings.Contains(got, "real question two") {
		t.Fatalf("genuine content between/after reminder blocks was lost: %q", got)
	}
}

// TestLastUserInstruction_PreservesGenuineFlagOutsideScaffold is the other
// side of the same fix: a flag the human actually typed, outside any
// scaffolding, must still be detected — the fix must not overcorrect into
// never matching anything.
func TestLastUserInstruction_PreservesGenuineFlagOutsideScaffold(t *testing.T) {
	msgs := []byte(`[
		{"role":"user","content":[
			{"type":"text","text":"<system-reminder>unrelated context, nothing about vendors here</system-reminder>\n\n/agy reply with exactly the word: pong"}
		]}
	]`)
	got, err := ngl.LastUserInstruction("claude", msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "/agy reply with exactly the word: pong" {
		t.Fatalf("got %q, want the genuine flag+prompt preserved", got)
	}
}

// TestLastUserInstruction_UnknownVendorNoStripping proves the fix is
// data-driven, not Claude-hardcoded: an unrecognized vendor name must not
// silently reuse Claude's stripping pattern (or any other vendor's) — a
// vendor with no confirmed scaffolding convention gets none applied, ever,
// rather than a guessed one.
func TestLastUserInstruction_UnknownVendorNoStripping(t *testing.T) {
	msgs := []byte(`[
		{"role":"user","content":"<system-reminder>this vendor has no known stripper</system-reminder> hello"}
	]`)
	got, err := ngl.LastUserInstruction("some-future-vendor", msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "<system-reminder>") {
		t.Fatalf("an unrecognized vendor's content was stripped anyway; expected a pure no-op, got %q", got)
	}
}

// TestLastUserInstruction_EmptyVendorStripsDefensively is the regression
// test for a real bug the origin-vendor generalization introduced
// (2026-07-26, same day): an EMPTY vendor name (unresolvable origin
// process, vendors.ResolveOriginVendorName's documented "" result) is NOT
// the same case as TestLastUserInstruction_UnknownVendorNoStripping's
// named-but-unregistered vendor — "" must strip every known pattern
// defensively, since "no stripping" is exactly the condition that caused
// the original live scaffold-leak bug this whole mechanism exists to
// prevent. Caught live by TestDelegateHandler_ScaffoldedFlagDoesNotTriggerDelegation
// failing the moment origin identification stopped being hardcoded.
func TestLastUserInstruction_EmptyVendorStripsDefensively(t *testing.T) {
	msgs := []byte(`[
		{"role":"user","content":"<system-reminder>\nrecent tool output mentioned /agy somewhere\n</system-reminder>\n\nwhat does this code do?"}
	]`)
	got, err := ngl.LastUserInstruction("", msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "<system-reminder>") || strings.Contains(got, "/agy") {
		t.Fatalf("an unresolvable origin must still strip known scaffolding defensively, got %q", got)
	}
	if !strings.Contains(got, "what does this code do?") {
		t.Fatalf("got %q, expected the genuine trailing text preserved", got)
	}
}

// TestLastUserInstruction_OnlyLastUserMessageConsidered directly answers
// the multi-turn concern raised alongside the scaffolding bug: a flag typed
// in an *earlier* turn must not still match on a later, unrelated turn —
// only the most recent role="user" message is ever inspected.
func TestLastUserInstruction_OnlyLastUserMessageConsidered(t *testing.T) {
	msgs := []byte(`[
		{"role":"user","content":"/agy this was an earlier turn"},
		{"role":"assistant","content":"ok, delegated that earlier turn"},
		{"role":"user","content":"now just a normal follow-up question, no flag here"}
	]`)
	got, err := ngl.LastUserInstruction("claude", msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(got, "/agy") {
		t.Fatalf("an earlier turn's flag leaked into the latest turn's extracted instruction: %q", got)
	}
	if got != "now just a normal follow-up question, no flag here" {
		t.Fatalf("got %q", got)
	}
}

func TestLastUserInstruction_NoUserMessage(t *testing.T) {
	msgs := []byte(`[{"role":"assistant","content":"hi"}]`)
	got, err := ngl.LastUserInstruction("claude", msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Fatalf("got %q, want empty string when there is no user message", got)
	}
}

func TestExtractParts_OtherBlockTypesHaveNoText(t *testing.T) {
	content := []byte(`[{"type":"tool_use","id":"x","name":"Read","input":{}}]`)
	parts := ngl.ExtractParts(content)
	if len(parts) != 1 {
		t.Fatalf("got %d parts, want 1", len(parts))
	}
	if parts[0].Kind != ngl.PartOther || parts[0].Text != "" {
		t.Fatalf("got %+v, want PartOther with empty text", parts[0])
	}
}
