package vendors

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Summarization makes the older half of a continuity record into a short
// briefing. Therefore a long session stays of use, and it does not grow with no
// limit.
//
// Each other agent that processes a long session does this. Claude Code and
// Codex CLI do it at approximately 95% of the context window. OpenCode does it
// when its own isOverflow test operates.
//
// Glider did not do it. It cut each entry at 400 characters, and it removed the
// oldest entry when the ring became full. To cut is not to compact. That method
// loses exactly the early turns which say what the session is FOR.
//
// Two decisions here are different from those tools, and this is on purpose.
//
// **This work operates OFF the critical path.** The other tools compact when a
// live conversation reaches its limit, and the user waits. Glider has no such
// moment: a request that the code already answered writes the record. Therefore
// compaction operates in the background, and a delegate reads a record that is
// already small.
//
// That decision is what permits a summarizer that uses the origin CLI. A full
// start of a CLI needs approximately 20 seconds. That delay is not acceptable
// in front of a delegate, and it has no importance behind one.
//
// **This work prefers the ORIGIN CLI, and not a key of the user.** The user
// already pays for the CLI that they type in. To spend the API credits of
// that user on the bookkeeping of Glider is the incorrect default.
//
// Know what this does NOT do: it does not use the credentials that Glider
// captured from the origin to make a request of its own. Glider has never
// made an upstream call with the authorization of a different party.
// planning/glider_high_level_design.md §8 gives that exact pattern as the
// risk with the terms of service.
//
// To run the origin CLI as a delegate with no console is the permitted
// mechanism, and it reaches the same subscription.

// Summarizer condenses text into at most maxChars characters.
//
// An interface, not a concrete backend call, so this package does not depend
// on internal/backend — the same boundary VendorAdapter keeps for execution.
type Summarizer interface {
	Summarize(ctx context.Context, text string, maxChars int) (string, error)
}

// SummarizerFunc adapts a function to Summarizer.
type SummarizerFunc func(ctx context.Context, text string, maxChars int) (string, error)

func (f SummarizerFunc) Summarize(ctx context.Context, text string, maxChars int) (string, error) {
	return f(ctx, text, maxChars)
}

// SummarySource names one provider in the preference chain.
type SummarySource string

const (
	// SummaryOrigin runs the origin CLI headlessly. Spends the subscription
	// the user already has, costs no API credits, and needs no credential of
	// anyone else's. Slow (~20s), which is why compaction is asynchronous.
	SummaryOrigin SummarySource = "origin"
	// SummaryCloud uses a configured BYOK cloud backend.
	SummaryCloud SummarySource = "cloud"
	// SummaryLocal uses a configured local model.
	SummaryLocal SummarySource = "local"
	// SummaryNone disables model summarization; compaction still happens,
	// deterministically. Always the implicit last resort.
	SummaryNone SummarySource = "none"
)

// DefaultSummaryChain is the order tried when config names none. Origin
// first, per the reasoning above; local last before giving up, because a
// small local model summarizing a session is better than losing it.
var DefaultSummaryChain = []SummarySource{SummaryOrigin, SummaryCloud, SummaryLocal}

var (
	summarizerMu sync.RWMutex
	summarizers  = map[SummarySource]Summarizer{}
	summaryChain = DefaultSummaryChain
)

// RegisterSummarizer wires one source. cmd/glider calls this at startup for
// whichever sources the config actually enables; an unregistered source is
// skipped rather than erroring, so a chain naming "cloud" on a machine with
// no cloud key simply falls through.
func RegisterSummarizer(src SummarySource, s Summarizer) {
	summarizerMu.Lock()
	defer summarizerMu.Unlock()
	if s == nil {
		delete(summarizers, src)
		return
	}
	summarizers[src] = s
}

// SetSummaryChain overrides the preference order.
func SetSummaryChain(chain []SummarySource) {
	summarizerMu.Lock()
	defer summarizerMu.Unlock()
	if len(chain) == 0 {
		summaryChain = DefaultSummaryChain
		return
	}
	summaryChain = append([]SummarySource(nil), chain...)
}

// SummaryChain reports the current preference order.
func SummaryChain() []SummarySource {
	summarizerMu.RLock()
	defer summarizerMu.RUnlock()
	return append([]SummarySource(nil), summaryChain...)
}

// summarizeWithChain tries each registered source in order. It returns the
// first success and the source that produced it.
func summarizeWithChain(ctx context.Context, text string, maxChars int) (string, SummarySource, error) {
	summarizerMu.RLock()
	chain := append([]SummarySource(nil), summaryChain...)
	reg := make(map[SummarySource]Summarizer, len(summarizers))
	for k, v := range summarizers {
		reg[k] = v
	}
	summarizerMu.RUnlock()

	var lastErr error
	for _, src := range chain {
		if src == SummaryNone {
			break
		}
		s, ok := reg[src]
		if !ok || s == nil {
			continue
		}
		out, err := s.Summarize(ctx, text, maxChars)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", src, err)
			continue
		}
		if out = strings.TrimSpace(out); out != "" {
			return out, src, nil
		}
	}
	return "", SummaryNone, lastErr
}

// SummaryPrompt is the instruction given to whichever model does the work.
//
// The shape follows what Claude Code, Codex CLI and OpenCode converged on
// independently — accomplished / in progress / files / decisions / next —
// because a summary that omits any of those reliably fails the next turn.
// Kept vendor-neutral: the same string goes to a CLI, a cloud model or a
// local one.
const SummaryPrompt = `Summarize this record of a coding session into at most %d characters.

Write compact bullet points, no preamble, no closing remarks. Cover, in this order and only where the record actually shows them:
- what was accomplished
- what is still in progress
- files, directories or components involved
- decisions and constraints that later work must respect
- what remains to be done

Preserve concrete identifiers exactly: file paths, function names, flags, error strings. Drop pleasantries, restatements and anything already superseded by a later entry. If the record shows a delegated run and what it changed, keep that — a later delegate must not redo or undo it.

RECORD:
%s`

// CompactionResult reports what one compaction pass did.
type CompactionResult struct {
	Compacted int           // entries replaced by the summary
	Kept      int           // entries left verbatim
	Source    SummarySource // who produced the summary
	Summary   string
	Took      time.Duration
}
