package api

import (
	"context"
	"net/http"
	"time"
)

// Pinger reports whether a dependency is reachable. Narrow on purpose: the
// health endpoint needs to know that the database answers, not what it holds.
type Pinger interface {
	Ping(ctx context.Context) error
}

// pingTimeout bounds the readiness check. Short: a probe that hangs is worse
// than one that fails, because an orchestrator waiting on it learns nothing
// while the timeout it actually respects is its own.
const pingTimeout = 2 * time.Second

// handleLive answers liveness. It touches nothing, deliberately.
//
// Liveness means "is this process wedged", and the answer to a wedged process
// is to restart it. If this checked the database, a database outage would
// restart every gateway instance in a loop — turning somebody else's problem
// into an outage of our own, and destroying the device connections that would
// otherwise have survived it.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleReady answers readiness: can this instance actually do its job.
//
// Here the database does matter — without it nothing can be accepted — and the
// right response to failure is to take the instance out of rotation, not to
// kill it.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if s.opts.Pinger == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), pingTimeout)
	defer cancel()

	if err := s.opts.Pinger.Ping(ctx); err != nil {
		s.opts.Logger.Warn("readiness check failed", "err", err)
		// The reason is logged, not returned: an unauthenticated endpoint
		// should not hand out connection strings or driver internals.
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unavailable",
			"reason": "database",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
