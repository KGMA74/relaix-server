package grpcserver

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/KGMA74/relaix-server/events"
	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/policy"
	"github.com/KGMA74/relaix-server/store"
)

func TestReceivedSMSIsRecordedOnceAndAcknowledged(t *testing.T) {
	h := newHarness(t, func(o *Options) {
		o.InboundWebhook = func(context.Context, uuid.UUID) (string, error) { return "https://example.com/in", nil }
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.store.Devices().SetPolicy(ctx, h.device.ID, policy.Policy{
		Inbound: policy.Inbound{Enabled: true, Senders: []string{"OrangeMoney"}},
	}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	sub, unsubscribe := h.events.Subscribe()
	defer unsubscribe()

	stream, ack := h.connect(t, ctx, deviceToken)
	if ack == nil || !ack.GetAccepted() {
		t.Fatal("registration failed")
	}
	received := time.Date(2026, 7, 30, 11, 59, 0, 0, time.UTC)
	send := func(ref, sender string) {
		t.Helper()
		err := stream.Send(&v1.DeviceMessage{
			MessageId: uuid.NewString(), DeviceToken: deviceToken,
			Payload: &v1.DeviceMessage_SmsReceived{SmsReceived: &v1.SmsReceived{
				Id: ref, Sender: sender, Body: "Vous avez recu 5000 FCFA", ReceivedAt: timestamppb.New(received), SubscriptionId: 2,
			}},
		})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	acked := func(want string) {
		t.Helper()
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if ids := msg.GetSmsReceivedAck().GetIds(); len(ids) != 1 || ids[0] != want {
			t.Fatalf("got %v, want an ack for %s", msg, want)
		}
	}

	send("a1", "orangemoney")
	acked("a1")
	send("a1", "orangemoney") // a resend after a lost ack
	acked("a1")
	send("b2", "Maman") // not covered by the rules
	acked("b2")

	mine := h.device.AccountID
	listed, err := h.store.Inbound().List(ctx, store.InboundFilter{AccountID: &mine})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("recorded %d messages, want 1", len(listed))
	}
	m := listed[0]
	if m.Sender != "orangemoney" || !m.ReceivedAt.Equal(received) || m.SubscriptionID == nil || *m.SubscriptionID != 2 {
		t.Errorf("recorded %+v", m)
	}
	if m.Webhook.URL != "https://example.com/in" || m.Webhook.NextAttemptAt == nil {
		t.Errorf("webhook = %+v, want due at once", m.Webhook)
	}
	waitForEvent(t, sub, events.SmsReceived, h.device.ID.String())
}

func TestAutomationForwardKeepsFieldsAndIsCheckedAgainstTheRules(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.store.Devices().SetPolicy(ctx, h.device.ID, policy.Policy{Automations: []policy.Automation{{
		ID: "pay", Name: "paiement", Enabled: true, Senders: []string{"OrangeMoney"},
		Pattern: `recu (?<amount>[0-9 ]+) FCFA`, Actions: []policy.Action{{Type: policy.ActionForward}},
	}}}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	stream, ack := h.connect(t, ctx, deviceToken)
	if ack == nil || !ack.GetAccepted() {
		t.Fatal("registration failed")
	}
	send := func(ref, automation, body string) {
		t.Helper()
		err := stream.Send(&v1.DeviceMessage{
			MessageId: uuid.NewString(), DeviceToken: deviceToken,
			Payload: &v1.DeviceMessage_SmsReceived{SmsReceived: &v1.SmsReceived{
				Id: ref, Sender: "OrangeMoney", Body: body, AutomationId: automation,
				Fields: map[string]string{"amount": "5 000"},
			}},
		})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		if _, err := stream.Recv(); err != nil {
			t.Fatalf("ack: %v", err)
		}
	}
	send("ok", "pay", "Vous avez recu 5 000 FCFA")
	send("nomatch", "pay", "Bonjour")
	send("unknown", "other", "Vous avez recu 5 000 FCFA")

	mine := h.device.AccountID
	listed, err := h.store.Inbound().List(ctx, store.InboundFilter{AccountID: &mine})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed) != 1 || listed[0].DeviceRef != "ok" || listed[0].AutomationID != "pay" || listed[0].Fields["amount"] != "5 000" {
		t.Fatalf("recorded %+v, want the matching one with its fields", listed)
	}
}

func TestAutomationRunIsRecordedOnceAndAcknowledged(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stream, ack := h.connect(t, ctx, deviceToken)
	if ack == nil || !ack.GetAccepted() {
		t.Fatal("registration failed")
	}
	for range 2 { // the second is a resend after a lost ack
		err := stream.Send(&v1.DeviceMessage{
			MessageId: uuid.NewString(), DeviceToken: deviceToken,
			Payload: &v1.DeviceMessage_AutomationRun{AutomationRun: &v1.AutomationRun{
				Id: "run-1", AutomationId: "pay", Sender: "OrangeMoney", Ok: true,
				Steps: []*v1.AutomationStep{{Action: "ussd", Ok: true, Detail: "*144*{pin}#", Responses: []string{"OK"}}},
			}},
		})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		msg, err := stream.Recv()
		if err != nil || len(msg.GetAutomationRunAck().GetIds()) != 1 {
			t.Fatalf("ack = %v, %v", msg, err)
		}
	}
	runs, err := h.store.AutomationRuns().List(ctx, store.RunFilter{})
	if err != nil || len(runs) != 1 || runs[0].Steps[0].Detail != "*144*{pin}#" || runs[0].AccountID != h.device.AccountID {
		t.Fatalf("runs = %+v, %v", runs, err)
	}
}
