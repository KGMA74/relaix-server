package api

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

// maxBodyRunes bounds an SMS body. 10 concatenated GSM-7 parts is already an
// expensive message; past that the caller almost certainly meant something else.
const maxBodyRunes = 1530

// sendRequest is the body of POST /send.
type sendRequest struct {
	Recipient   string  `json:"recipient"`
	Body        string  `json:"body"`
	DeviceID    *string `json:"deviceId,omitempty"`
	Mode        string  `json:"mode,omitempty"`
	Priority    int     `json:"priority,omitempty"`
	ScheduledAt *string `json:"scheduledAt,omitempty"`
	ExpiresAt   *string `json:"expiresAt,omitempty"`
	CallbackURL string  `json:"callbackUrl,omitempty"`
}

// jobResponse is what the API says about a job. It is deliberately not the
// store type: the database schema is free to change without breaking callers.
type jobResponse struct {
	JobID       string  `json:"jobId"`
	Status      string  `json:"status"`
	Recipient   string  `json:"recipient"`
	Mode        string  `json:"mode"`
	Priority    int     `json:"priority"`
	DeviceID    *string `json:"deviceId,omitempty"`
	Attempts    int     `json:"attempts"`
	PartsSent   int     `json:"partsSent"`
	ErrorCode   string  `json:"errorCode,omitempty"`
	ErrorMsg    string  `json:"errorMessage,omitempty"`
	ScheduledAt *string `json:"scheduledAt,omitempty"`
	CreatedAt   string  `json:"createdAt"`
	CompletedAt *string `json:"completedAt,omitempty"`
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	var req sendRequest
	if err := decodeJSON(w, r, s.opts.MaxBodyBytes, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	job, apiErr := s.buildJob(&req)
	if apiErr != nil {
		writeError(w, apiErr.status, apiErr.code, apiErr.message)
		return
	}

	// Immediate means fail fast, and the caller learns now rather than from a
	// callback four minutes later: for an interactive flow like an OTP, a clean
	// error is worth more than a late message, because only a prompt error
	// lets the caller fall back to another channel.
	//
	// The scheduler enforces this too — a device can vanish between here and
	// the next tick — but checking here is what makes the promise synchronous.
	if job.Mode == store.ModeImmediate && job.ScheduledAt == nil {
		ok, err := s.hasReadyDevice(ctx, job.RequestedDeviceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not check device availability")
			return
		}
		if !ok {
			writeError(w, http.StatusServiceUnavailable, "no_device_available",
				"no ready device for an immediate send")
			return
		}
	}

	created, err := s.store.Jobs().Create(ctx, job)
	if err != nil {
		s.opts.Logger.Error("could not create job", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create job")
		return
	}

	s.event(ctx, created.ID, store.JobPending, "submitted via api")

	// 202, not 201: the gateway has accepted responsibility for the message,
	// not sent it. Saying 201 Created would invite callers to read it as done.
	writeJSON(w, http.StatusAccepted, toJobResponse(created))
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parseID(w, r)
	if !ok {
		return
	}

	job, err := s.store.Jobs().Get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found", "no such job")
		return
	}
	if err != nil {
		s.opts.Logger.Error("could not read job", "job_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not read job")
		return
	}

	writeJSON(w, http.StatusOK, toJobResponse(job))
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parseID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()

	err := s.store.Jobs().Cancel(ctx, id, "cancelled via api")
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such job")
		return
	case errors.Is(err, store.ErrConflict):
		// There is no recalling a message the handset already passed to the
		// network, so this is a conflict rather than a failure to try.
		writeError(w, http.StatusConflict, "already_final",
			"job has already reached a terminal state")
		return
	case err != nil:
		s.opts.Logger.Error("could not cancel job", "job_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not cancel job")
		return
	}

	s.event(ctx, id, store.JobCancelled, "cancelled via api")

	job, err := s.store.Jobs().Get(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, toJobResponse(job))
}

// apiError carries a validation failure back to the handler.
type apiError struct {
	status  int
	code    string
	message string
}

func badRequest(code, message string) *apiError {
	return &apiError{status: http.StatusBadRequest, code: code, message: message}
}

