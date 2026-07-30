package grpcserver

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/KGMA74/relaix-server/devices"
	v1 "github.com/KGMA74/relaix-server/gen/smsgateway/v1"
	"github.com/KGMA74/relaix-server/store"
)

// Enroller is the part of *devices.Service this server uses.
type Enroller interface {
	Enroll(ctx context.Context, enrollmentToken string, info *store.Device) (*devices.EnrolledDevice, error)
}

// Enroll trades a single-use enrollment token, scanned from a QR code, for a
// device id and a long-lived device token.
//
// It is unary rather than the first message of Connect because it authenticates
// differently — a short-lived enrollment token instead of a device token — and
// keeping it separate lets the stream keep one uniform rule: every message on
// Connect carries a device_token, no exceptions.
func (s *Server) Enroll(ctx context.Context, req *v1.EnrollRequest) (*v1.EnrollResponse, error) {
	if s.enroller == nil {
		return nil, status.Error(codes.Unimplemented, "enrollment is not configured")
	}

	info := deviceFromProto(req.GetDeviceInfo())

	enrolled, err := s.enroller.Enroll(ctx, req.GetEnrollmentToken(), info)
	if err != nil {
		return nil, enrollError(err)
	}

	s.opts.Logger.Info("device enrolled over gRPC",
		"device_id", enrolled.Device.ID, "label", enrolled.Device.Label)

	return &v1.EnrollResponse{
		DeviceId:    enrolled.Device.ID.String(),
		DeviceToken: enrolled.DeviceToken,
	}, nil
}

// enrollError maps enrollment failures onto gRPC codes.
//
// All three token failures answer with the same code and a distinguishing
// message. An agent holding a bad QR can do nothing different about any of
// them — it must ask for a new one — while the operator reads the reason in the
// server log, where it is not something an unauthenticated caller can probe for.
func enrollError(err error) error {
	switch {
	case errors.Is(err, devices.ErrInvalidToken):
		return status.Error(codes.PermissionDenied, "invalid enrollment token")
	case errors.Is(err, devices.ErrTokenExpired):
		return status.Error(codes.PermissionDenied, "enrollment token expired")
	case errors.Is(err, devices.ErrTokenUsed):
		return status.Error(codes.PermissionDenied, "enrollment token already used")
	default:
		return status.Errorf(codes.Internal, "enroll: %v", err)
	}
}
