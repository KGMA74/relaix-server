// Package devices owns how a phone joins the fleet.
//
// An operator mints a single-use token, which is rendered as a QR code; the
// agent scans it and trades it for a long-lived device token. The QR exists to
// avoid typing a long random string on a phone keyboard and to carry the server
// endpoint alongside the token, so there is no separate manual configuration
// step. See docs/architecture.md §7 in the monorepo.
package devices

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/token"
)

// Errors returned by the enrollment service. They are distinct because the
// operator's next move differs: an expired token means mint another, a
// consumed one means find out which phone claimed it.
var (
	// ErrInvalidToken means no such enrollment token exists.
	ErrInvalidToken = errors.New("devices: invalid enrollment token")

	// ErrTokenExpired means the token ran out of time before being used.
	ErrTokenExpired = errors.New("devices: enrollment token expired")

	// ErrTokenUsed means the token was already redeemed by another device.
	ErrTokenUsed = errors.New("devices: enrollment token already used")
)

// Options configures the service.
type Options struct {
	// TokenTTL is how long a minted enrollment token stays usable. Default
	// 15 minutes: long enough to walk a phone over and scan, short enough that
	// a QR left on a screen or in a chat log stops being a way in.
	TokenTTL time.Duration

	Now    func() time.Time
	Logger *slog.Logger
}

func (o *Options) withDefaults() {
	if o.TokenTTL <= 0 {
		o.TokenTTL = 15 * time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// Service mints and redeems enrollment tokens.
type Service struct {
	store  store.Store
	hasher token.Hasher
	opts   Options
}

// New creates a Service.
func New(s store.Store, hasher token.Hasher, opts Options) *Service {
	opts.withDefaults()
	return &Service{store: s, hasher: hasher, opts: opts}
}

// MintedToken is a freshly created enrollment token.
type MintedToken struct {
	// Plaintext is the value to encode in the QR code. It is returned exactly
	// once and never stored: the database holds only its hash, so a dump does
	// not hand over a way to enroll.
	Plaintext string

	Record *store.EnrollmentToken
}

// MintEnrollmentToken creates a single-use token.
func (s *Service) MintEnrollmentToken(ctx context.Context) (*MintedToken, error) {
	plaintext, err := token.Generate()
	if err != nil {
		return nil, err
	}

	expires := s.opts.Now().Add(s.opts.TokenTTL)
	rec, err := s.store.Enrollments().Create(ctx, s.hasher.Hash(plaintext), expires)
	if err != nil {
		return nil, fmt.Errorf("devices: mint enrollment token: %w", err)
	}

	s.opts.Logger.Info("enrollment token minted", "token_id", rec.ID, "expires_at", expires)
	return &MintedToken{Plaintext: plaintext, Record: rec}, nil
}

// EnrolledDevice is the result of a successful enrollment.
type EnrolledDevice struct {
	Device *store.Device

	// DeviceToken is the long-lived credential the agent puts on every message.
	// Returned exactly once; the database holds only its hash.
	DeviceToken string
}

// Enroll trades an enrollment token for a device and its long-lived token.
//
// The token consumption and the device creation happen in one transaction. They
// have to: consuming without creating burns a token and leaves the operator
// wondering where the phone went, and creating without consuming leaves a
// single-use token usable twice — which is the whole property the QR flow
// depends on.
//
// The device is created first so that consumption can record which device
// claimed the token, giving an audit trail from "who minted it" to "which phone
// joined". If consumption then fails, the transaction takes the device with it.
func (s *Service) Enroll(ctx context.Context, enrollmentToken string, info *store.Device) (*EnrolledDevice, error) {
	if enrollmentToken == "" {
		return nil, ErrInvalidToken
	}

	deviceToken, err := token.Generate()
	if err != nil {
		return nil, err
	}

	now := s.opts.Now()
	tokenHash := s.hasher.Hash(enrollmentToken)

	var device *store.Device
	err = s.store.WithTx(ctx, func(tx store.Store) error {
		candidate := *info
		candidate.Enabled = true
		candidate.CreatedAt = now

		created, err := tx.Devices().Create(ctx, &candidate, s.hasher.Hash(deviceToken))
		if err != nil {
			return fmt.Errorf("create device: %w", err)
		}

		if _, err := tx.Enrollments().Consume(ctx, tokenHash, created.ID, now); err != nil {
			return err
		}

		device = created
		return nil
	})
	if err != nil {
		return nil, translate(err)
	}

	s.opts.Logger.Info("device enrolled", "device_id", device.ID, "label", device.Label)
	return &EnrolledDevice{Device: device, DeviceToken: deviceToken}, nil
}

// translate turns store errors into this package's vocabulary, so callers do
// not have to know what a conflict meant at the persistence layer.
func translate(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return ErrInvalidToken
	case errors.Is(err, store.ErrTokenExpired):
		return ErrTokenExpired
	case errors.Is(err, store.ErrConflict):
		return ErrTokenUsed
	default:
		return err
	}
}
