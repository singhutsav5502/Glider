package vendors

import "sync/atomic"

// Limits on the background block that a delegate receives. A person can change
// them while Glider operates.
//
// These are vars behind accessor functions, and not constants. There is one
// cause: the correct value is a decision about cost, and it is not a fact.
//
// The block goes with EVERY delegate call. Therefore a large budget gives better
// orientation, and it costs more tokens and more time at each delegation. A user
// who routes to a small local model and a user who routes to a large cloud model
// must not have the same value. And no user must build the program again to
// change it.
//
// The code takes the values from context.background.* when it starts. Refer to
// cmd/glider.
var (
	backgroundTokenBudget atomic.Int64
	backgroundMaxEntries  atomic.Int64
	backgroundEntryChars  atomic.Int64
)

func init() {
	backgroundTokenBudget.Store(defaultTokenBudget)
	backgroundMaxEntries.Store(defaultMaxEntries)
	backgroundEntryChars.Store(continuityMaxTextLen)
}

// BackgroundTokenBudget is the approximate token ceiling for the whole
// background block.
func BackgroundTokenBudget() int { return int(backgroundTokenBudget.Load()) }

// BackgroundMaxEntries is the ceiling on how many entries the block may hold.
func BackgroundMaxEntries() int { return int(backgroundMaxEntries.Load()) }

// BackgroundEntryChars is the ceiling on one entry, in characters.
func BackgroundEntryChars() int { return int(backgroundEntryChars.Load()) }

// SetBackgroundLimits applies the limits from the config. A value of zero or
// less keeps the default for that limit. Therefore a config that names only one
// of the three values does not make the other two zero.
func SetBackgroundLimits(tokenBudget, maxEntries, entryChars int) {
	if tokenBudget > 0 {
		backgroundTokenBudget.Store(int64(tokenBudget))
	}
	if maxEntries > 0 {
		backgroundMaxEntries.Store(int64(maxEntries))
	}
	if entryChars > 0 {
		backgroundEntryChars.Store(int64(entryChars))
	}
	// Both entry points for a delegate ask for DefaultContextTurns. To keep it in
	// agreement means that a larger budget can truly apply. At the old fixed value
	// of 6, no budget of more than some hundred tokens could ever apply.
	DefaultContextTurns = BackgroundMaxEntries()
}

// boundBackground applies the limit for one entry and the limit for the full
// budget, to a list of background turns. It does this for each source of those
// turns.
//
// This exists because the budget applied to only ONE of the two sources.
//
// Entries from the continuity record of Glider went through SelectContinuity,
// and that function applied the limit. Entries that the request of the front CLI
// gave went into the pack with no limit. Those came from
// ngl.PriorUserInstructions or OriginAdapter.PriorUserInstructions.
//
// Claude Code sends the full record of the conversation with each call.
// Therefore the path with no limit was the path most probably to be very
// large.
func boundBackground(turns []string) []string {
	if len(turns) == 0 {
		return turns
	}
	maxEntries := BackgroundMaxEntries()
	entryChars := BackgroundEntryChars()
	budget := BackgroundTokenBudget()

	if len(turns) > maxEntries {
		turns = turns[len(turns)-maxEntries:] // keep the newest
	}

	capped := make([]string, len(turns))
	for i, t := range turns {
		capped[i] = truncateMiddleOut(t, entryChars)
	}

	// Spend the budget from the newest backwards, so a record that overflows
	// loses its oldest entries rather than its most recent ones.
	spent := 0
	keepFrom := 0
	for i := len(capped) - 1; i >= 0; i-- {
		cost := approxTokens(capped[i])
		if spent+cost > budget && i != len(capped)-1 {
			keepFrom = i + 1
			break
		}
		spent += cost
	}
	return capped[keepFrom:]
}
