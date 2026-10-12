package api

import (
	"context"
	"net/http"

	"github.com/KGMA74/relaix-server/store"
)

// SendGuard admits or refuses a send.
//
// It runs inside the transaction that creates the jobs, after they are
// written: it sees their ids, and whatever it records alongside them commits
// or rolls back with them. Returning an error rolls the send back; a
// *Refusal is answered to the caller as it says, anything else as a 500.
type SendGuard interface {
	AdmitSend(ctx context.Context, tx store.Store, p Principal, jobs []*store.Job) error
}

// Refusal is an error an extension returns to answer the caller itself: a
// status, a stable code to branch on, and a message for people.
type Refusal struct {
	Status  int
	Code    string
	Message string
}

func (r *Refusal) Error() string { return r.Code + ": " + r.Message }

// UIConfig is what the dashboard learns about the gateway before anyone signs
// in, from GET /ui-config: what to call it, and which optional parts to show.
type UIConfig struct {
	Name string `json:"name"`
	// Features names optional parts of the interface the gateway supports.
	Features []string `json:"features"`
}

func (s *Server) handleUIConfig(w http.ResponseWriter, _ *http.Request) {
	cfg := s.opts.UIConfig
	if cfg.Name == "" {
		cfg.Name = "Relaix"
	}
	if cfg.Features == nil {
		cfg.Features = []string{}
	}
	writeJSON(w, http.StatusOK, cfg)
}
