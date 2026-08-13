package ngl_test

import (
	"testing"

	"github.com/glider-ai/glider/internal/ngl"
)

// TestAnnotateToolCall_ConfirmedToolGetsRealTags proves the vendor-pack
// data layer and the runtime parsers actually connect — before this,
// vendorpacks/*.yaml and adapter_*.go were two independent pieces that
// never talked to each other in code, only in intent.
func TestAnnotateToolCall_ConfirmedToolGetsRealTags(t *testing.T) {
	pack, err := ngl.LoadVendorPack("../../vendorpacks/agy.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tc := &ngl.ToolCall{Name: "write_to_file"}
	pack.AnnotateToolCall(tc)
	if !tc.Confirmed {
		t.Fatalf("write_to_file is confirmed:true in the pack, annotation must reflect that")
	}
	found := false
	for _, tag := range tc.Tags {
		if tag == "write" {
			found = true
		}
	}
	if !found {
		t.Fatalf("got tags %+v, want \"write\" among them", tc.Tags)
	}
}

// TestAnnotateToolCall_UnknownToolStaysUnconfirmed is the concrete proof
// of unknown_tool_policy: passthrough — a tool name the pack has never
// heard of parses into a perfectly normal ToolCall (adapters never
// require a pack to parse at all) and annotation just leaves it
// Confirmed=false, Tags=nil, rather than erroring or guessing.
func TestAnnotateToolCall_UnknownToolStaysUnconfirmed(t *testing.T) {
	pack, err := ngl.LoadVendorPack("../../vendorpacks/agy.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tc := &ngl.ToolCall{Name: "some_tool_that_does_not_exist_yet"}
	pack.AnnotateToolCall(tc)
	if tc.Confirmed {
		t.Fatalf("an unknown tool must never be marked Confirmed")
	}
	if tc.Tags != nil {
		t.Fatalf("an unknown tool must get no tags guessed for it, got %+v", tc.Tags)
	}
}

// TestAnnotateToolCall_WireDeclaredButUnconfirmedToolStaysFalse is the
// sharpest single regression this whole design exists to prevent: a tool
// that IS in the pack (wire-declared) but confirmed:false must stay
// unconfirmed after annotation — never silently upgraded just because
// it is a known name.
func TestAnnotateToolCall_WireDeclaredButUnconfirmedToolStaysFalse(t *testing.T) {
	pack, err := ngl.LoadVendorPack("../../vendorpacks/agy.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tc := &ngl.ToolCall{Name: "propose_code"} // wire-declared, deliberately never confirmed live
	pack.AnnotateToolCall(tc)
	if tc.Confirmed {
		t.Fatalf("propose_code was deliberately re-tried twice and never fired — annotation must not mark it confirmed")
	}
}

// TestAnnotateToolCall_AllThreeVendors is a light cross-vendor sanity
// check that annotation works the same way against all three real packs,
// not just the one exercised above.
func TestAnnotateToolCall_AllThreeVendors(t *testing.T) {
	cases := []struct {
		packPath string
		tool     string
	}{
		{"../../vendorpacks/claude.yaml", "Edit"},
		{"../../vendorpacks/cursor-agent.yaml", "editToolCall"},
		{"../../vendorpacks/agy.yaml", "replace_file_content"},
	}
	for _, c := range cases {
		pack, err := ngl.LoadVendorPack(c.packPath)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", c.packPath, err)
		}
		tc := &ngl.ToolCall{Name: c.tool}
		pack.AnnotateToolCall(tc)
		if !tc.Confirmed {
			t.Fatalf("%s: %s should be confirmed", c.packPath, c.tool)
		}
	}
}
