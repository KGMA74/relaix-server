package grpcserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/hub"
	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/store/storetest"
)

var testNow = time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

const deviceToken = "device-token-plaintext"

// harness runs a real gRPC server over an in-memory listener, with a real hub.
// The stream lifecycle is the thing under test, so faking the transport would
// test the wrong layer.
type harness struct {
	store  *storetest.Store
	hub    *hub.Hub
	client v1.DeviceGatewayClient
	device *store.Device
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := storetest.New()
	hasher := SHA256Hasher{}

	device := st.SeedDevice(&store.Device{Label: "test phone", Enabled: true})
	// SeedDevice bypasses Create, so register the token mapping through it.
	if _, err := st.Devices().Create(context.Background(), &store.Device{
		ID: device.ID, Label: device.Label, Enabled: true,
	}, hasher.Hash(deviceToken)); err != nil {
		t.Fatalf("seed device token: %v", err)
	}

	h := hub.New(hub.Options{
		Now:    func() time.Time { return testNow },
		Logger: logger,
	})
	hubCtx, stopHub := context.WithCancel(context.Background())
	hubDone := make(chan struct{})
	go func() { defer close(hubDone); _ = h.Run(hubCtx) }()

	srv := New(st, h, hasher, Options{
		HeartbeatInterval: 30 * time.Second,
		Now:               func() time.Time { return testNow },
		Logger:            logger,
	})

	lis := bufconn.Listen(1024 * 1024)
	gs := grpc.NewServer()
	v1.RegisterDeviceGatewayServer(gs, srv)
	serveDone := make(chan struct{})
	go func() { defer close(serveDone); _ = gs.Serve(lis) }()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	t.Cleanup(func() {
		_ = conn.Close()
		gs.Stop()
		<-serveDone
		stopHub()
		<-hubDone
	})

	return &harness{store: st, hub: h, client: v1.NewDeviceGatewayClient(conn), device: device}
}

func healthyProto() *v1.DeviceHealth {
	return &v1.DeviceHealth{
		BatteryLevel:   80,
		SignalStrength: 4,
		NetworkType:    "LTE",
		SimReady:       true,
		PermissionsOk:  true,
	}
}

// connect opens a stream and sends the initial Register.
func (h *harness) connect(t *testing.T, ctx context.Context, token string, pending ...string) (v1.DeviceGateway_ConnectClient, *v1.RegisterAck) {
	t.Helper()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	err = stream.Send(&v1.DeviceMessage{
		MessageId:   uuid.NewString(),
		DeviceToken: token,
		SentAt:      timestamppb.New(testNow),
		Payload: &v1.DeviceMessage_Register{Register: &v1.Register{
			DeviceInfo:    &v1.DeviceInfo{Label: "test phone", PhoneNumber: "+33600000000"},
			Health:        healthyProto(),
			PendingJobIds: pending,
		}},
	})
	if err != nil {
		t.Fatalf("send Register: %v", err)
	}

	msg, err := stream.Recv()
	if err != nil {
		return stream, nil
	}
	return stream, msg.GetRegisterAck()
}

func TestConnectRegistersAndAcks(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, ack := h.connect(t, ctx, deviceToken)
	if ack == nil {
		t.Fatal("no RegisterAck received")
	}
	if !ack.GetAccepted() {
		t.Fatalf("registration refused: %q", ack.GetReason())
	}
	if ack.GetHeartbeatIntervalSeconds() != 30 {
		t.Errorf("heartbeat interval = %d, want 30", ack.GetHeartbeatIntervalSeconds())
	}

	// The hub must now know about the device.
	if _, err := h.hub.Get(ctx, h.device.ID); err != nil {
		t.Errorf("device not registered in hub: %v", err)
	}
}

func TestConnectRejectsInvalidToken(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, _ := h.connect(t, ctx, "wrong-token")
	_, err := stream.Recv()
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated (err %v)", status.Code(err), err)
	}
}

func TestConnectRejectsMissingToken(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, _ := h.connect(t, ctx, "")
	_, err := stream.Recv()
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated (err %v)", status.Code(err), err)
	}
}

