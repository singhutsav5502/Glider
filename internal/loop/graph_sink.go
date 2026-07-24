package loop

import "github.com/glider-ai/glider/internal/contextgraph"

// LoopGraphSink is the narrow contextgraph surface Manager needs (DIP).
// *contextgraph.Store satisfies this; tests can inject fakes without a full store.
type LoopGraphSink interface {
	Append(ev contextgraph.Event)
	Turn(id string) (contextgraph.TurnView, bool)
	RelevancyScore(turnID string) float64
	RecordThreadWave(turnID, threadID string, waveIndex int, mergedID, mergedSummary string, workers []contextgraph.WaveWorker)
	RecordEpisodeFact(turnID, episodeID, label, summary string)
	RecordHoopContext(turnID, key, value string)
	LookupHoopContext(turnID, key string) (string, bool)
	RecordEdge(turnID, id, from, to, relation string, prov contextgraph.Provenance, attrs map[string]string)
	RecordFact(turnID string, f contextgraph.Fact)
	Entities(turnID string, limit int) []contextgraph.Entity
	HoopContextDigest(turnID string, keys ...string) string
	Query(turnID, q string, limit int) string
	PathSummary(turnID, from, to string) string
	IndexFileTree(turnID, root string, maxDepth, maxFiles int) (int, error)
	IndexSymbols(turnID, root string, maxFiles int) (int, error)
}

// Ensure *contextgraph.Store implements LoopGraphSink at compile time.
var _ LoopGraphSink = (*contextgraph.Store)(nil)
