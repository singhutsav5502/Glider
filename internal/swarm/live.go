package swarm

import (
	"sync"
	"time"
)

// LiveWorkerView is one fan-out worker's live status for graph paint.
type LiveWorkerView struct {
	WorkerID string `json:"worker_id"`
	Role     string `json:"role"`
	Model    string `json:"model,omitempty"`
	Status   string `json:"status"` // pending | running | ok | fail
	Summary  string `json:"summary,omitempty"`
	Err      string `json:"err,omitempty"`
}

// LiveRunView is a snapshot of an in-flight or recently finished swarm run.
type LiveRunView struct {
	TurnID    string            `json:"turn_id"`
	ThreadID  string            `json:"thread_id,omitempty"`
	Status    string            `json:"status"`          // running | completed | failed
	Phase     string            `json:"phase,omitempty"` // fanout | merge | done
	Wave      int               `json:"wave,omitempty"`
	Waves     int               `json:"waves,omitempty"`
	Workers   []LiveWorkerView  `json:"workers"`
	Progress  DecisionRouteView `json:"progress,omitempty"`
	UpdatedAt time.Time         `json:"updated_at"`
}

type liveStore struct {
	mu   sync.RWMutex
	byID map[string]*LiveRunView
}

func (r *Runner) ensureLive() *liveStore {
	r.liveMu.Lock()
	defer r.liveMu.Unlock()
	if r.liveRuns == nil {
		r.liveRuns = &liveStore{byID: make(map[string]*LiveRunView)}
	}
	return r.liveRuns
}

// LiveProgress returns a copy of the live run snapshot (nil if unknown).
func (r *Runner) LiveProgress(turnID string) *LiveRunView {
	if r == nil || turnID == "" {
		return nil
	}
	st := r.ensureLive()
	st.mu.RLock()
	defer st.mu.RUnlock()
	src := st.byID[turnID]
	if src == nil {
		return nil
	}
	cp := *src
	cp.Workers = append([]LiveWorkerView(nil), src.Workers...)
	return &cp
}

func (r *Runner) beginLive(turnID, threadID string, workers []LiveWorkerView, waves int) {
	if r == nil || turnID == "" {
		return
	}
	st := r.ensureLive()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.byID[turnID] = &LiveRunView{
		TurnID:    turnID,
		ThreadID:  threadID,
		Status:    "running",
		Phase:     "fanout",
		Wave:      0,
		Waves:     waves,
		Workers:   append([]LiveWorkerView(nil), workers...),
		UpdatedAt: time.Now().UTC(),
	}
	// Cap retained runs.
	if len(st.byID) > 32 {
		var oldest string
		var oldestT time.Time
		first := true
		for id, v := range st.byID {
			if id == turnID {
				continue
			}
			if first || v.UpdatedAt.Before(oldestT) {
				oldest, oldestT, first = id, v.UpdatedAt, false
			}
		}
		if oldest != "" {
			delete(st.byID, oldest)
		}
	}
}

func (r *Runner) patchLive(turnID string, fn func(v *LiveRunView)) {
	if r == nil || turnID == "" || fn == nil {
		return
	}
	st := r.ensureLive()
	st.mu.Lock()
	defer st.mu.Unlock()
	v := st.byID[turnID]
	if v == nil {
		return
	}
	fn(v)
	v.UpdatedAt = time.Now().UTC()
}

func (r *Runner) setLiveWorker(turnID, workerID, role, status, summary, errMsg string) {
	r.patchLive(turnID, func(v *LiveRunView) {
		found := false
		for i := range v.Workers {
			if v.Workers[i].WorkerID == workerID || (workerID == "" && v.Workers[i].Role == role && v.Workers[i].Status == "pending") {
				v.Workers[i].Status = status
				if summary != "" {
					v.Workers[i].Summary = truncate(summary, mergeWorkerSummaryCap)
				}
				if errMsg != "" {
					v.Workers[i].Err = truncate(errMsg, mergeWorkerSummaryCap)
				}
				found = true
				break
			}
		}
		if !found && role != "" {
			v.Workers = append(v.Workers, LiveWorkerView{
				WorkerID: workerID,
				Role:     role,
				Status:   status,
				Summary:  truncate(summary, mergeWorkerSummaryCap),
				Err:      truncate(errMsg, mergeWorkerSummaryCap),
			})
		}
	})
}

func (r *Runner) setLiveProgress(turnID string, prog DecisionRouteView, phase string) {
	r.patchLive(turnID, func(v *LiveRunView) {
		v.Progress = prog
		if phase != "" {
			v.Phase = phase
		}
	})
}

func (r *Runner) finishLive(turnID string, ok bool, prog DecisionRouteView) {
	r.patchLive(turnID, func(v *LiveRunView) {
		if ok {
			v.Status = "completed"
		} else {
			v.Status = "failed"
		}
		v.Phase = "done"
		v.Progress = prog
	})
}
