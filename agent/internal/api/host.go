package api

import (
	"context"
	"net/http"
	"time"
)

// hostTimeout ends a reading of the machine (its systemctl calls have their own, shorter limits).
const hostTimeout = 10 * time.Second

// host answers how the machine is doing now. The CPU share is measured since the previous reading
// by anyone in this process (the manager screen of the same process shares the counter), so a
// caller that polls gets the load between its own calls.
func (s *Server) host(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), hostTimeout)
	defer cancel()
	writeJSON(w, http.StatusOK, s.backend.Host(ctx).Status())
}
