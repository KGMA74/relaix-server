package devices

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/store/storetest"
	"github.com/KGMA74/relaix-server/token"
)

// capGuard allows at most max phones per account, counting the store.
type capGuard struct {
	max     int
	rejoins []bool
}

func (g *capGuard) AllowMint(ctx context.Context, account uuid.UUID) error {
	return nil
}

func (g *capGuard) AllowEnroll(ctx context.Context, tx store.Store, account uuid.UUID, rejoining bool) error {
	g.rejoins = append(g.rejoins, rejoining)
	if rejoining {
		return nil
	}
	devs, err := tx.Devices().List(ctx, store.OnlyAccount(account))
	if err != nil {
		return err
	}
	if len(devs) >= g.max {
		return &RefusedError{Reason: "device limit reached"}
	}
	return nil
}

func guarded(st store.Store, g Guard) *Service {
	return New(st, token.SHA256{}, Options{
		Guard:  g,
		Now:    func() time.Time { return testNow },
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func TestGuardCapsDevicesButLetsAPhoneRejoin(t *testing.T) {
	st := storetest.New()
	g := &capGuard{max: 1}
	s := guarded(st, g)
	ctx := context.Background()

	first := mustEnroll(t, s, &store.Device{Label: "a"}, "hw-a")

	// A second phone is refused, and its token is left unused so it can be
	// scanned again once the account may grow.
	minted, err := s.MintEnrollmentToken(ctx, uuid.Nil)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	_, err = s.Enroll(ctx, minted.Plaintext, &store.Device{Label: "b"}, "hw-b")
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Reason != "device limit reached" {
		t.Fatalf("second phone: err = %v, want the guard's refusal", err)
	}
	if tok, _ := st.Enrollments().Lookup(ctx, token.SHA256{}.Hash(minted.Plaintext)); tok.ConsumedAt != nil {
		t.Error("refused enrollment consumed the token")
	}
	if devs, _ := st.Devices().List(ctx, store.AllAccounts()); len(devs) != 1 {
		t.Errorf("%d devices, want 1", len(devs))
	}

	// The first phone enrolling again is not a new device.
	again := mustEnroll(t, s, &store.Device{Label: "a"}, "hw-a")
	if again.Device.ID != first.Device.ID || !again.Reenrolled {
		t.Errorf("rejoin made a new device")
	}
	if want := []bool{false, false, true}; !slices.Equal(g.rejoins, want) {
		t.Errorf("rejoining flags = %v, want %v", g.rejoins, want)
	}
}

type refuseMint struct{ capGuard }

func (*refuseMint) AllowMint(context.Context, uuid.UUID) error {
	return &RefusedError{Reason: "account suspended"}
}

func TestGuardCanRefuseMinting(t *testing.T) {
	st := storetest.New()
	s := guarded(st, &refuseMint{})
	_, err := s.MintEnrollmentToken(context.Background(), uuid.Nil)
	var refused *RefusedError
	if !errors.As(err, &refused) || refused.Reason != "account suspended" {
		t.Fatalf("err = %v, want the guard's refusal", err)
	}
}
