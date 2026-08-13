package vendors_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glider-ai/glider/internal/vendors"
)

type stubSummarizer struct {
	name  string
	fail  bool
	calls *[]string
}

func (s stubSummarizer) Summarize(ctx context.Context, text string, maxChars int) (string, error) {
	*s.calls = append(*s.calls, s.name)
	if s.fail {
		return "", errors.New("stub failure")
	}
	return "summary from " + s.name, nil
}

func resetSummarizers(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		for _, s := range []vendors.SummarySource{vendors.SummaryOrigin, vendors.SummaryCloud, vendors.SummaryLocal} {
			vendors.RegisterSummarizer(s, nil)
		}
		vendors.SetSummaryChain(nil)
	})
}

// The whole point of the chain: spend the subscription the user already has
// before spending their separate API credits.
func TestSummaryChain_PrefersOriginOverCloudAndLocal(t *testing.T) {
	resetSummarizers(t)
	withHome(t)

	var calls []string
	vendors.RegisterSummarizer(vendors.SummaryOrigin, stubSummarizer{name: "origin", calls: &calls})
	vendors.RegisterSummarizer(vendors.SummaryCloud, stubSummarizer{name: "cloud", calls: &calls})
	vendors.RegisterSummarizer(vendors.SummaryLocal, stubSummarizer{name: "local", calls: &calls})
	vendors.SetSummaryChain(nil) // defaults

	ws := t.TempDir()
	pid := uint32(os.Getpid())
	for i := 0; i < vendors.CompactThreshold+8; i++ {
		if err := vendors.RecordContinuity(ws, "claude", pid, "turn "+itoa(i)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	res, err := vendors.CompactContinuity(context.Background(), ws)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if res.Source != vendors.SummaryOrigin {
		t.Fatalf("source = %q, want origin — the chain must try the origin CLI first", res.Source)
	}
	if len(calls) != 1 || calls[0] != "origin" {
		t.Fatalf("expected exactly one call, to origin; got %v", calls)
	}
}

// A source that fails must fall through, not abort compaction.
func TestSummaryChain_FallsThroughOnFailure(t *testing.T) {
	resetSummarizers(t)
	withHome(t)

	var calls []string
	vendors.RegisterSummarizer(vendors.SummaryOrigin, stubSummarizer{name: "origin", fail: true, calls: &calls})
	vendors.RegisterSummarizer(vendors.SummaryCloud, stubSummarizer{name: "cloud", calls: &calls})
	vendors.SetSummaryChain(nil)

	ws := t.TempDir()
	pid := uint32(os.Getpid())
	for i := 0; i < vendors.CompactThreshold+8; i++ {
		if err := vendors.RecordContinuity(ws, "claude", pid, "turn "+itoa(i)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	res, err := vendors.CompactContinuity(context.Background(), ws)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if res.Source != vendors.SummaryCloud {
		t.Fatalf("source = %q, want cloud after origin failed", res.Source)
	}
	if len(calls) != 2 || calls[0] != "origin" || calls[1] != "cloud" {
		t.Fatalf("chain order wrong: %v", calls)
	}
}

// With every source failing, compaction still happens — deterministically.
func TestSummaryChain_FallsBackToDeterministicDigest(t *testing.T) {
	resetSummarizers(t)
	withHome(t)

	var calls []string
	vendors.RegisterSummarizer(vendors.SummaryOrigin, stubSummarizer{name: "origin", fail: true, calls: &calls})
	vendors.RegisterSummarizer(vendors.SummaryCloud, stubSummarizer{name: "cloud", fail: true, calls: &calls})
	vendors.SetSummaryChain(nil)

	ws := t.TempDir()
	pid := uint32(os.Getpid())
	if err := vendors.RecordContinuity(ws, "claude", pid, "GOAL: the thing that must survive"); err != nil {
		t.Fatalf("record: %v", err)
	}
	for i := 0; i < vendors.CompactThreshold+8; i++ {
		if err := vendors.RecordContinuity(ws, "claude", pid, "turn "+itoa(i)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	res, err := vendors.CompactContinuity(context.Background(), ws)
	if err != nil {
		t.Fatalf("compact: %v", err)
	}
	if res.Source != vendors.SummaryNone {
		t.Fatalf("source = %q, want none once every model source failed", res.Source)
	}
	if res.Compacted == 0 {
		t.Fatal("compaction was abandoned when the models failed — the digest must still run")
	}
	if !strings.Contains(res.Summary, "must survive") {
		t.Errorf("the deterministic digest dropped the opening goal: %q", res.Summary)
	}
}

// "none" in the chain stops it, so a user can opt out of model summarization
// entirely without disabling compaction.
func TestSummaryChain_NoneStopsTheChain(t *testing.T) {
	resetSummarizers(t)
	withHome(t)

	var calls []string
	vendors.RegisterSummarizer(vendors.SummaryLocal, stubSummarizer{name: "local", calls: &calls})
	vendors.SetSummaryChain([]vendors.SummarySource{vendors.SummaryNone, vendors.SummaryLocal})

	ws := t.TempDir()
	pid := uint32(os.Getpid())
	for i := 0; i < vendors.CompactThreshold+8; i++ {
		if err := vendors.RecordContinuity(ws, "claude", pid, "turn "+itoa(i)); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if _, err := vendors.CompactContinuity(context.Background(), ws); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("a source after \"none\" was still called: %v", calls)
	}
}

// The origin summarizer must never select an interactive template.
//
// It operates in the background. And the DEFAULT template of agy is
// interactive. Therefore a simple method of "the first enabled vendor, with its
// default template" would open a console window on the desktop of the user,
// because a task in the background decided to make a file smaller.
func TestOriginSummarizer_NeverPicksAnInteractiveTemplate(t *testing.T) {
	// The path must NOT resolve. An earlier version of this test used "agy". On a
	// machine that has that binary, the test found it and ran it for 20 seconds. A
	// test must never start a vendor CLI.
	reg := vendors.Registry{Vendors: []vendors.Vendor{
		{
			Name: "agy", Enabled: true,
			Path: filepath.Join(t.TempDir(), "no-such-binary-agy"),
			Templates: []vendors.CommandTemplate{
				{Name: "default", Mode: "interactive", Args: []string{"--prompt-interactive", "{{prompt}}"}},
				{Name: "headless", Mode: "headless", Args: []string{"-p", "{{prompt}}"}},
			},
		},
	}}

	o := vendors.OriginSummarizer{Registry: func() vendors.Registry { return reg }, Vendor: "agy"}
	// The exec fails, and the error names the template that was selected.
	_, err := o.Summarize(context.Background(), "text", 100)
	if err == nil {
		t.Fatal("expected the exec to fail against a non-existent binary")
	}
	if strings.Contains(err.Error(), "agy:default") {
		t.Fatalf("the interactive default template was selected: %v", err)
	}
	if !strings.Contains(err.Error(), "agy:headless") {
		t.Fatalf("expected the headless template to be chosen, got: %v", err)
	}
}

// A vendor with interactive templates only cannot operate as a summarizer. The
// code must report that condition. It must not open a window with no
// message.
func TestOriginSummarizer_RefusesWhenOnlyInteractiveExists(t *testing.T) {
	reg := vendors.Registry{Vendors: []vendors.Vendor{
		{
			Name: "agy", Enabled: true,
			Path: filepath.Join(t.TempDir(), "no-such-binary-agy"),
			Templates: []vendors.CommandTemplate{
				{Name: "default", Mode: "interactive", Args: []string{"{{prompt}}"}},
			},
		},
	}}
	o := vendors.OriginSummarizer{Registry: func() vendors.Registry { return reg }}
	_, err := o.Summarize(context.Background(), "text", 100)
	if err == nil {
		t.Fatal("expected a refusal when no headless template exists")
	}
	if !strings.Contains(err.Error(), "headless") {
		t.Errorf("error should say no headless template was available, got: %v", err)
	}
}
