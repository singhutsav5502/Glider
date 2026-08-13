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
	// A tool_result block that contains the text of the flag must never appear in
	// the result. This part was already correct before the correction, because a
	// filter on the type of the block existed. This test stays as an explicit
	// regression test, because the logic moved to a different package.
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

// TestLastUserInstruction_StripsClaudeSystemReminder is the direct regression
// test for the live defect from 2026-07-26.
//
// A true block with type="text" can hold the <system-reminder> content that
// Claude Code adds automatically. That content can contain the text of the
// flag. Such a block must not start a delegation.
//
// The test uses the true shape that a person captured live in this
// investigation, for example "<system-reminder>\nAs you answer the user's
// questions...\n</system-reminder>".
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

// TestLastUserInstruction_MultipleSystemReminderBlocks examines a message
// with more than one <system-reminder> block, and those blocks are not
// together. A person observed this condition many times in this
// investigation. The notifications of a task add one block. The context of
// the memory adds a second block. Both blocks are in the same turn.
//
// The test confirms that the pattern, which is not greedy, removes each block
// at its own end. The pattern must not go from the first opening tag to the
// last closing tag, because that would also remove the true content between
// them.
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

// TestLastUserInstruction_PreservesGenuineFlagOutsideScaffold examines the
// other side of the same correction. A flag that a person truly typed,
// outside any added content, must still agree. The correction must not go too
// far and agree with nothing.
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

// TestLastUserInstruction_UnknownVendorNoStripping proves that the correction
// uses data, and that no Claude value is fixed in the code. A vendor name
// that the code does not know must not use the pattern of Claude, or of a
// different vendor, with no message. A vendor with no confirmed convention
// gets no pattern, at any time. It never gets a pattern from an estimate.
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

// TestLastUserInstruction_EmptyVendorStripsDefensively is the regression test
// for a true defect that the general form of the origin vendor added, on
// 2026-07-26, the same day. An EMPTY vendor name is NOT the same condition as
// the vendor with a name and no registration in
// TestLastUserInstruction_UnknownVendorNoStripping. An empty name occurs when
// the code cannot resolve the origin process, and
// vendors.ResolveOriginVendorName documents that "" result. For "", the code
// must remove each pattern that it knows, for protection. The condition
// "remove nothing" is exactly the cause of the first live defect that this
// full mechanism prevents. Caught live by
// TestDelegateHandler_ScaffoldedFlagDoesNotTriggerDelegation failing the
// moment origin identification stopped being hardcoded.
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

// TestLastUserInstruction_OnlyLastUserMessageConsidered answers the question
// about many turns, which a person asked with the defect about added content.
// A flag that a person typed in an *earlier* turn must not agree on a later
// turn with no relation. The code examines only the newest message with
// role="user".
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

// TestStripScaffold_RemovesAllClaudeWrappers pins a real leak found by
// dogfooding 2026-07-31: a <transcript> blob carrying an entire prior
// session summary was written verbatim into a durable file as if a human
// had typed it. Only <system-reminder> was registered at the time, and the
// stripper map held ONE pattern per vendor, so additional markers could
// never apply even once added.
func TestStripScaffold_RemovesAllClaudeWrappers(t *testing.T) {
	cases := map[string]string{
		"system-reminder":     "keep this <system-reminder>injected junk</system-reminder> text",
		"transcript":          "keep this <transcript>{\"user\":\"a whole prior session\"}</transcript> text",
		"session":             "keep this <session>aux blob</session> text",
		"unclosed transcript": "keep this <transcript>{\"user\":\"truncated mid-stream",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			got := ngl.StripScaffold("claude", input)
			if strings.Contains(got, "injected junk") || strings.Contains(got, "prior session") ||
				strings.Contains(got, "aux blob") || strings.Contains(got, "truncated mid-stream") {
				t.Fatalf("scaffold survived stripping: %q", got)
			}
			if !strings.Contains(got, "keep this") {
				t.Fatalf("genuine human text was destroyed: %q", got)
			}
		})
	}
}
