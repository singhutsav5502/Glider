package vendors

import (
	"sort"
	"strings"
	"unicode"

	"github.com/glider-ai/glider/internal/procinfo"
)

// Selection changes the continuity ring into the background block that a
// delegate receives.
//
// The old behaviour was one line: take the last N entries. That is incorrect in
// three different ways, and this file corrects each one.
//
//  1. Time is not relevance. A person who does five turns about CSS, and then
//     delegates a defect in Go, received five turns about CSS as "background".
//     The code now scores each entry against the task of the delegate. This is
//     the method that pi-memctx uses: it ranks its notes against the prompt
//     before it puts any note in the context.
//  2. A count is not a budget. Six entries with a limit of 400 characters is a
//     fixed size. It ignores the true size of the entries, and it was much too
//     small: approximately 150 tokens of orientation in total. A budget of
//     tokens now limits the selection. The overflow test of OpenCode measures
//     the same quantity.
//  3. To remove the oldest entry first destroys the wrong end. The first turns
//     of a session give the objective. The last turns give the current
//     condition. The MIDDLE has the least value. The "middle-out" transform of
//     OpenRouter exists for exactly this cause, and truncateMiddleOut uses the
//     same idea inside one entry.
//
// The code always keeps a tail of recent entries, whatever their score. The
// ranking can be good, but "what the user said now" is never noise.
const (
	// selectProtectedTail is how many of the newest entries bypass scoring.
	selectProtectedTail = 2
	// defaultMaxEntries bounds how many entries the block may hold, whatever
	// the budget allows. Raised alongside the budget: at the old 6, the
	// budget could never bind, because six entries could not fill it.
	defaultMaxEntries = 20
	// defaultTokenBudget limits the full background block, in approximate tokens.
	//
	// 20000 is the quantity that Codex CLI keeps word for word with its summary. It
	// is also the buffer that OpenCode holds for its overflow test.
	//
	// This is an upper limit, and it is not an objective. A short session gives a
	// short block. Only a truly long record comes near this value.
	//
	// Know the cost. This block goes with each delegate call. Therefore a record
	// that truly reaches 20k tokens makes each delegation much more expensive and
	// much slower. Change it with context.background.token_budget if that cost is
	// incorrect for you.
	defaultTokenBudget = 20000
	// charsPerToken is the usual rough estimate for English prose. Used only
	// to size a budget, never to bill anything, so an approximation is fine.
	charsPerToken = 4
)

// approxTokens estimates a token count from a character count.
func approxTokens(s string) int { return (len(s) + charsPerToken - 1) / charsPerToken }

// truncateMiddleOut shortens s to at most max characters by removing the
// MIDDLE, not the tail.
//
// The old code cut the end. Therefore it always removed the part of an
// instruction that says what to do. "refactor the auth module so that
// <everything that matters>" became "refactor the auth module so that".
// Keeping both ends preserves the subject and the conclusion.
func truncateMiddleOut(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	const ellipsis = " […] "
	if max <= len(ellipsis) {
		return s[:max]
	}
	keep := max - len(ellipsis)
	head := keep * 2 / 3 // the opening of an instruction carries more than its end
	tail := keep - head
	return strings.TrimSpace(s[:head]) + ellipsis + strings.TrimSpace(s[len(s)-tail:])
}

// tokenize lowercases and splits on non-letters/digits, dropping very short
// words. Deliberately not stemming: this scores one short instruction against
// a handful of others, where precision matters more than recall.
func tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	out := fields[:0]
	for _, f := range fields {
		if len(f) > 2 && !stopWords[f] {
			out = append(out, f)
		}
	}
	return out
}

var stopWords = map[string]bool{
	"the": true, "and": true, "for": true, "that": true, "this": true,
	"with": true, "from": true, "into": true, "you": true, "are": true,
	"was": true, "not": true, "但": false,
}

// scoreAgainst returns how well entry text matches the task, in [0,1].
//
// The code gives each term a weight from how rare it is across the candidate
// set. Therefore a word in each entry gives almost nothing. Examples are a
// project name, or a file name that each task touches. But a term that the
// task and one entry share gives much. This is a cheap equivalent of the
// "semantic coverage" ranking that pi-memctx does. It makes no model call and
// it needs no store of embeddings. It operates on a small number of short
// texts.
func scoreAgainst(taskTerms map[string]bool, entryText string, df map[string]int, total int) float64 {
	if len(taskTerms) == 0 || total == 0 {
		return 0
	}
	seen := map[string]bool{}
	var score, norm float64
	for term := range taskTerms {
		// inverse document frequency, kept simple and bounded
		weight := 1.0 / float64(1+df[term])
		norm += weight
	}
	if norm == 0 {
		return 0
	}
	for _, t := range tokenize(entryText) {
		if !taskTerms[t] || seen[t] {
			continue
		}
		seen[t] = true
		score += 1.0 / float64(1+df[t])
	}
	return score / norm
}

