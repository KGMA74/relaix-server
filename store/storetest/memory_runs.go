package storetest

import (
	"bytes"
	"context"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

// AutomationRuns implements store.Store.
func (s *Store) AutomationRuns() store.AutomationRunStore { return runStore{s} }

var _ store.AutomationRunStore = runStore{}

type runStore struct{ s *Store }

func (r runStore) Record(ctx context.Context, in *store.AutomationRun) (*store.AutomationRun, bool, error) {
	defer r.s.lock()()
	if err := r.s.takeFailure(); err != nil {
		return nil, false, err
	}
	for _, x := range r.s.runs {
		if x.DeviceID != nil && in.DeviceID != nil && *x.DeviceID == *in.DeviceID && x.DeviceRef == in.DeviceRef {
			return copyRun(x), false, nil
		}
	}
	cp := copyRun(in)
	if cp.ID == uuid.Nil {
		cp.ID = uuid.New()
	}
	cp.AccountID = store.AccountOrDefault(cp.AccountID)
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now()
	}
	r.s.runs[cp.ID] = cp
	return copyRun(cp), true, nil
}

func (r runStore) List(ctx context.Context, f store.RunFilter) ([]*store.AutomationRun, error) {
	defer r.s.lock()()
	var out []*store.AutomationRun
	for _, x := range r.s.runs {
		switch {
		case f.AccountID != nil && x.AccountID != *f.AccountID:
		case f.DeviceID != nil && (x.DeviceID == nil || *x.DeviceID != *f.DeviceID):
		case f.AutomationID != "" && x.AutomationID != f.AutomationID:
		default:
			out = append(out, copyRun(x))
		}
	}
	slices.SortFunc(out, func(a, b *store.AutomationRun) int {
		if c := b.StartedAt.Compare(a.StartedAt); c != 0 {
			return c
		}
		return bytes.Compare(b.ID[:], a.ID[:])
	})
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func copyRun(x *store.AutomationRun) *store.AutomationRun {
	cp := *x
	cp.DeviceID = copyUUID(x.DeviceID)
	cp.Steps = make([]store.AutomationStep, len(x.Steps))
	for i, s := range x.Steps {
		s.Responses = slices.Clone(s.Responses)
		cp.Steps[i] = s
	}
	return &cp
}
