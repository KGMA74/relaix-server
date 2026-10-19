package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/KGMA74/relaix-server/events"
	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/hub"
	"github.com/KGMA74/relaix-server/policy"
	"github.com/KGMA74/relaix-server/store"
)

// handlePutPolicy replaces a device's rules.
//
//	PUT /devices/{id}/policy
//	{"availability": {...}, "accept": {...}, "sims": [...]}
//
// The body is the whole policy — what is left out is no longer restricted.
// Answers the stored version, with its new version number. A connected phone
// gets it at once; an offline one with its next RegisterAck. Either way the
// scheduler applies it from its next tick.
func (s *Server) handlePutPolicy(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parseID(w, r)
	if !ok {
		return
	}

	var in policy.Policy
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "body is not valid JSON")
		return
	}
	in = policy.Normalize(in)
	if err := policy.Validate(in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_policy", err.Error())
		return
	}

	ctx := r.Context()
	dev, ok := s.ownedDevice(w, r, id)
	if !ok {
		return
	}
	locks, err := s.locksFor(ctx, id)
	if err != nil {
		s.opts.Logger.Error("could not read locks", "device_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not store policy")
		return
	}
	if writeLockError(w, policy.CheckLocks(dev.Policy, in, locks)) {
		return
	}
	stored, err := s.store.Devices().SetPolicy(ctx, id, in)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such device")
		return
	case err != nil:
		s.opts.Logger.Error("could not store policy", "device_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not store policy")
		return
	}

	s.pushPolicy(ctx, id, stored, dev.Can(store.CapAutomationSchedule))
	s.opts.Logger.Info("device rules updated", "device_id", id, "version", stored.Version)
	s.opts.Events.Publish(events.Event{Type: events.DeviceUpdated, DeviceID: id.String(), AccountID: dev.AccountID})
	writeJSON(w, http.StatusOK, stored)
}

// pushPolicy sends the stored rules to the phone if it is connected, so the
// screen on the phone shows them without waiting for a reconnect.
func (s *Server) pushPolicy(ctx context.Context, id uuid.UUID, p policy.Policy, scheduling bool) {
	err := s.hub.Send(ctx, id, &v1.ServerMessage{
		MessageId: uuid.NewString(),
		SentAt:    timestamppb.New(s.opts.Now()),
		Payload:   &v1.ServerMessage_Policy{Policy: policy.ToProtoFor(p, scheduling)},
	})
	if err != nil && !errors.Is(err, hub.ErrNotConnected) {
		s.opts.Logger.Warn("could not push rules to the device", "device_id", id, "err", err)
	}
}

// handleCountries lists the countries the rules can name, with the calling
// code prefixes each is matched on.
//
//	GET /countries
func (s *Server) handleCountries(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "max-age=86400")
	writeJSON(w, http.StatusOK, map[string]any{"countries": policy.Countries()})
}
