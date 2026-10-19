package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/events"
	"github.com/KGMA74/relaix-server/policy"
	"github.com/KGMA74/relaix-server/store"
)

// LockSource says which of a device's automations its rules must keep as
// they are (policy.Locks). Nil locks none.
type LockSource func(ctx context.Context, device uuid.UUID) (policy.Locks, error)

func (s *Server) locksFor(ctx context.Context, device uuid.UUID) (policy.Locks, error) {
	if s.opts.Locks == nil {
		return nil, nil
	}
	return s.opts.Locks(ctx, device)
}

// handleLocks lists the locked automations of the caller's devices, for the
// dashboard to show them as such.
//
//	GET /automations/locks → {"locks": {"<device id>": {"<automation id>": "why"}}}
func (s *Server) handleLocks(w http.ResponseWriter, r *http.Request) {
	out := map[string]policy.Locks{}
	if s.opts.Locks != nil {
		ctx := r.Context()
		list, err := s.store.Devices().List(ctx, principal(r).Scope())
		if err != nil {
			s.opts.Logger.Error("could not list devices", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "could not list locks")
			return
		}
		for _, d := range list {
			locks, err := s.opts.Locks(ctx, d.ID)
			if err != nil {
				s.opts.Logger.Error("could not read locks", "device_id", d.ID, "err", err)
				writeError(w, http.StatusInternalServerError, "internal", "could not list locks")
				return
			}
			if len(locks) > 0 {
				out[d.ID.String()] = locks
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"locks": out})
}

// writeLockError answers a change to a locked automation.
func writeLockError(w http.ResponseWriter, err error) bool {
	var lock *policy.LockError
	if !errors.As(err, &lock) {
		return false
	}
	writeError(w, http.StatusConflict, "automation_locked", lock.Error())
	return true
}

// ReplacePolicy edits a device's rules from inside the program — no request,
// no locks: an embedding program removing the automation it locked, say.
// Validated, stored and pushed to the phone like PUT /devices/{id}/policy.
func (s *Server) ReplacePolicy(ctx context.Context, device uuid.UUID, edit func(policy.Policy) policy.Policy) (policy.Policy, error) {
	dev, err := s.store.Devices().Get(ctx, device)
	if err != nil {
		return policy.Policy{}, err
	}
	next := policy.Normalize(edit(dev.Policy))
	if err := policy.Validate(next); err != nil {
		return policy.Policy{}, err
	}
	stored, err := s.store.Devices().SetPolicy(ctx, device, next)
	if err != nil {
		return policy.Policy{}, err
	}
	s.pushPolicy(ctx, device, stored, dev.Can(store.CapAutomationSchedule))
	s.opts.Events.Publish(events.Event{Type: events.DeviceUpdated, DeviceID: device.String(), AccountID: dev.AccountID})
	return stored, nil
}
