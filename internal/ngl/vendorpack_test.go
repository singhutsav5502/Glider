package ngl_test

import (
	"testing"

	"github.com/glider-ai/glider/internal/ngl"
)

func TestLoadVendorPack_Claude(t *testing.T) {
	p, err := ngl.LoadVendorPack("../../vendorpacks/claude.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Vendor != "claude" {
		t.Fatalf("got vendor %q", p.Vendor)
	}
	if !p.IsConfirmed("Edit") {
		t.Fatalf("Edit should be confirmed")
	}
	if p.Tools["Edit"].DiffView != "hunks" {
		t.Fatalf("Edit diff_view = %q, want hunks", p.Tools["Edit"].DiffView)
	}
	if p.Tools["Write"].DiffView != "whole_file" {
		t.Fatalf("Write diff_view = %q, want whole_file", p.Tools["Write"].DiffView)
	}
	if canon, ok := p.CanonicalArg("Edit", "file_path"); !ok || canon != "path" {
		t.Fatalf("CanonicalArg(Edit, file_path) = %q, %v; want path, true", canon, ok)
	}
}

func TestLoadVendorPack_CursorAgent(t *testing.T) {
	p, err := ngl.LoadVendorPack("../../vendorpacks/cursor-agent.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !p.IsConfirmed("editToolCall") {
		t.Fatalf("editToolCall should be confirmed")
	}
	if p.Tools["editToolCall"].DiffView != "unified_text" {
		t.Fatalf("editToolCall diff_view = %q, want unified_text", p.Tools["editToolCall"].DiffView)
	}
	// A real, load-bearing distinction from the research: the model chose
	// an alternative tool rather than confirming these two exist — must
	// not be marked confirmed just because they're wire-declared.
	if p.IsConfirmed("lsToolCall") {
		t.Fatalf("lsToolCall was never confirmed live — model substituted shellToolCall")
	}
	if p.IsConfirmed("semSearchToolCall") {
		t.Fatalf("semSearchToolCall was never confirmed live — model substituted grep/read")
	}
}

func TestLoadVendorPack_Agy(t *testing.T) {
	p, err := ngl.LoadVendorPack("../../vendorpacks/agy.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The single sharpest finding from the agy research: the wire-declared
	// catalog is a superset of the live toolset. This pack must encode that
	// distinction precisely, not treat "on the wire" as "usable".
	if !p.IsConfirmed("list_dir") {
		t.Fatalf("list_dir (the real live tool name) should be confirmed")
	}
	if p.IsConfirmed("list_directory") {
		t.Fatalf("list_directory (the wire-catalog name, never actually invoked) must not be marked confirmed")
	}
	if p.IsConfirmed("propose_code") {
		t.Fatalf("propose_code was deliberately re-tried twice and never fired — must not be confirmed")
	}
	if p.Tools["replace_file_content"].DiffView != "range_replace" {
		t.Fatalf("replace_file_content diff_view = %q, want range_replace", p.Tools["replace_file_content"].DiffView)
	}
	if p.Tools["write_to_file"].DiffView != "whole_file" {
		t.Fatalf("write_to_file diff_view = %q, want whole_file", p.Tools["write_to_file"].DiffView)
	}

	// The confirmed/unconfirmed ratio itself is a real assertion: this
	// vendor's live toolset (~7 tools) is much smaller than its
	// wire-declared catalog (~40) — a pack that confirmed most entries
	// would silently reintroduce the exact mistake this research corrected.
	confirmedCount := 0
	for _, spec := range p.Tools {
		if spec.Confirmed {
			confirmedCount++
		}
	}
	if confirmedCount > 10 {
		t.Fatalf("got %d confirmed tools for agy, want a small minority of the ~40 wire-declared entries", confirmedCount)
	}
	if confirmedCount < 5 {
		t.Fatalf("got %d confirmed tools for agy, want at least the 5 directly proven live", confirmedCount)
	}
}
