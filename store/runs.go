package store

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// AutomationRun is what one of a phone's automations did with one received
// SMS, as the phone reported it.
type AutomationRun struct {
	ID        uuid.UUID
	AccountID uuid.UUID
	// DeviceID is the phone that ran it; nil once that phone is deleted.
	DeviceID *uuid.UUID
	// DeviceRef is the id the phone gave the run, stable across resends.
	DeviceRef    string
	AutomationID string
	Sender       string
	StartedAt    time.Time
	FinishedAt   time.Time
	OK           bool
	Error        string
	Steps        []AutomationStep
	CreatedAt    time.Time
}

// AutomationStep is the outcome of one action of a run.
type AutomationStep struct {
	Action    string   `json:"action"`
	OK        bool     `json:"ok"`
	Detail    string   `json:"detail,omitempty"`
	Responses []string `json:"responses,omitempty"`
}

// RunFilter narrows AutomationRunStore.List; the zero value matches all.
type RunFilter struct {
	AccountID    *uuid.UUID
	DeviceID     *uuid.UUID
	AutomationID string
	Limit        int
}

// AutomationRunStore keeps the runs phones report.
type AutomationRunStore interface {
	// Record stores a run once per device and DeviceRef, reporting whether
	// this call created it.
	Record(ctx context.Context, r *AutomationRun) (*AutomationRun, bool, error)
	// List returns runs matching f, most recently started first.
	List(ctx context.Context, f RunFilter) ([]*AutomationRun, error)
}
