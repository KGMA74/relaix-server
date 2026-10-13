package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

type runResponse struct {
	RunID        string                 `json:"runId"`
	DeviceID     *string                `json:"deviceId,omitempty"`
	AutomationID string                 `json:"automationId"`
	Sender       string                 `json:"sender,omitempty"`
	StartedAt    string                 `json:"startedAt"`
	FinishedAt   string                 `json:"finishedAt"`
	OK           bool                   `json:"ok"`
	Error        string                 `json:"error,omitempty"`
	Steps        []store.AutomationStep `json:"steps"`
}

// handleListRuns serves what the phones' automations did, most recent first.
//
//	GET /automations/runs?deviceId=…&automationId=…&limit=50
func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.RunFilter{Limit: defaultListLimit, AutomationID: q.Get("automationId")}
	if p := principal(r); !p.AllAccounts {
		f.AccountID = &p.AccountID
	}
	if raw := q.Get("deviceId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_device_id", "deviceId must be a uuid")
			return
		}
		f.DeviceID = &id
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
			return
		}
		f.Limit = min(n, maxListLimit)
	}
	runs, err := s.store.AutomationRuns().List(r.Context(), f)
	if err != nil {
		s.opts.Logger.Error("could not list automation runs", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list automation runs")
		return
	}
	out := make([]runResponse, 0, len(runs))
	for _, run := range runs {
		resp := runResponse{
			RunID: run.ID.String(), AutomationID: run.AutomationID, Sender: run.Sender,
			StartedAt: run.StartedAt.UTC().Format(time.RFC3339), FinishedAt: run.FinishedAt.UTC().Format(time.RFC3339),
			OK: run.OK, Error: run.Error, Steps: run.Steps,
		}
		if resp.Steps == nil {
			resp.Steps = []store.AutomationStep{}
		}
		if run.DeviceID != nil {
			id := run.DeviceID.String()
			resp.DeviceID = &id
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}
