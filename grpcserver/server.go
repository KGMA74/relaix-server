// Package grpcserver implements the DeviceGateway service: the endpoint every
// Android agent dials out to and holds open.
//
// The direction is inverted from a normal server because phones sit behind
// carrier NAT and can never be reached inbound. The device opens the stream,
// authenticates every message it sends, and the control plane pushes work down
// the connection the device itself established. See docs/architecture.md §2 in
// the monorepo.
package grpcserver

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/hub"
	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/token"
)

// Hub is the part of *hub.Hub this server uses, narrowed so the handlers can be
// tested without running a hub goroutine.
type Hub interface {
	Register(ctx context.Context, deviceID uuid.UUID, health *store.DeviceHealth) (*hub.Registration, error)
	Unregister(ctx context.Context, deviceID, regID uuid.UUID, reason string) error
	Heartbeat(ctx context.Context, deviceID, regID uuid.UUID, health *store.DeviceHealth) error
}

// Options configures the server.
type Options struct {
	// HeartbeatInterval is handed to agents in RegisterAck. Server-controlled
	// so the cadence can be tuned fleet-wide without shipping a new app: the
	// trade-off between detection latency and battery is an operator's call.
	// Default 30s.
	HeartbeatInterval time.Duration

	Now    func() time.Time
	Logger *slog.Logger
}

