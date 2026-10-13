package grpcserver

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/KGMA74/relaix-server/events"
	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/store"
)

// maxRunSteps bounds a reported run: an automation has at most a handful of
// actions, so anything longer is not one of ours.
const maxRunSteps = 20

// onAutomationRun records what an automation did on the phone, and
// acknowledges it so the phone forgets it. Like received SMS, a failure to
// record is not acknowledged: the phone reports the run again later.
func (s *Server) onAutomationRun(ctx context.Context, device *store.Device, m *v1.AutomationRun, log *slog.Logger) {
	ref := m.GetId()
	if ref == "" || len(ref) > maxInboundRefLen {
		log.Warn("automation run without a usable id; dropped")
		return
	}
	ack := func() {
		msg := &v1.ServerMessage{
			MessageId: uuid.NewString(),
			SentAt:    timestamppb.New(s.opts.Now()),
			Payload:   &v1.ServerMessage_AutomationRunAck{AutomationRunAck: &v1.AutomationRunAck{Ids: []string{ref}}},
		}
		if err := s.hub.Send(ctx, device.ID, msg); err != nil {
			log.Warn("could not acknowledge an automation run", "err", err)
		}
	}
	if len(m.GetSteps()) > maxRunSteps {
		log.Warn("automation run with too many steps; dropped", "steps", len(m.GetSteps()))
		ack()
		return
	}

	now := s.opts.Now()
	run := &store.AutomationRun{
		AccountID:    device.AccountID,
		DeviceID:     &device.ID,
		DeviceRef:    ref,
		AutomationID: m.GetAutomationId(),
		Sender:       m.GetSender(),
		StartedAt:    now,
		FinishedAt:   now,
		OK:           m.GetOk(),
		Error:        m.GetError(),
	}
	if ts := m.GetStartedAt(); ts != nil {
		run.StartedAt = ts.AsTime()
	}
	if ts := m.GetFinishedAt(); ts != nil {
		run.FinishedAt = ts.AsTime()
	}
	for _, st := range m.GetSteps() {
		run.Steps = append(run.Steps, store.AutomationStep{
			Action: st.GetAction(), OK: st.GetOk(), Detail: st.GetDetail(), Responses: st.GetResponses(),
		})
	}

	stored, created, err := s.store.AutomationRuns().Record(ctx, run)
	if err != nil {
		log.Warn("could not record an automation run; the phone will resend", "err", err)
		return
	}
	if created {
		s.opts.Events.Publish(events.Event{
			Type: events.AutomationRun, DeviceID: device.ID.String(), MessageID: stored.ID.String(), AccountID: stored.AccountID,
		})
	}
	ack()
}
