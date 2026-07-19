package contextgraph

import (
	"fmt"
	"strconv"
	"strings"
)

// WaveWorker is a lightweight worker snapshot for graph writes.
type WaveWorker struct {
	WorkerID string
	Role     string
	Model    string
	Summary  string
	OK       bool
}

// RecordThreadWave writes thread + wave + worker/episode entities and edges (RUNTIME).
func (s *Store) RecordThreadWave(turnID, threadID string, waveIndex int, mergedID, mergedSummary string, workers []WaveWorker) {
	if s == nil {
		return
	}
	if threadID == "" {
		threadID = turnID
	}
	waveID := fmt.Sprintf("%s-wave-%d", threadID, waveIndex)
	s.RecordFact(turnID, Fact{
		ID:         threadID,
		Kind:       KindThread,
		Label:      "thread " + threadID,
		Provenance: ProvenanceRuntime,
		Attrs:      map[string]string{"thread_id": threadID},
	})
	s.RecordFact(turnID, Fact{
		ID:         waveID,
		Kind:       KindWave,
		Label:      fmt.Sprintf("wave %d", waveIndex),
		Provenance: ProvenanceRuntime,
		Attrs: map[string]string{
			"wave":      strconv.Itoa(waveIndex),
			"thread_id": threadID,
			"summary":   truncateLabel(mergedSummary, 240),
		},
	})
	s.RecordEdge(turnID, threadID+"-"+waveID, threadID, waveID, RelPartOf, ProvenanceRuntime, map[string]string{
		"wave": strconv.Itoa(waveIndex),
	})
	epID := mergedID
	if epID == "" {
		epID = waveID + "-merge"
	}
	s.RecordFact(turnID, Fact{
		ID:         epID,
		Kind:       KindEpisode,
		Label:      truncateLabel(mergedSummary, 160),
		Provenance: ProvenanceRuntime,
		Attrs: map[string]string{
			"wave":    strconv.Itoa(waveIndex),
			"summary": truncateLabel(mergedSummary, 400),
		},
	})
	s.RecordEdge(turnID, waveID+"-"+epID, waveID, epID, RelMergedInto, ProvenanceRuntime, nil)

	for _, r := range workers {
		wid := r.WorkerID
		if wid == "" {
			continue
		}
		id := fmt.Sprintf("%s-%s", waveID, wid)
		s.RecordFact(turnID, Fact{
			ID:         id,
			Kind:       KindWorker,
			Label:      wid,
			Provenance: ProvenanceRuntime,
			Attrs: map[string]string{
				"wave":    strconv.Itoa(waveIndex),
				"role":    r.Role,
				"model":   r.Model,
				"summary": truncateLabel(r.Summary, 240),
				"ok":      strconv.FormatBool(r.OK),
			},
		})
		s.RecordEdge(turnID, id+"-prod", id, epID, RelProduced, ProvenanceRuntime, nil)
		if waveIndex > 0 {
			prev := fmt.Sprintf("%s-wave-%d", threadID, waveIndex-1)
			s.RecordEdge(turnID, prev+"-"+waveID, prev, waveID, RelFollows, ProvenanceInferred, nil)
		}
	}

	s.Append(Event{
		Kind:   EventSwarmFanOut,
		TurnID: turnID,
		Actor:  "swarm",
		Attrs: map[string]string{
			"thread_id":  threadID,
			"wave":       strconv.Itoa(waveIndex),
			"workers":    strconv.Itoa(len(workers)),
			"provenance": string(ProvenanceRuntime),
			"summary":    truncateLabel(mergedSummary, 160),
		},
	})
}

// RecordEpisodeFact stores an episode node for PathSummary / context_query.
func (s *Store) RecordEpisodeFact(turnID, episodeID, label, summary string) {
	if s == nil {
		return
	}
	if episodeID == "" {
		episodeID = "ep-" + label
	}
	if label == "" {
		label = episodeID
	}
	s.RecordFact(turnID, Fact{
		ID:         episodeID,
		Kind:       KindEpisode,
		Label:      label,
		Provenance: ProvenanceRuntime,
		Attrs: map[string]string{
			"summary": truncateLabel(summary, 400),
		},
	})
}

func truncateLabel(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
