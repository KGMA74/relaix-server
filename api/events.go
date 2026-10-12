package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// keepaliveInterval is how often an idle event stream sends a comment line.
// Proxies and load balancers close connections that stay silent for a minute
// or so; a comment every 25 s keeps the stream under every common default,
// and lets the server notice a client that went away without a FIN.
const keepaliveInterval = 25 * time.Second

// handleEvents streams state changes as server-sent events.
//
//	GET /events
//	Accept: text/event-stream
//
// Each event is "event: <type>" plus a JSON "data:" line — see events.Event.
// They are notifications, not state: a client re-reads what changed through
// the regular endpoints. The first frame is a "ready" event, sent once the
// subscription exists, so a client knows from that point on that it will not
// miss a change and can refetch its baseline without a race.
//
// Scoped like the rest of the API: a caller sees the events of its own
// account, and the operator sees them all.
//
// Authenticated like the rest of the API. Browsers' EventSource cannot set an
// Authorization header, so the dashboard reads the stream with fetch instead.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	p := principal(r)

	ch, cancel := s.opts.Events.Subscribe()
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// nginx buffers responses by default, which would hold every event back
	// until the buffer fills.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// retry: how long a disconnected client waits before reconnecting.
	if _, err := fmt.Fprint(w, "retry: 3000\nevent: ready\ndata: {}\n\n"); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		s.opts.Logger.Warn("event stream cannot flush", "err", err)
		return
	}

	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case e, ok := <-ch:
			if !ok {
				// Dropped for falling behind, or the server is shutting down.
				// Ending the response makes the client reconnect and resync.
				return
			}
			if !p.AllAccounts && e.AccountID != p.AccountID {
				continue
			}
			data, err := json.Marshal(e)
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, data); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}

		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}
