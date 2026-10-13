package grpcserver

import (
	"context"
	"log/slog"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/KGMA74/relaix-server/events"
	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/store"
)

// Bounds on what a phone may hand over as one received SMS. A concatenated
// SMS tops out around 40 000 characters in theory and a few hundred in
// practice; anything bigger is not a text message.
const (
	maxInboundBodyRunes = 10_000
	maxInboundSenderLen = 64
	maxInboundRefLen    = 128
)

// onSmsReceived records an SMS the phone forwarded and acknowledges it, so
// the agent stops resending it.
//
// The acknowledgement says "the server is done with this id", not "it was
// stored": a message the rules do not forward, or one too malformed to keep,
// is acknowledged too — resending it would only meet the same answer. What
// is not acknowledged is a failure on this side, a database error: the phone
// keeps the message and sends it again on its next connection.
func (s *Server) onSmsReceived(ctx context.Context, device *store.Device, m *v1.SmsReceived, log *slog.Logger) {
	ref := m.GetId()
	if ref == "" || len(ref) > maxInboundRefLen {
		log.Warn("received SMS without a usable id; dropped")
		return
	}
	log = log.With("ref", ref)
	ack := func() {
		msg := &v1.ServerMessage{
			MessageId: uuid.NewString(),
			SentAt:    timestamppb.New(s.opts.Now()),
			Payload:   &v1.ServerMessage_SmsReceivedAck{SmsReceivedAck: &v1.SmsReceivedAck{Ids: []string{ref}}},
		}
		if err := s.hub.Send(ctx, device.ID, msg); err != nil {
			log.Warn("could not acknowledge a received SMS", "err", err)
		}
	}

	// The rules as stored now, not as they were when the stream opened: an
	// operator who just turned forwarding off means it for this message too.
	current, err := s.store.Devices().Get(ctx, device.ID)
	if err != nil {
		log.Warn("could not read the device for a received SMS", "err", err)
		return
	}
	sender := m.GetSender()
	// Forwarded by an automation, or by the plain inbound rules: either way the
	// rules as stored now must agree, or the message is not kept.
	if id := m.GetAutomationId(); id != "" {
		a, ok := current.Policy.Automation(id)
		received := ""
		if ts := m.GetReceivedAt(); ts != nil {
			received = ts.AsTime().UTC().Format(time.RFC3339)
		}
		if _, matches := a.Match(sender, m.GetBody(), received, m.GetSubscriptionId()); !ok || !matches || !a.Forwards() {
			log.Info("received SMS not covered by the automation it names; dropped", "automation", id)
			ack()
			return
		}
	} else if !current.Policy.Inbound.Forwards(sender) {
		log.Info("received SMS not covered by the device's rules; dropped")
		ack()
		return
	}
	if sender == "" || len(sender) > maxInboundSenderLen || utf8.RuneCountInString(m.GetBody()) > maxInboundBodyRunes {
		log.Warn("received SMS malformed; dropped", "sender_len", len(sender), "body_len", len(m.GetBody()))
		ack()
		return
	}

	now := s.opts.Now()
	in := &store.InboundMessage{
		AccountID:    current.AccountID,
		DeviceID:     &device.ID,
		DeviceRef:    ref,
		Sender:       sender,
		Body:         m.GetBody(),
		ReceivedAt:   now,
		AutomationID: m.GetAutomationId(),
		Fields:       m.GetFields(),
	}
	if ts := m.GetReceivedAt(); ts != nil {
		in.ReceivedAt = ts.AsTime()
	}
	if sub := m.GetSubscriptionId(); sub != 0 {
		in.SubscriptionID = &sub
	}
	if s.opts.InboundWebhook != nil {
		url, err := s.opts.InboundWebhook(ctx, current.AccountID)
		if err != nil {
			log.Warn("could not resolve the inbound webhook; the phone will resend", "err", err)
			return
		}
		if url != "" {
			in.Webhook.URL = url
			in.Webhook.NextAttemptAt = &now
		}
	}

	stored, created, err := s.store.Inbound().Record(ctx, in)
	if err != nil {
		log.Warn("could not record a received SMS; the phone will resend", "err", err)
		return
	}
	if created {
		log.Info("SMS received", "message_id", stored.ID)
		s.opts.Events.Publish(events.Event{
			Type: events.SmsReceived, DeviceID: device.ID.String(), MessageID: stored.ID.String(), AccountID: stored.AccountID,
		})
	}
	ack()
}
