package dashboard

import (
	"encoding/json"
	"net/http"
)

// handleSetMITMEnrollment implements POST /api/mitm/enrollment.
//
// Refer to the comment on mitm.PIDScoper for the cause. This makes the
// transparent interception more narrow: it uses only the PIDs that a person
// gives, in addition to the AllowProcessNames test that exists. Without it,
// Glider catches each process on the machine with an image name that agrees.
//
// The body is {"pids":[1234,5678]}. An array that is empty, or that is absent,
// stops the narrow selection. That is the default behaviour today.
//
// This does nothing, and it succeeds, when s.Redirector is nil. That occurs
// when transparent mode does not operate, or when the redirector of the
// platform does not implement PIDScoper. To call this function against a setup
// with no transparent mode is not dangerous. Therefore the code does not give
// an error.
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
