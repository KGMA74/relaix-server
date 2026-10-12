package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/events"
	"github.com/KGMA74/relaix-server/store"
)

// maxBodyRunes bounds an SMS body. 10 concatenated GSM-7 parts is already an
// expensive message; past that the caller almost certainly meant something else.
const maxBodyRunes = 1530

// maxRecipients bounds a single request.
//
// The limit is not about server capacity: it is that a consumer SIM sends on
// the order of tens of messages an hour, so a batch of thousands would be
// accepted and then sit queued for a day. Refusing it up front tells the
// caller something true, and keeps one request from writing an unbounded
// number of rows in one transaction.
const maxRecipients = 100

// sendRequest is the body of POST /send.
//
// Recipient and Recipients are two spellings of the same thing: exactly one
// must be present. The singular form is kept because it is what every existing
// caller sends, and the plural exists because a fleet's whole point is that
// the same message to fifty numbers can leave from several phones at once.
type sendRequest struct {
	Recipient   string   `json:"recipient,omitempty"`
	Recipients  []string `json:"recipients,omitempty"`
	Body        string   `json:"body"`
	DeviceID    *string  `json:"deviceId,omitempty"`
	Mode        string   `json:"mode,omitempty"`
	Priority    int      `json:"priority,omitempty"`
	ScheduledAt *string  `json:"scheduledAt,omitempty"`
	ExpiresAt   *string  `json:"expiresAt,omitempty"`
	CallbackURL string   `json:"callbackUrl,omitempty"`
	// SubscriptionID picks a SIM on a multi-SIM handset, using a value from
	// that device's reported sims. Omitted means the device's default SMS
	// subscription.
	SubscriptionID *int32 `json:"subscriptionId,omitempty"`
}