// buildJob validates a request and turns it into a job.
func (s *Server) buildJob(req *sendRequest) (*store.Job, *apiError) {
	recipient := strings.TrimSpace(req.Recipient)
	if recipient == "" {
		return nil, badRequest("missing_recipient", "recipient is required")
	}
	// E.164, checked here rather than left to the handset: a malformed number
	// would otherwise be discovered one device dispatch and one failed send
	// later, with the caller told nothing useful.
	if !validE164(recipient) {
		return nil, badRequest("invalid_recipient",
			"recipient must be E.164, e.g. +33600000000")
	}

	if req.Body == "" {
		return nil, badRequest("missing_body", "body is required")
	}
	if utf8.RuneCountInString(req.Body) > maxBodyRunes {
		return nil, badRequest("body_too_long", "body exceeds the maximum length")
	}

	mode := store.JobMode(req.Mode)
	if req.Mode == "" {
		// Queued by default: eventual delivery is the safer assumption, and a
		// caller who genuinely cannot wait says so explicitly.
		mode = store.ModeQueued
	}
	if mode != store.ModeQueued && mode != store.ModeImmediate {
		return nil, badRequest("invalid_mode", `mode must be "immediate" or "queued"`)
	}

	job := &store.Job{
		Recipient: recipient,
		Body:      req.Body,
		Mode:      mode,
		Priority:  req.Priority,
		Status:    store.JobPending,
	}

	if req.DeviceID != nil && *req.DeviceID != "" {
		id, err := uuid.Parse(*req.DeviceID)
		if err != nil {
			return nil, badRequest("invalid_device_id", "deviceId must be a uuid")
		}
		job.RequestedDeviceID = &id
	}

	now := s.opts.Now()

	if req.ScheduledAt != nil && *req.ScheduledAt != "" {
		at, err := time.Parse(time.RFC3339, *req.ScheduledAt)
		if err != nil {
			return nil, badRequest("invalid_scheduled_at", "scheduledAt must be RFC3339")
		}
		if at.After(now) {
			job.ScheduledAt = &at
		}
		// A scheduledAt in the past is treated as "now" rather than refused:
		// clocks drift, and a caller aiming at this second should not be
		// punished for losing a race with their own request.
	}

	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		at, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			return nil, badRequest("invalid_expires_at", "expiresAt must be RFC3339")
		}
		if !at.After(now) {
			return nil, badRequest("expires_in_past", "expiresAt is already in the past")
		}
		if job.ScheduledAt != nil && !at.After(*job.ScheduledAt) {
			return nil, badRequest("expires_before_scheduled",
				"expiresAt is not after scheduledAt, so the job could never be sent")
		}
		job.ExpiresAt = &at
	}

	if req.CallbackURL != "" {
		if !validCallbackURL(req.CallbackURL) {
			return nil, badRequest("invalid_callback_url",
				"callbackUrl must be an absolute http or https URL")
		}
		job.Callback.URL = req.CallbackURL
		// Due immediately: the watcher fires as soon as the job is terminal,
		// and until then the claim query skips it anyway.
		at := now
		job.Callback.NextAttemptAt = &at
	}

	return job, nil
}

// hasReadyDevice reports whether an immediate send could be placed right now.
func (s *Server) hasReadyDevice(ctx context.Context, requested *uuid.UUID) (bool, error) {
	ready, err := s.hub.ListReady(ctx)
	if err != nil {
		return false, err
	}
	if requested == nil {
		return len(ready) > 0, nil
	}
	for _, d := range ready {
		if d.DeviceID == *requested {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) parseID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "id must be a uuid")
		return uuid.Nil, false
	}
	return id, true
}

func (s *Server) event(ctx context.Context, jobID uuid.UUID, status store.JobStatus, reason string) {
	e := &store.JobEvent{
		JobID:     jobID,
		Status:    status,
		Reason:    reason,
		CreatedAt: s.opts.Now(),
	}
	if err := s.store.Events().Append(ctx, e); err != nil {
		s.opts.Logger.Warn("could not record job event", "job_id", jobID, "err", err)
	}
}

func toJobResponse(j *store.Job) jobResponse {
	resp := jobResponse{
		JobID:     j.ID.String(),
		Status:    string(j.Status),
		Recipient: j.Recipient,
		Mode:      string(j.Mode),
		Priority:  j.Priority,
		Attempts:  j.Attempts,
		PartsSent: j.PartsSent,
		ErrorCode: j.ErrorCode,
		ErrorMsg:  j.ErrorMessage,
		CreatedAt: j.CreatedAt.UTC().Format(time.RFC3339),
	}
	if j.AssignedDeviceID != nil {
		id := j.AssignedDeviceID.String()
		resp.DeviceID = &id
	} else if j.RequestedDeviceID != nil {
		id := j.RequestedDeviceID.String()
		resp.DeviceID = &id
	}
	if j.ScheduledAt != nil {
		at := j.ScheduledAt.UTC().Format(time.RFC3339)
		resp.ScheduledAt = &at
	}
	if j.CompletedAt != nil {
		at := j.CompletedAt.UTC().Format(time.RFC3339)
		resp.CompletedAt = &at
	}
	return resp
}

// validE164 checks the shape only: a plus sign then 8 to 15 digits, first
// non-zero. Whether the number is reachable is the carrier's answer to give.
func validE164(s string) bool {
	if len(s) < 9 || len(s) > 16 || s[0] != '+' {
		return false
	}
	if s[1] == '0' {
		return false
	}
	for _, r := range s[1:] {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// validCallbackURL requires an absolute http(s) URL. Relative or exotic schemes
// are refused now rather than failing on every watcher attempt later.
func validCallbackURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