// The first frame must be a Register; anything else is a protocol error.
func TestConnectRequiresRegisterFirst(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	err = stream.Send(&v1.DeviceMessage{
		MessageId:   uuid.NewString(),
		DeviceToken: deviceToken,
		Payload:     &v1.DeviceMessage_Heartbeat{Heartbeat: &v1.Heartbeat{Health: healthyProto()}},
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	_, err = stream.Recv()
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("code = %v, want FailedPrecondition (err %v)", status.Code(err), err)
	}
}

func TestDisabledDeviceIsRefusedWithAReason(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := h.store.Devices().SetEnabled(ctx, h.device.ID, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}

	// The agent gets a refusal it can display, not a bare disconnect.
	_, ack := h.connect(t, ctx, deviceToken)
	if ack == nil {
		t.Fatal("no RegisterAck received; a disabled device must be told why")
	}
	if ack.GetAccepted() {
		t.Error("a disabled device was accepted")
	}
	if ack.GetReason() == "" {
		t.Error("refusal carried no reason")
	}
}

func TestHeartbeatUpdatesHubAndStore(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, ack := h.connect(t, ctx, deviceToken)
	if ack == nil || !ack.GetAccepted() {
		t.Fatal("registration failed")
	}

	hb := healthyProto()
	hb.BatteryLevel = 42
	err := stream.Send(&v1.DeviceMessage{
		MessageId:   uuid.NewString(),
		DeviceToken: deviceToken,
		Payload:     &v1.DeviceMessage_Heartbeat{Heartbeat: &v1.Heartbeat{Health: hb}},
	})
	if err != nil {
		t.Fatalf("send heartbeat: %v", err)
	}

	waitFor(t, func() bool {
		st, err := h.hub.Get(ctx, h.device.ID)
		return err == nil && st.Health != nil && st.Health.BatteryLevel == 42
	}, "hub never saw the heartbeat")

	dev, err := h.store.Devices().Get(ctx, h.device.ID)
	if err != nil {
		t.Fatalf("Get device: %v", err)
	}
	if dev.Health == nil || dev.Health.BatteryLevel != 42 {
		t.Errorf("stored health = %+v, want battery 42", dev.Health)
	}
	if dev.LastSeenAt == nil {
		t.Error("LastSeenAt was not persisted")
	}
}

// A refusal is worth more than a timeout: the job goes back to pending at once.
func TestJobAckRefusalReleasesTheJob(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	job := h.store.SeedJob(&store.Job{
		Recipient:        "+1",
		Body:             "x",
		Mode:             store.ModeQueued,
		Status:           store.JobAssigned,
		AssignedDeviceID: &h.device.ID,
	})

	stream, _ := h.connect(t, ctx, deviceToken, job.ID.String())
	err := stream.Send(&v1.DeviceMessage{
		MessageId:   uuid.NewString(),
		DeviceToken: deviceToken,
		Payload: &v1.DeviceMessage_JobAck{JobAck: &v1.JobAck{
			JobId:    job.ID.String(),
			Accepted: false,
			Reason:   "no SIM",
		}},
	})
	if err != nil {
		t.Fatalf("send ack: %v", err)
	}

	waitFor(t, func() bool {
		return h.store.JobByID(job.ID).Status == store.JobPending
	}, "refused job was not released")
}

func TestJobResultIsRecorded(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	job := h.store.SeedJob(&store.Job{
		Recipient:        "+1",
		Body:             "x",
		Mode:             store.ModeQueued,
		Status:           store.JobAssigned,
		AssignedDeviceID: &h.device.ID,
	})

	stream, _ := h.connect(t, ctx, deviceToken, job.ID.String())
	err := stream.Send(&v1.DeviceMessage{
		MessageId:   uuid.NewString(),
		DeviceToken: deviceToken,
		Payload: &v1.DeviceMessage_JobResult{JobResult: &v1.JobResult{
			JobId:     job.ID.String(),
			Status:    v1.JobStatus_JOB_STATUS_SENT,
			PartsSent: 2,
		}},
	})
	if err != nil {
		t.Fatalf("send result: %v", err)
	}

	waitFor(t, func() bool {
		got := h.store.JobByID(job.ID)
		return got.Status == store.JobSent && got.PartsSent == 2
	}, "job result was not recorded")
}

// Results are at-least-once, so a repeat must not blow up, and a late DELIVERED
// after SENT is a real transition that must be accepted.
func TestDuplicateAndLateResultsAreHandled(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	job := h.store.SeedJob(&store.Job{
		Recipient: "+1", Body: "x", Mode: store.ModeQueued,
		Status: store.JobAssigned, AssignedDeviceID: &h.device.ID,
	})

	stream, _ := h.connect(t, ctx, deviceToken, job.ID.String())
	send := func(s v1.JobStatus) {
		t.Helper()
		err := stream.Send(&v1.DeviceMessage{
			MessageId:   uuid.NewString(),
			DeviceToken: deviceToken,
			Payload: &v1.DeviceMessage_JobResult{JobResult: &v1.JobResult{
				JobId: job.ID.String(), Status: s,
			}},
		})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
	}

	send(v1.JobStatus_JOB_STATUS_SENT)
	waitFor(t, func() bool { return h.store.JobByID(job.ID).Status == store.JobSent }, "first result lost")

	send(v1.JobStatus_JOB_STATUS_SENT) // duplicate
	send(v1.JobStatus_JOB_STATUS_DELIVERED)

	waitFor(t, func() bool {
		return h.store.JobByID(job.ID).Status == store.JobDelivered
	}, "late DELIVERED after SENT was not accepted")

	// The stream must still be alive: neither message is an error.
	if err := stream.Send(&v1.DeviceMessage{
		MessageId: uuid.NewString(), DeviceToken: deviceToken,
		Payload: &v1.DeviceMessage_Heartbeat{Heartbeat: &v1.Heartbeat{Health: healthyProto()}},
	}); err != nil {
		t.Fatalf("stream died after duplicate results: %v", err)
	}
}

// Reconnect reconciliation: the database's view and the agent's must be
// reconciled, not assumed.
func TestReconcileReleasesForgottenJobsAndReportsStale(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// The database thinks the device holds this one; the agent will not report it.
	forgotten := h.store.SeedJob(&store.Job{
		Recipient: "+1", Body: "forgotten", Mode: store.ModeQueued,
		Status: store.JobAssigned, AssignedDeviceID: &h.device.ID,
	})
	// The agent claims to hold this one; the database knows nothing about it.
	ghost := uuid.NewString()

	_, ack := h.connect(t, ctx, deviceToken, ghost)
	if ack == nil || !ack.GetAccepted() {
		t.Fatal("registration failed")
	}

	if len(ack.GetStaleJobIds()) != 1 || ack.GetStaleJobIds()[0] != ghost {
		t.Errorf("stale ids = %v, want [%s]", ack.GetStaleJobIds(), ghost)
	}

	waitFor(t, func() bool {
		return h.store.JobByID(forgotten.ID).Status == store.JobPending
	}, "a job the device no longer holds was not released")
}

// A job pushed through the hub must reach the stream.
func TestServerPushesHubMessagesToTheStream(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, ack := h.connect(t, ctx, deviceToken)
	if ack == nil || !ack.GetAccepted() {
		t.Fatal("registration failed")
	}

	err := h.hub.SendJob(ctx, h.device.ID, &v1.ServerMessage{
		MessageId: "push-1",
		Payload: &v1.ServerMessage_SendSmsJob{SendSmsJob: &v1.SendSmsJob{
			JobId: uuid.NewString(), Recipient: "+33600000000", Body: "pushed",
		}},
	})
	if err != nil {
		t.Fatalf("hub SendJob: %v", err)
	}

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if got := msg.GetSendSmsJob().GetBody(); got != "pushed" {
		t.Errorf("body = %q, want %q", got, "pushed")
	}
}

// A reconnect must supersede the old stream, and the old one must end.
func TestReconnectEndsThePreviousStream(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first, ack := h.connect(t, ctx, deviceToken)
	if ack == nil || !ack.GetAccepted() {
		t.Fatal("first registration failed")
	}

	second, ack2 := h.connect(t, ctx, deviceToken)
	if ack2 == nil || !ack2.GetAccepted() {
		t.Fatal("second registration failed")
	}
	_ = second

	// The first stream must terminate rather than linger.
	done := make(chan error, 1)
	go func() {
		for {
			if _, err := first.Recv(); err != nil {
				done <- err
				return
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the superseded stream never ended")
	}
}

// Every message is authenticated on its own terms, not just the handshake.
func TestMidStreamMessageWithABadTokenIsRejected(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, ack := h.connect(t, ctx, deviceToken)
	if ack == nil || !ack.GetAccepted() {
		t.Fatal("registration failed")
	}

	err := stream.Send(&v1.DeviceMessage{
		MessageId:   uuid.NewString(),
		DeviceToken: "forged",
		Payload:     &v1.DeviceMessage_Heartbeat{Heartbeat: &v1.Heartbeat{Health: healthyProto()}},
	})
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatalf("send: %v", err)
	}

	_, err = stream.Recv()
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("code = %v, want Unauthenticated (err %v)", status.Code(err), err)
	}
}

func TestStreamCloseUnregistersFromHub(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, ack := h.connect(t, ctx, deviceToken)
	if ack == nil || !ack.GetAccepted() {
		t.Fatal("registration failed")
	}
	if err := stream.CloseSend(); err != nil {
		t.Fatalf("CloseSend: %v", err)
	}

	waitFor(t, func() bool {
		_, err := h.hub.Get(context.Background(), h.device.ID)
		return errors.Is(err, hub.ErrNotConnected)
	}, "device was not unregistered when its stream closed")
}

func TestSHA256HasherIsStableAndDistinct(t *testing.T) {
	hasher := SHA256Hasher{}
	first, second := hasher.Hash("a"), hasher.Hash("a")
	if first != second {
		t.Error("hash is not stable")
	}
	if other := hasher.Hash("b"); first == other {
		t.Error("distinct tokens collided")
	}
	if len(first) != 64 {
		t.Errorf("hash length = %d, want 64 hex chars", len(first))
	}
}

// waitFor polls cond until it holds, because the server acts on a message after
// the client's Send has already returned.
func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatal(msg)
		case <-time.After(5 * time.Millisecond):
		}
	}
}