// jobResponse is what the API says about a job. It is deliberately not the
// store type: the database schema is free to change without breaking callers.
type jobResponse struct {
	JobID          string  `json:"jobId"`
	Status         string  `json:"status"`
	Recipient      string  `json:"recipient"`
	Body           string  `json:"body"`
	Mode           string  `json:"mode"`
	Priority       int     `json:"priority"`
	DeviceID       *string `json:"deviceId,omitempty"`
	SubscriptionID *int32  `json:"subscriptionId,omitempty"`
	// SentFromSubscriptionID is the SIM the job was handed to — the requested
	// one or the one the device's rules picked. Absent for the phone's
	// default, or before assignment.
	SentFromSubscriptionID *int32  `json:"sentFromSubscriptionId,omitempty"`
	Attempts               int     `json:"attempts"`
	PartsSent              int     `json:"partsSent"`
	ErrorCode              string  `json:"errorCode,omitempty"`
	ErrorMsg               string  `json:"errorMessage,omitempty"`
	ScheduledAt            *string `json:"scheduledAt,omitempty"`
	ExpiresAt              *string `json:"expiresAt,omitempty"`
	CreatedAt              string  `json:"createdAt"`
	CompletedAt            *string `json:"completedAt,omitempty"`
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principal(r)

	var req sendRequest
	if err := decodeJSON(w, r, s.opts.MaxBodyBytes, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	jobs, apiErr := s.buildJobs(&req)
	if apiErr != nil {
		writeError(w, apiErr.status, apiErr.code, apiErr.message)
		return
	}
	for _, j := range jobs {
		j.AccountID = p.AccountID
	}
	// The shape of the request decides the shape of the reply: a caller who
	// sent one recipient gets the object it always got, and only the plural
	// form gets a list.
	batch := len(req.Recipients) > 0
	job := jobs[0]

	// A device of another account is answered like one that does not exist.
	// The operator, who sees every account, keeps today's leniency: an
	// unknown id is accepted and the scheduler fails the job if it never
	// shows up.
	if job.RequestedDeviceID != nil && !p.AllAccounts {
		dev, err := s.store.Devices().Get(ctx, *job.RequestedDeviceID)
		if errors.Is(err, store.ErrNotFound) || (err == nil && !p.Owns(dev.AccountID)) {
			writeError(w, http.StatusBadRequest, "unknown_device", "no such device")
			return
		}
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "could not read device")
			return
		}
	}

	// Immediate means fail fast, and the caller learns now rather than from a
	// callback four minutes later: for an interactive flow like an OTP, a clean
	// error is worth more than a late message, because only a prompt error
	// lets the caller fall back to another channel.
	//
	// The scheduler enforces this too — a device can vanish between here and
	// the next tick — but checking here is what makes the promise synchronous.
	if job.Mode == store.ModeImmediate && job.ScheduledAt == nil {
		ok, err := s.hasReadyDevice(ctx, job.AccountID, job.RequestedDeviceID)
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

	// All or nothing: a caller who sent fifty numbers and got twelve jobs back
	// would have no way to know which twelve, and retrying would duplicate the
	// ones that did land.
	created := make([]*store.Job, 0, len(jobs))
	err := s.store.WithTx(ctx, func(tx store.Store) error {
		created = created[:0]
		for _, j := range jobs {
			c, err := tx.Jobs().Create(ctx, j)
			if err != nil {
				return err
			}
			created = append(created, c)
		}
		return nil
	})
	if err != nil {
		s.opts.Logger.Error("could not create jobs", "count", len(jobs), "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not create job")
		return
	}

	for _, c := range created {
		s.event(ctx, c.ID, store.JobPending, "submitted via api")
		s.opts.Events.Publish(events.Event{Type: events.JobCreated, JobID: c.ID.String(), Status: string(c.Status), AccountID: c.AccountID})
	}

	// 202, not 201: the gateway has accepted responsibility for the message,
	// not sent it. Saying 201 Created would invite callers to read it as done.
	if !batch {
		writeJSON(w, http.StatusAccepted, toJobResponse(created[0]))
		return
	}

	out := make([]jobResponse, 0, len(created))
	for _, c := range created {
		out = append(out, toJobResponse(c))
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"jobs": out})
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parseID(w, r)
	if !ok {
		return
	}

	job, ok := s.ownedJob(w, r, id)
	if !ok {
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
	owned, ok := s.ownedJob(w, r, id)
	if !ok {
		return
	}

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
	s.opts.Events.Publish(events.Event{Type: events.JobUpdated, JobID: id.String(), Status: string(store.JobCancelled), AccountID: owned.AccountID})

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

// parseRecipients resolves the singular and plural forms into one list.
//
// Exactly one form is allowed. Accepting both and silently merging them would
// make a caller who set the wrong field think they had sent more messages than
// they did.
func parseRecipients(req *sendRequest) ([]string, *apiError) {
	single := strings.TrimSpace(req.Recipient)

	switch {
	case single == "" && len(req.Recipients) == 0:
		return nil, badRequest("missing_recipient", "recipient or recipients is required")
	case single != "" && len(req.Recipients) > 0:
		return nil, badRequest("ambiguous_recipient",
			"use recipient or recipients, not both")
	}

	raw := req.Recipients
	if single != "" {
		raw = []string{single}
	}
	if len(raw) > maxRecipients {
		return nil, badRequest("too_many_recipients",
			fmt.Sprintf("at most %d recipients per request", maxRecipients))
	}

	out := make([]string, 0, len(raw))
	for i, r := range raw {
		r = strings.TrimSpace(r)
		// E.164 is checked here rather than left to the handset: a malformed
		// number would otherwise be discovered one device dispatch and one
		// failed send later, with the caller told nothing useful. The index is
		// named because finding the bad one in a list of fifty is the whole
		// difficulty.
		if !validE164(r) {
			return nil, badRequest("invalid_recipient",
				fmt.Sprintf("recipient %d must be E.164, e.g. +33600000000", i))
		}
		out = append(out, r)
	}
	return out, nil
}

// buildJobs turns one request into one job per recipient.
//
// Fanning out here rather than carrying a list on the job keeps "one job, one
// outcome" true, which is what GET /jobs/:id and the callbacks depend on — and
// it lets the scheduler spread a batch across several handsets instead of
// pinning it to whichever one took the job.
func (s *Server) buildJobs(req *sendRequest) ([]*store.Job, *apiError) {
	recipients, apiErr := parseRecipients(req)
	if apiErr != nil {
		return nil, apiErr
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

	// One prototype validated once, then copied per recipient: everything but
	// the number is shared, and validating the same schedule fifty times would
	// only multiply the ways the answers could disagree.
	job := &store.Job{
		Body:     req.Body,
		Mode:     mode,
		Priority: req.Priority,
		Status:   store.JobPending,
	}

	if req.DeviceID != nil && *req.DeviceID != "" {
		id, err := uuid.Parse(*req.DeviceID)
		if err != nil {
			return nil, badRequest("invalid_device_id", "deviceId must be a uuid")
		}
		job.RequestedDeviceID = &id
	}

	if req.SubscriptionID != nil {
		// Subscription ids are assigned by Android per handset, so the same
		// number means a different SIM on another phone. Naming one without
		// naming a device would let the scheduler pick a handset and then send
		// from whichever SIM happened to share that id — from a number the
		// caller never chose.
		if job.RequestedDeviceID == nil {
			return nil, badRequest("subscription_needs_device",
				"subscriptionId only means something with a deviceId: subscription ids are per handset")
		}
		if *req.SubscriptionID < 0 {
			return nil, badRequest("invalid_subscription_id",
				"subscriptionId must be a subscription the device reported")
		}
		sub := *req.SubscriptionID
		job.SubscriptionID = &sub
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

	jobs := make([]*store.Job, 0, len(recipients))
	for _, r := range recipients {
		// Copy by value: sharing one struct would give every job the same
		// pointer identity, and Create writing an id back would overwrite the
		// previous one.
		clone := *job
		clone.Recipient = r
		jobs = append(jobs, &clone)
	}
	return jobs, nil
}

// hasReadyDevice reports whether an immediate send could be placed right now.
// Only the devices the scheduler would hand the job count: enabled, and owned
// by an account the router lets carry it.
func (s *Server) hasReadyDevice(ctx context.Context, account uuid.UUID, requested *uuid.UUID) (bool, error) {
	ready, err := s.hub.ListReady(ctx)
	if err != nil || len(ready) == 0 {
		return false, err
	}
	carriers, err := s.opts.Router.Carriers(ctx, account)
	if err != nil {
		return false, err
	}
	all, err := s.store.Devices().List(ctx, store.AllAccounts())
	if err != nil {
		return false, err
	}
	usable := make(map[uuid.UUID]bool, len(all))
	for _, d := range all {
		if d.Enabled && slices.Contains(carriers, d.AccountID) {
			usable[d.ID] = true
		}
	}
	for _, d := range ready {
		if usable[d.DeviceID] && (requested == nil || d.DeviceID == *requested) {
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
		Body:      j.Body,
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
	resp.SubscriptionID = j.SubscriptionID
	resp.SentFromSubscriptionID = j.AssignedSubscriptionID
	if j.ScheduledAt != nil {
		at := j.ScheduledAt.UTC().Format(time.RFC3339)
		resp.ScheduledAt = &at
	}
	if j.ExpiresAt != nil {
		at := j.ExpiresAt.UTC().Format(time.RFC3339)
		resp.ExpiresAt = &at
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
