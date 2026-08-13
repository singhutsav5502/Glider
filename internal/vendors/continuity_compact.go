package vendors

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const (
	// compactWhenOver is the count of entries that makes a record large enough to
	// compact. Below this count, the ring does its work alone.
	//
	// OpenCode measures an overflow against the true context window of the model.
	// Glider cannot do that. This record is not a conversation, and no one model is
	// behind it. The equal measure is the budget that SelectContinuity spends.
	// Therefore compaction starts when the record holds much more than one delegate
	// can receive.
	//
	// This value is exported. Thus a test uses the true limit, and it does not use a
	// number in the test that stops each test when this limit moves. That is exactly
	// what occurred when this value went from 24 to 40.
	compactWhenOver = CompactThreshold
	// compactKeepTail is how many newest entries stay verbatim. Codex CLI
	// keeps roughly the last 20k tokens of user messages beside its summary;
	// OpenCode protects a 2-turn tail. The principle is the same: recent
	// turns are read literally, older ones are read for gist.
	compactKeepTail = 20
	// compactSummaryChars limits the summary that this code makes. It agrees with
	// the size of the background budget. A summary of 1200 characters that replaces
	// half of a record is a title, and it is not a summary, when that record can be
	// large.
	compactSummaryChars = 4000
	// compactTimeout bounds one compaction attempt. Generous because the
	// origin-CLI source pays a full cold start, and nothing is waiting.
	compactTimeout = 3 * time.Minute
)

// CompactThreshold is the count of entries above which the code compacts a
// record. It must stay below continuityMaxEntries. If it does not, the ring
// discards entries before the code can compact them.
const CompactThreshold = 40

// summaryMarker prefixes a compacted entry so a later pass can tell a
// summary from an ordinary turn and never summarize a summary of a summary.
// Repeated compaction is where every tool in this space degrades — Codex CLI
// warns about it explicitly — so the marker is load-bearing, not cosmetic.
const summaryMarker = "[session summary] "

// CompactContinuity replaces the older entries of a record of a workspace with
// one summary. It keeps a tail of entries with no change.
//
// A failure here is acceptable. The function also does nothing to a record that
// it already compacted and which did not grow after that.
//
// Each path with a failure leaves the record exactly as it was. A file that this
// code writes only in part, and which a delegate then reads, is worse than a
// file with no compaction.
func CompactContinuity(ctx context.Context, workspace string) (CompactionResult, error) {
	started := time.Now()
	var res CompactionResult
	res.Source = SummaryNone

	if strings.TrimSpace(workspace) == "" {
		return res, nil
	}
	path, err := ContinuityPath(workspace)
	if err != nil {
		return res, err
	}

	mu := lockForContinuity(path)
	mu.Lock()
	defer mu.Unlock()

	entries, err := readContinuityFile(path)
	if err != nil || len(entries) <= compactWhenOver {
		res.Kept = len(entries)
		return res, err
	}

	head := entries[:len(entries)-compactKeepTail]
	tail := entries[len(entries)-compactKeepTail:]

	// Nothing to gain if the head is already a single summary.
	if len(head) == 1 && strings.HasPrefix(head[0].Text, summaryMarker) {
		res.Kept = len(entries)
		return res, nil
	}

	var b strings.Builder
	for _, e := range head {
		kind := "user"
		if e.Kind == KindDelegate {
			kind = "delegate result"
		}
		fmt.Fprintf(&b, "- (%s) %s\n", kind, e.Text)
	}

	sctx, cancel := context.WithTimeout(ctx, compactTimeout)
	defer cancel()
	summary, src, sErr := summarizeWithChain(sctx,
		fmt.Sprintf(SummaryPrompt, compactSummaryChars, b.String()), compactSummaryChars)
	if sErr != nil || summary == "" {
		// No model available, or all of them failed. Compact anyway, with the
		// deterministic fallback — losing the early turns entirely (which is
		// what the plain ring did) is worse than keeping a coarse digest of
		// them.
		summary = deterministicDigest(head, compactSummaryChars)
		src = SummaryNone
	}

	compacted := ContinuityEntry{
		At:           head[len(head)-1].At,
		OriginVendor: head[len(head)-1].OriginVendor,
		OriginPID:    head[len(head)-1].OriginPID,
		Kind:         KindTurn,
		Text:         summaryMarker + singleLine(summary),
	}

	out := append([]ContinuityEntry{compacted}, tail...)
	if err := writeContinuityFile(path, workspace, out); err != nil {
		return res, err
	}

	res.Compacted = len(head)
	res.Kept = len(tail)
	res.Source = src
	res.Summary = summary
	res.Took = time.Since(started)
	return res, nil
}

// deterministicDigest condenses entries with no model at all.
//
// Not a summary — a digest. It keeps the first entries (which state what the
// session is for), the delegate outcomes (which describe changes already
// made), and counts what it dropped, so the record never silently pretends
// the middle did not happen. This is the floor: it works with no backend, no
// key and no network, and it is what runs when a user sets the chain to
// "none".
func deterministicDigest(entries []ContinuityEntry, maxChars int) string {
	var keep []string
	var dropped int

	for i, e := range entries {
		switch {
		case i < 3: // the opening turns set the goal
			keep = append(keep, e.Text)
		case e.Kind == KindDelegate: // outcomes must survive
			keep = append(keep, e.Text)
		default:
			dropped++
		}
	}
	digest := strings.Join(keep, " · ")
	if dropped > 0 {
		digest += fmt.Sprintf(" · (%d further turns elided, no summarizer configured)", dropped)
	}
	return truncateMiddleOut(digest, maxChars)
}

// MaybeCompactContinuity runs a compaction pass in the background if the
// record looks big enough to need one, and returns immediately.
//
// Callers on a request path use this. It never blocks the caller, never
// returns an error to it, and holds no lock the caller depends on.
func MaybeCompactContinuity(workspace string) {
	if strings.TrimSpace(workspace) == "" {
		return
	}
	go func() {
		defer func() { _ = recover() }() // bookkeeping must never take the process down
		_, _ = CompactContinuity(context.Background(), workspace)
	}()
}
