package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

const (
	// defaultListLimit and maxListLimit bound one page of GET /jobs. The cap
	// keeps a single request from serialising the whole table, whatever the
	// caller asks for.
	defaultListLimit = 50
	maxListLimit     = 200

	// maxStatsWindow bounds GET /stats. The count is a scan of every job in the
	// window; a year of history is not what an overview card is for.
	maxStatsWindow = 90 * 24 * time.Hour
)

// listJobsResponse is one page of history. NextCursor is absent on the last
// page, so a client loops until it is missing rather than until a short page —
// a page can be exactly full and still be the last.
type listJobsResponse struct {
	Jobs       []jobResponse `json:"jobs"`
	NextCursor string        `json:"nextCursor,omitempty"`
}

// handleListJobs serves the sent-message history, newest first.
//
//	GET /jobs?status=sent,failed&deviceId=…&recipient=+237&from=…&to=…&limit=50&cursor=…
func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	f, apiErr := parseJobFilter(r)
	if apiErr != nil {
		writeError(w, apiErr.status, apiErr.code, apiErr.message)
		return
	}

	// One extra row tells whether another page exists without a second query.
	limit := f.Limit
	f.Limit = limit + 1

	jobs, err := s.store.Jobs().List(r.Context(), f)
	if err != nil {
		s.opts.Logger.Error("could not list jobs", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list jobs")
		return
	}

	resp := listJobsResponse{Jobs: make([]jobResponse, 0, min(len(jobs), limit))}
	if len(jobs) > limit {
		jobs = jobs[:limit]
		resp.NextCursor = encodeCursor(store.CursorOf(jobs[len(jobs)-1]))
	}
	for _, j := range jobs {
		resp.Jobs = append(resp.Jobs, toJobResponse(j))
	}
	writeJSON(w, http.StatusOK, resp)
}

func parseJobFilter(r *http.Request) (store.JobFilter, *apiError) {
	q := r.URL.Query()
	f := store.JobFilter{Limit: defaultListLimit}

	if raw := q.Get("status"); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			st := store.JobStatus(strings.TrimSpace(part))
			if !knownStatus(st) {
				return f, badRequest("invalid_status", "unknown status "+strconv.Quote(string(st)))
			}
			f.Statuses = append(f.Statuses, st)
		}
	}
	if raw := q.Get("deviceId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			return f, badRequest("invalid_device_id", "deviceId must be a uuid")
		}
		f.DeviceID = &id
	}
	f.RecipientPrefix = strings.TrimSpace(q.Get("recipient"))

	for _, bound := range []struct {
		name string
		dst  **time.Time
	}{{"from", &f.CreatedFrom}, {"to", &f.CreatedTo}} {
		raw := q.Get(bound.name)
		if raw == "" {
			continue
		}
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return f, badRequest("invalid_"+bound.name, bound.name+" must be RFC 3339")
		}
		*bound.dst = &at
	}

	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			return f, badRequest("invalid_limit", "limit must be a positive integer")
		}
		f.Limit = min(n, maxListLimit)
	}
	if raw := q.Get("cursor"); raw != "" {
		c, err := decodeCursor(raw)
		if err != nil {
			return f, badRequest("invalid_cursor", "cursor is not one this server issued")
		}
		f.After = c
	}
	return f, nil
}

func knownStatus(s store.JobStatus) bool {
	switch s {
	case store.JobPending, store.JobAssigned, store.JobSent,
		store.JobDelivered, store.JobFailed, store.JobCancelled:
		return true
	}
	return false
}

// encodeCursor makes a page position opaque. Callers only ever hand it back,
// so its shape can change without breaking anyone; nanosecond precision keeps
// it exact against whatever resolution the store keeps.
func encodeCursor(c *store.JobCursor) string {
	raw := c.CreatedAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (*store.JobCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok {
		return nil, errors.New("malformed cursor")
	}
	t, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, err
	}
	u, err := uuid.Parse(id)
	if err != nil {
		return nil, err
	}
	return &store.JobCursor{CreatedAt: t, ID: u}, nil
}

// eventResponse is one entry of a job's timeline.
type eventResponse struct {
	Status    string  `json:"status,omitempty"`
	DeviceID  *string `json:"deviceId,omitempty"`
	Reason    string  `json:"reason,omitempty"`
	CreatedAt string  `json:"createdAt"`
}

// handleJobEvents returns the audit trail behind a job's current state: when
// it was queued, which phone took it, why it was retried or refused.
func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parseID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	// An unknown job and a job with no events would both list as empty; only
	// one of them is a mistake on the caller's side.
	if _, err := s.store.Jobs().Get(ctx, id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no such job")
		return
	} else if err != nil {
		s.opts.Logger.Error("could not read job", "job_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not read job")
		return
	}

	events, err := s.store.Events().ListByJob(ctx, id)
	if err != nil {
		s.opts.Logger.Error("could not list job events", "job_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list job events")
		return
	}

	out := make([]eventResponse, 0, len(events))
	for _, e := range events {
		resp := eventResponse{
			Status:    string(e.Status),
			Reason:    e.Reason,
			CreatedAt: e.CreatedAt.UTC().Format(time.RFC3339Nano),
		}
		if e.DeviceID != nil {
			d := e.DeviceID.String()
			resp.DeviceID = &d
		}
		out = append(out, resp)
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// statsResponse is what an overview screen needs in one round trip.
type statsResponse struct {
	Since   string         `json:"since"`
	Jobs    map[string]int `json:"jobs"`
	Total   int            `json:"total"`
	Devices deviceCounts   `json:"devices"`
}

type deviceCounts struct {
	Total     int `json:"total"`
	Enabled   int `json:"enabled"`
	Connected int `json:"connected"`
	Ready     int `json:"ready"`
}

// handleStats counts jobs per status over a recent window and summarises the
// fleet.
//
//	GET /stats?window=24h
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	window := 24 * time.Hour
	if raw := r.URL.Query().Get("window"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 || d > maxStatsWindow {
			writeError(w, http.StatusBadRequest, "invalid_window",
				"window must be a positive duration of at most 2160h")
			return
		}
		window = d
	}
	ctx := r.Context()
	since := s.opts.Now().Add(-window)

	counts, err := s.store.Jobs().CountByStatus(ctx, since)
	if err != nil {
		s.opts.Logger.Error("could not count jobs", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not compute stats")
		return
	}
	stored, err := s.store.Devices().List(ctx)
	if err != nil {
		s.opts.Logger.Error("could not list devices", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not compute stats")
		return
	}
	connected, err := s.hub.ListConnected(ctx)
	if err != nil {
		s.opts.Logger.Error("could not list connections", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not compute stats")
		return
	}
	ready, err := s.hub.ListReady(ctx)
	if err != nil {
		s.opts.Logger.Error("could not list ready devices", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not compute stats")
		return
	}

	// Every status is present, zero or not, so a client can render a fixed
	// set of cards without guessing which keys may be missing.
	resp := statsResponse{
		Since: since.UTC().Format(time.RFC3339),
		Jobs:  make(map[string]int),
		Devices: deviceCounts{
			Total:     len(stored),
			Connected: len(connected),
			Ready:     len(ready),
		},
	}
	for _, st := range []store.JobStatus{
		store.JobPending, store.JobAssigned, store.JobSent,
		store.JobDelivered, store.JobFailed, store.JobCancelled,
	} {
		resp.Jobs[string(st)] = counts[st]
		resp.Total += counts[st]
	}
	for _, d := range stored {
		if d.Enabled {
			resp.Devices.Enabled++
		}
	}
	writeJSON(w, http.StatusOK, resp)
}
