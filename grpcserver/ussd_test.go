package grpcserver

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/store"
)

func TestCapabilitiesAndUSSDResponsesAreRecorded(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	job := h.store.SeedJob(&store.Job{
		Kind: store.KindUSSD, Body: "*144#", Mode: store.ModeQueued,
		Status: store.JobAssigned, AssignedDeviceID: &h.device.ID,
	})

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	err = stream.Send(&v1.DeviceMessage{
		MessageId: uuid.NewString(), DeviceToken: deviceToken, SentAt: timestamppb.New(testNow),
		Payload: &v1.DeviceMessage_Register{Register: &v1.Register{
			DeviceInfo: &v1.DeviceInfo{Label: "test phone", Capabilities: []string{store.CapUSSD, store.CapInboundSMS}},
			Health:     healthyProto(), PendingJobIds: []string{job.ID.String()},
		}},
	})
	if err != nil {
		t.Fatalf("send Register: %v", err)
	}
	if _, err := stream.Recv(); err != nil {
		t.Fatalf("RegisterAck: %v", err)
	}

	waitFor(t, func() bool {
		d, err := h.store.Devices().Get(ctx, h.device.ID)
		return err == nil && slices.Equal(d.Capabilities, []string{store.CapUSSD, store.CapInboundSMS})
	}, "capabilities were not recorded")

	err = stream.Send(&v1.DeviceMessage{
		MessageId: uuid.NewString(), DeviceToken: deviceToken,
		Payload: &v1.DeviceMessage_JobResult{JobResult: &v1.JobResult{
			JobId: job.ID.String(), Status: v1.JobStatus_JOB_STATUS_SENT,
			Responses: []string{"1. Solde", "Votre solde est de 5000 FCFA"},
		}},
	})
	if err != nil {
		t.Fatalf("send result: %v", err)
	}
	waitFor(t, func() bool {
		got := h.store.JobByID(job.ID)
		return got.Status == store.JobSent && len(got.Responses) == 2 && got.Responses[1] == "Votre solde est de 5000 FCFA"
	}, "USSD responses were not recorded")
}
