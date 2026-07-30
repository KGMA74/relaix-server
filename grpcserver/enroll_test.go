package grpcserver

import (
	"context"
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

	"github.com/KGMA74/relaix-server/devices"
	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/hub"
	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/store/storetest"
	"github.com/KGMA74/relaix-server/token"
)

// enrollHarness wires the real enrollment service behind a real gRPC server.
type enrollHarness struct {
	store   *storetest.Store
	service *devices.Service
	client  v1.DeviceGatewayClient
}

func newEnrollHarness(t *testing.T, withEnroller bool) *enrollHarness {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := storetest.New()
	hasher := token.SHA256{}
	now := func() time.Time { return testNow }

	svc := devices.New(st, hasher, devices.Options{
		TokenTTL: 15 * time.Minute,
		Now:      now,
		Logger:   logger,
	})

	h := hub.New(hub.Options{Now: now, Logger: logger})
	hubCtx, stopHub := context.WithCancel(context.Background())
	hubDone := make(chan struct{})
	go func() { defer close(hubDone); _ = h.Run(hubCtx) }()

	srv := New(st, h, hasher, Options{Now: now, Logger: logger})
	if withEnroller {
		srv = srv.WithEnroller(svc)
	}

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

	return &enrollHarness{store: st, service: svc, client: v1.NewDeviceGatewayClient(conn)}
}

func TestEnrollReturnsIdentityAndToken(t *testing.T) {
	h := newEnrollHarness(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	minted, err := h.service.MintEnrollmentToken(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}

	resp, err := h.client.Enroll(ctx, &v1.EnrollRequest{
		EnrollmentToken: minted.Plaintext,
		DeviceInfo: &v1.DeviceInfo{
			Label:       "desk phone",
			PhoneNumber: "+33600000000",
			Model:       "SM-A536B",
			Carrier:     "Orange",
		},
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	id, err := uuid.Parse(resp.GetDeviceId())
	if err != nil {
		t.Fatalf("device id is not a uuid: %v", err)
	}
	if resp.GetDeviceToken() == "" {
		t.Fatal("no device token returned")
	}

	dev, err := h.store.Devices().Get(ctx, id)
	if err != nil {
		t.Fatalf("device was not persisted: %v", err)
	}
	if dev.Label != "desk phone" || dev.Carrier != "Orange" {
		t.Errorf("device info not stored: %+v", dev)
	}
}

// The token returned by Enroll must actually open a Connect stream — the two
// RPCs are only useful together.
func TestTokenFromEnrollOpensAConnectStream(t *testing.T) {
	h := newEnrollHarness(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	minted, err := h.service.MintEnrollmentToken(ctx)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	resp, err := h.client.Enroll(ctx, &v1.EnrollRequest{
		EnrollmentToken: minted.Plaintext,
		DeviceInfo:      &v1.DeviceInfo{Label: "fresh"},
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	stream, err := h.client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	err = stream.Send(&v1.DeviceMessage{
		MessageId:   uuid.NewString(),
		DeviceToken: resp.GetDeviceToken(),
		Payload: &v1.DeviceMessage_Register{Register: &v1.Register{
			DeviceInfo: &v1.DeviceInfo{Label: "fresh"},
			Health:     healthyProto(),
		}},
	})
	if err != nil {
		t.Fatalf("send Register: %v", err)
	}

	msg, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv: %v", err)
	}
	if !msg.GetRegisterAck().GetAccepted() {
		t.Fatalf("the freshly enrolled device was refused: %q", msg.GetRegisterAck().GetReason())
	}
}

func TestEnrollRejectsBadTokens(t *testing.T) {
	tests := []struct {
		name  string
		token func(t *testing.T, h *enrollHarness, ctx context.Context) string
	}{
		{
			name:  "unknown",
			token: func(*testing.T, *enrollHarness, context.Context) string { return "never-minted" },
		},
		{
			name:  "empty",
			token: func(*testing.T, *enrollHarness, context.Context) string { return "" },
		},
		{
			name: "already used",
			token: func(t *testing.T, h *enrollHarness, ctx context.Context) string {
				minted, err := h.service.MintEnrollmentToken(ctx)
				if err != nil {
					t.Fatalf("mint: %v", err)
				}
				if _, err := h.client.Enroll(ctx, &v1.EnrollRequest{
					EnrollmentToken: minted.Plaintext,
					DeviceInfo:      &v1.DeviceInfo{Label: "first"},
				}); err != nil {
					t.Fatalf("first Enroll: %v", err)
				}
				return minted.Plaintext
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newEnrollHarness(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			tok := tc.token(t, h, ctx)
			_, err := h.client.Enroll(ctx, &v1.EnrollRequest{
				EnrollmentToken: tok,
				DeviceInfo:      &v1.DeviceInfo{Label: "attempt"},
			})
			if status.Code(err) != codes.PermissionDenied {
				t.Fatalf("code = %v, want PermissionDenied (err %v)", status.Code(err), err)
			}
		})
	}
}

func TestEnrollRejectsExpiredToken(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := storetest.New()
	hasher := token.SHA256{}

	now := testNow
	svc := devices.New(st, hasher, devices.Options{
		TokenTTL: time.Minute,
		Now:      func() time.Time { return now },
		Logger:   logger,
	})

	minted, err := svc.MintEnrollmentToken(context.Background())
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	now = testNow.Add(time.Hour)

	_, err = svc.Enroll(context.Background(), minted.Plaintext, &store.Device{Label: "late"})
	if got := enrollError(err); status.Code(got) != codes.PermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", status.Code(got))
	}
}

// Without an enroller configured the RPC must say so rather than pretend.
func TestEnrollWithoutEnrollerIsUnimplemented(t *testing.T) {
	h := newEnrollHarness(t, false)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := h.client.Enroll(ctx, &v1.EnrollRequest{EnrollmentToken: "anything"})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("code = %v, want Unimplemented (err %v)", status.Code(err), err)
	}
}

// Compile-time check that the real service satisfies the narrow interface.
var _ Enroller = (*devices.Service)(nil)
