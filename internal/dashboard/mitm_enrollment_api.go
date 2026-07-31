package dashboard

import (
	"encoding/json"
	"net/http"
)

// handleSetMITMEnrollment implements POST /api/mitm/enrollment — see
// mitm.PIDScoper's doc comment for why this exists: narrows transparent
// interception to only the given PIDs, on top of the existing
// AllowProcessNames match, instead of catching every process on the
// machine with a matching image name. Body: {"pids":[1234,5678]} — an
// empty or omitted array disables narrowing (today's default behavior).
// A no-op, successfully, when s.Redirector is nil (transparent mode isn't
// running, or the platform redirector doesn't implement PIDScoper) —
// there's nothing unsafe about calling this against a non-transparent
// setup, so it isn't treated as an error.
func (s *Server) handleSetMITMEnrollment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PIDs []uint32 `json:"pids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if s.Redirector != nil {
		s.Redirector.SetEnrolledPIDs(body.PIDs)
	}
	writeJSON(w, map[string]any{"ok": true, "pids": body.PIDs, "active": s.Redirector != nil})
}
