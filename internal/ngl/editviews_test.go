package ngl_test

import (
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/ngl"
)

func TestEditViews_WholeFileConvertsToBeforeAfter(t *testing.T) {
	v := ngl.EditViews{WholeFile: &ngl.WholeFile{Content: "new content\n", Overwrite: true}}
	val, ok := v.Get("before_after")
	if !ok {
		t.Fatalf("expected before_after to be derivable from whole_file")
	}
	pair := val.([2]string)
	if pair[0] != "" {
		t.Fatalf("got before %q, want empty (no pre-image known from a whole-file write alone)", pair[0])
	}
	if pair[1] != "new content\n" {
		t.Fatalf("got after %q", pair[1])
	}
}

func TestEditViews_UnavailableViewReturnsFalseNotZeroValue(t *testing.T) {
	// A bare RangeReplace (agy's native shape) genuinely cannot answer
	// "give me a whole_file view" — no converter is registered for that
	// pair, and Get must say so explicitly rather than return a
	// zero-valued WholeFile that looks like a real (empty) answer.
	v := ngl.EditViews{RangeReplace: &ngl.RangeReplace{StartLine: 1, EndLine: 2, New: "x"}}
	_, ok := v.Get("whole_file")
	if ok {
		t.Fatalf("expected whole_file to be unavailable from a bare RangeReplace")
	}
}

func TestEditViews_DirectValueNeedsNoConversion(t *testing.T) {
	v := ngl.EditViews{UnifiedText: "--- a\n+++ b\n"}
	val, ok := v.Get("unified_text")
	if !ok {
		t.Fatalf("expected the natively-populated view to be returned directly")
	}
	if val.(string) != "--- a\n+++ b\n" {
		t.Fatalf("got %q", val)
	}
}

func TestConverter_HunksToUnifiedIncludesHunkHeader(t *testing.T) {
	v := ngl.EditViews{Hunks: []ngl.DiffHunk{
		{OldStart: 5, OldLines: 2, NewStart: 5, NewLines: 3, Lines: []string{" ctx", "-old", "+new1", "+new2"}},
	}}
	val, ok := v.Get("unified_text")
	if !ok {
		t.Fatalf("expected unified_text to be derivable from hunks")
	}
	text := val.(string)
	if !strings.Contains(text, "@@ -5,2 +5,3 @@") {
		t.Fatalf("got %q, want a hunk header for -5,2 +5,3", text)
	}
	if !strings.Contains(text, "-old") || !strings.Contains(text, "+new1") {
		t.Fatalf("got %q, missing expected diff lines", text)
	}
}