// SelectContinuity picks the background entries for one delegate run.
//
// task is the delegate's own instruction; entries are ranked against it. A
// blank task falls back to pure recency, which is the old behaviour and the
// only sensible answer when there is nothing to rank against.
func SelectContinuity(entries []ContinuityEntry, task string, maxItems, tokenBudget int) []string {
	if len(entries) == 0 || maxItems <= 0 {
		return nil
	}
	if tokenBudget <= 0 {
		tokenBudget = BackgroundTokenBudget()
	}

	// Document frequency over the candidate set, for the rarity weighting.
	df := map[string]int{}
	for _, e := range entries {
		for _, t := range uniqueTerms(e.Text) {
			df[t]++
		}
	}
	taskTerms := map[string]bool{}
	for _, t := range tokenize(task) {
		taskTerms[t] = true
	}

	type scored struct {
		idx   int
		score float64
	}
	protectedFrom := len(entries) - selectProtectedTail
	if protectedFrom < 0 {
		protectedFrom = 0
	}

	var ranked []scored
	for i, e := range entries {
		if i >= protectedFrom {
			continue // the tail is taken unconditionally
		}
		s := scoreAgainst(taskTerms, e.Text, df, len(entries))
		// At the same relevance, the result of a delegate is worth more than a
		// usual turn. It describes a change that a delegate already made to these
		// files. A delegate that does that work again would remove the change.
		if e.Kind == KindDelegate {
			s += 0.15
		}
		ranked = append(ranked, scored{i, s})
	}
	sort.SliceStable(ranked, func(a, b int) bool { return ranked[a].score > ranked[b].score })

	chosen := map[int]bool{}
	spent := 0
	for i := protectedFrom; i < len(entries); i++ { // tail first, it is mandatory
		chosen[i] = true
		spent += approxTokens(entries[i].Text)
	}
	for _, r := range ranked {
		if len(chosen) >= maxItems {
			break
		}
		cost := approxTokens(entries[r.idx].Text)
		if spent+cost > tokenBudget {
			continue // skip this one, a later cheaper entry may still fit
		}
		if r.score <= 0 && len(taskTerms) > 0 {
			continue // nothing in common with the task at all
		}
		chosen[r.idx] = true
		spent += cost
	}

	// If the ranking found nothing, use the newest entries. Therefore a delegate
	// never gets the protected tail alone, only because its task shares no words
	// with the session.
	if len(chosen) < maxItems {
		for i := protectedFrom - 1; i >= 0 && len(chosen) < maxItems; i-- {
			if chosen[i] {
				continue
			}
			cost := approxTokens(entries[i].Text)
			if spent+cost > tokenBudget {
				break
			}
			chosen[i] = true
			spent += cost
		}
	}

	var out []string
	for i := range entries { // emit in chronological order, oldest first
		if chosen[i] {
			out = append(out, entries[i].Text)
		}
	}
	return out
}

func uniqueTerms(s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, t := range tokenize(s) {
		if !seen[t] {
			seen[t] = true
			out = append(out, t)
		}
	}
	return out
}

// entriesForOrigin returns the entries belonging to the asking session.
//
// The same PID is the primary match. Two CLIs in one workspace are two
// conversations with no relation. To mix them gives a delegate a mixture of
// both.
//
// This corrects one gap. A restart of a CLI gives a NEW pid. Therefore the
// earlier record of that session became invisible to it, in the same
// workspace and from the same tool. No user could find the cause. Entries
// from the same vendor whose process has since EXITED are therefore included
// as well. A dead PID cannot be a concurrent session, so this cannot
// reintroduce the mixing the PID scoping exists to prevent.
//
// procinfo.ProcessAlive answers "cannot tell" as TRUE, so an unsupported
// platform keeps the old strict behaviour rather than silently merging.
func entriesForOrigin(entries []ContinuityEntry, originVendor string, originPID uint32) []ContinuityEntry {
	var out []ContinuityEntry
	for _, e := range entries {
		if e.OriginVendor != originVendor {
			// Vendor must match even when the PID does. An operating system uses a PID
			// again. Therefore an old entry, from a process that exited, can have the
			// same number as a live process of a DIFFERENT CLI. A comparison of the
			// number alone would give the turns of that CLI to this session. The old
			// code compared PIDs first and had this hole.
			continue
		}
		switch {
		case e.OriginPID == originPID:
			out = append(out, e)
		case originPID == 0:
			out = append(out, e)
		case !procinfo.ProcessAlive(e.OriginPID):
			out = append(out, e) // an earlier run of this same CLI
		}
	}
	return out
}