func (o *Options) withDefaults() {
	if o.HeartbeatInterval <= 0 {
		o.HeartbeatInterval = 30 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// Server implements v1.DeviceGatewayServer.
type Server struct {
	v1.UnimplementedDeviceGatewayServer

	store  store.Store
	hub    Hub
	hasher token.Hasher
	opts   Options
}

// New creates a Server. Enroll is added in a later change; until then the
// embedded UnimplementedDeviceGatewayServer answers it with Unimplemented,
// which is the honest response.
func New(s store.Store, h Hub, hasher token.Hasher, opts Options) *Server {
	opts.withDefaults()
	return &Server{store: s, hub: h, hasher: hasher, opts: opts}
}

// Connect is the bidirectional stream an agent holds open for its whole uptime.
//
// The first message must be a Register carrying a valid device token. After
// that the handler is a loop: read a message, authenticate it, act on it. A
// second goroutine drains the hub's outbound channel and writes to the stream —
// gRPC forbids concurrent Send from multiple goroutines, so exactly one
// goroutine writes and it is that one.
func (s *Server) Connect(stream v1.DeviceGateway_ConnectServer) error {
	ctx := stream.Context()

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	reg := first.GetRegister()
	if reg == nil {
		return status.Error(codes.FailedPrecondition, "first message must be a Register")
	}

	device, err := s.authenticate(ctx, first.GetDeviceToken())
	if err != nil {
		return err
	}
	if !device.Enabled {
		// Refused politely rather than dropped, so the agent can show why
		// instead of reconnecting forever against a closed door.
		_ = stream.Send(&v1.ServerMessage{
			MessageId: uuid.NewString(),
			SentAt:    timestamppb.New(s.opts.Now()),
			Payload: &v1.ServerMessage_RegisterAck{
				RegisterAck: &v1.RegisterAck{
					Accepted: false,
					Reason:   "device is disabled",
				},
			},
		})
		return status.Error(codes.PermissionDenied, "device is disabled")
	}

	health := healthFromProto(reg.GetHealth(), s.opts.Now())
	registration, err := s.hub.Register(ctx, device.ID, health)
	if err != nil {
		return status.Errorf(codes.Unavailable, "register: %v", err)
	}

	log := s.opts.Logger.With("device_id", device.ID, "registration", registration.ID)
	log.Info("device connected", "label", device.Label)

	defer func() {
		if err := s.hub.Unregister(context.WithoutCancel(ctx), device.ID, registration.ID, "stream closed"); err != nil {
			log.Warn("unregister failed", "err", err)
		}
		log.Info("device disconnected")
	}()

	if err := s.persistRegister(ctx, device, reg, health); err != nil {
		log.Warn("could not persist registration", "err", err)
	}

	stale, err := s.reconcile(ctx, device.ID, reg.GetPendingJobIds())
	if err != nil {
		log.Warn("could not reconcile pending jobs", "err", err)
	}

	// One writer goroutine, because gRPC does not allow concurrent Send.
	writeErr := make(chan error, 1)
	go func() { writeErr <- s.writeLoop(stream, registration) }()

	ack := &v1.ServerMessage{
		MessageId: uuid.NewString(),
		SentAt:    timestamppb.New(s.opts.Now()),
		Payload: &v1.ServerMessage_RegisterAck{
			RegisterAck: &v1.RegisterAck{
				Accepted:                 true,
				HeartbeatIntervalSeconds: int32(s.opts.HeartbeatInterval.Seconds()),
				StaleJobIds:              stale,
			},
		},
	}
	if err := stream.Send(ack); err != nil {
		return err
	}

	// Reading also runs in its own goroutine, so that the handler can return
	// the moment the writer stops. That matters for eviction: when a newer
	// connection supersedes this one the hub closes the outbound channel, and
	// without this the handler would sit in Recv until the abandoned agent
	// happened to send something — leaving a superseded stream open for
	// minutes. Returning from the handler tears the stream down, which is what
	// unblocks the pending Recv.
	readErr := make(chan error, 1)
	go func() { readErr <- s.readLoop(ctx, stream, device, registration, log) }()

	select {
	case err := <-writeErr:
		return err
	case err := <-readErr:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// readLoop consumes messages until the stream ends.
func (s *Server) readLoop(
	ctx context.Context,
	stream v1.DeviceGateway_ConnectServer,
	device *store.Device,
	registration *hub.Registration,
	log *slog.Logger,
) error {
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}

		// Every message is authenticated on its own terms — see the
		// per-message token decision in docs/protocol.md. A frame injected into
		// an established stream is rejected on its own merits rather than
		// inheriting trust from the handshake.
		if !s.tokenMatches(ctx, msg.GetDeviceToken(), device.ID) {
			return status.Error(codes.Unauthenticated, "invalid device token")
		}

		switch payload := msg.GetPayload().(type) {
		case *v1.DeviceMessage_Heartbeat:
			s.onHeartbeat(ctx, device, registration, payload.Heartbeat, log)
		case *v1.DeviceMessage_JobAck:
			s.onJobAck(ctx, device, payload.JobAck, log)
		case *v1.DeviceMessage_JobResult:
			s.onJobResult(ctx, device, payload.JobResult, log)
		case *v1.DeviceMessage_Register:
			// A second Register on an open stream is a protocol error: the
			// agent should have opened a new one.
			return status.Error(codes.FailedPrecondition, "duplicate Register on an open stream")
		default:
			log.Warn("ignoring unknown payload", "message_id", msg.GetMessageId())
		}
	}
}

// writeLoop drains the hub's outbound channel. It returns when the hub closes
// the channel — on eviction by a newer connection, on unregister, or on hub
// shutdown — which is how a superseded stream learns to end.
func (s *Server) writeLoop(stream v1.DeviceGateway_ConnectServer, reg *hub.Registration) error {
	for msg := range reg.Out {
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
	return nil
}

// authenticate resolves a device token to a device.
func (s *Server) authenticate(ctx context.Context, token string) (*store.Device, error) {
	if token == "" {
		return nil, status.Error(codes.Unauthenticated, "missing device token")
	}
	device, err := s.store.Devices().GetByTokenHash(ctx, s.hasher.Hash(token))
	if errors.Is(err, store.ErrNotFound) {
		return nil, status.Error(codes.Unauthenticated, "invalid device token")
	}
	if err != nil {
		return nil, status.Errorf(codes.Internal, "lookup device: %v", err)
	}
	return device, nil
}

// tokenMatches checks a per-message token against the device that owns the
// stream. Cheap by design: this runs on every message of every stream.
func (s *Server) tokenMatches(ctx context.Context, token string, deviceID uuid.UUID) bool {
	if token == "" {
		return false
	}
	device, err := s.store.Devices().GetByTokenHash(ctx, s.hasher.Hash(token))
	return err == nil && device.ID == deviceID && device.Enabled
}

// persistRegister refreshes the stored device info and liveness from a
// Register, so the server's view survives OS updates, app updates and SIM swaps.
func (s *Server) persistRegister(ctx context.Context, device *store.Device, reg *v1.Register, health *store.DeviceHealth) error {
	if info := reg.GetDeviceInfo(); info != nil {
		updated := deviceFromProto(info)
		if err := s.store.Devices().UpdateInfo(ctx, device.ID, updated); err != nil {
			return err
		}
	}
	return s.store.Devices().Touch(ctx, device.ID, health, s.opts.Now())
}

// reconcile compares what the agent says it still holds against what the
// database believes, and returns the ids the agent should drop.
//
// Jobs the database has assigned to this device but the agent does not report
// are released, so they can be placed elsewhere instead of waiting on a phone
// that has forgotten them. Jobs the agent reports that the database no longer
// considers assigned are returned as stale.
func (s *Server) reconcile(ctx context.Context, deviceID uuid.UUID, reported []string) ([]string, error) {
	assigned, err := s.store.Jobs().ListAssignedTo(ctx, deviceID)
	if err != nil {
		return nil, err
	}

	held := make(map[string]bool, len(reported))
	for _, id := range reported {
		held[id] = true
	}

	known := make(map[string]bool, len(assigned))
	for _, job := range assigned {
		id := job.ID.String()
		known[id] = true
		if !held[id] {
			if err := s.store.Jobs().Release(ctx, job.ID, "device did not report the job on reconnect"); err != nil {
				s.opts.Logger.Warn("could not release forgotten job", "job_id", job.ID, "err", err)
			}
		}
	}

	var stale []string
	for _, id := range reported {
		if !known[id] {
			stale = append(stale, id)
		}
	}
	return stale, nil
}

func (s *Server) onHeartbeat(ctx context.Context, device *store.Device, reg *hub.Registration, hb *v1.Heartbeat, log *slog.Logger) {
	now := s.opts.Now()
	health := healthFromProto(hb.GetHealth(), now)

	if err := s.hub.Heartbeat(ctx, device.ID, reg.ID, health); err != nil {
		log.Warn("hub heartbeat failed", "err", err)
	}
	if err := s.store.Devices().Touch(ctx, device.ID, health, now); err != nil {
		log.Warn("could not persist heartbeat", "err", err)
	}
}

func (s *Server) onJobAck(ctx context.Context, device *store.Device, ack *v1.JobAck, log *slog.Logger) {
	jobID, err := uuid.Parse(ack.GetJobId())
	if err != nil {
		log.Warn("job ack with unparseable id", "job_id", ack.GetJobId())
		return
	}

	if ack.GetAccepted() {
		s.event(ctx, jobID, store.JobAssigned, &device.ID, "accepted by device")
		return
	}

	// An explicit refusal is worth more than a timeout: release the job now so
	// the next tick can place it on another phone.
	reason := "device refused: " + ack.GetReason()
	if err := s.store.Jobs().Release(ctx, jobID, reason); err != nil {
		log.Warn("could not release refused job", "job_id", jobID, "err", err)
		return
	}
	s.event(ctx, jobID, store.JobPending, &device.ID, reason)
}

func (s *Server) onJobResult(ctx context.Context, device *store.Device, res *v1.JobResult, log *slog.Logger) {
	jobID, err := uuid.Parse(res.GetJobId())
	if err != nil {
		log.Warn("job result with unparseable id", "job_id", res.GetJobId())
		return
	}

	completed := s.opts.Now()
	if ts := res.GetCompletedAt(); ts != nil {
		completed = ts.AsTime()
	}

	result := store.JobResult{
		Status:       statusFromProto(res.GetStatus()),
		ErrorCode:    res.GetErrorCode(),
		ErrorMessage: res.GetErrorMessage(),
		PartsSent:    int(res.GetPartsSent()),
		CompletedAt:  completed,
	}

	// Results are at-least-once, so a repeat of a terminal state is expected
	// and the store treats it as a no-op rather than an error.
	if err := s.store.Jobs().Complete(ctx, jobID, result); err != nil {
		if errors.Is(err, store.ErrConflict) {
			log.Debug("ignoring result for an already-resolved job", "job_id", jobID)
			return
		}
		log.Warn("could not record job result", "job_id", jobID, "err", err)
		return
	}
	s.event(ctx, jobID, result.Status, &device.ID, res.GetErrorMessage())
}

func (s *Server) event(ctx context.Context, jobID uuid.UUID, status store.JobStatus, deviceID *uuid.UUID, reason string) {
	e := &store.JobEvent{
		JobID:     jobID,
		Status:    status,
		DeviceID:  deviceID,
		Reason:    reason,
		CreatedAt: s.opts.Now(),
	}
	if err := s.store.Events().Append(ctx, e); err != nil {
		s.opts.Logger.Warn("could not record job event", "job_id", jobID, "err", err)
	}
}

// ---------------------------------------------------------------------------
// conversions
// ---------------------------------------------------------------------------

func healthFromProto(h *v1.DeviceHealth, now time.Time) *store.DeviceHealth {
	if h == nil {
		return nil
	}
	return &store.DeviceHealth{
		BatteryLevel:   int(h.GetBatteryLevel()),
		IsCharging:     h.GetIsCharging(),
		SignalStrength: int(h.GetSignalStrength()),
		NetworkType:    h.GetNetworkType(),
		SimReady:       h.GetSimReady(),
		SentLastHour:   int(h.GetSentLastHour()),
		PermissionsOK:  h.GetPermissionsOk(),
		ReportedAt:     now,
	}
}

func deviceFromProto(i *v1.DeviceInfo) *store.Device {
	return &store.Device{
		Label:        i.GetLabel(),
		PhoneNumber:  i.GetPhoneNumber(),
		Manufacturer: i.GetManufacturer(),
		Model:        i.GetModel(),
		OSVersion:    i.GetOsVersion(),
		AgentVersion: i.GetAgentVersion(),
		Carrier:      i.GetCarrier(),
	}
}

func statusFromProto(s v1.JobStatus) store.JobStatus {
	switch s {
	case v1.JobStatus_JOB_STATUS_SENT:
		return store.JobSent
	case v1.JobStatus_JOB_STATUS_DELIVERED:
		return store.JobDelivered
	case v1.JobStatus_JOB_STATUS_CANCELLED:
		return store.JobCancelled
	default:
		// UNSPECIFIED included: an agent that cannot say what happened has not
		// told us it succeeded, and treating silence as success would lose
		// messages quietly.
		return store.JobFailed
	}
}
