package api

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/store"
)

// Principal is who a request acts for: the account whose devices and messages
// it sees, and whether it may see past that account.
type Principal struct {
	// AccountID owns what the request creates: messages it sends, enrollment
	// tokens it mints.
	AccountID uuid.UUID

	// AllAccounts lets the principal read and act on every account. The
	// operator of a self-hosted gateway holds it — there is only one account
	// to see — and so does whoever administers a gateway shared by several.
	AllAccounts bool
}

// Scope is what the principal may see.
func (p Principal) Scope() store.Scope {
	if p.AllAccounts {
		return store.AllAccounts()
	}
	return store.OnlyAccount(p.AccountID)
}

// Owns reports whether something belonging to accountID is visible to p.
func (p Principal) Owns(accountID uuid.UUID) bool {
	return p.Scope().Includes(accountID)
}

// Authenticator turns a request's credentials into a principal.
//
// An error rejects the request with 401, and its text is the message the
// caller gets back: say what was wrong with the credentials, never why a
// guess came close.
type Authenticator interface {
	Authenticate(r *http.Request) (Principal, error)
}

// AuthenticatorFunc adapts a function to Authenticator.
type AuthenticatorFunc func(r *http.Request) (Principal, error)

// Authenticate implements Authenticator.
func (f AuthenticatorFunc) Authenticate(r *http.Request) (Principal, error) { return f(r) }

// operator is the principal of a single-tenant gateway: everything belongs to
// the default account, and it sees all of it.
var operator = Principal{AccountID: store.DefaultAccountID, AllAccounts: true}

// SharedKey authenticates with one bearer key, the gateway's own: whoever
// holds it is the operator. An empty key lets every request through as the
// operator, which is only reasonable behind a proxy that authenticates.
func SharedKey(key string) Authenticator {
	return AuthenticatorFunc(func(r *http.Request) (Principal, error) {
		if key == "" {
			return operator, nil
		}
		token, err := BearerToken(r)
		if err != nil {
			return Principal{}, err
		}
		// Constant time, so the comparison does not leak the key one byte at a
		// time to anyone willing to measure.
		if subtle.ConstantTimeCompare([]byte(token), []byte(key)) != 1 {
			return Principal{}, errors.New("invalid api key")
		}
		return operator, nil
	})
}

// BearerToken extracts the token from an Authorization: Bearer header.
func BearerToken(r *http.Request) (string, error) {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return "", errors.New("missing bearer token")
	}
	return header[len(prefix):], nil
}

type principalKey struct{}

// WithPrincipal returns a context carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}

// PrincipalFrom returns the principal authenticate attached to the request.
// Handlers mounted by an embedding program read it the same way.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// principal is the request's principal. Every route behind authenticate has
// one; a route that reaches here without one is a wiring mistake, and it gets
// an empty scope rather than everything.
func principal(r *http.Request) Principal {
	p, _ := PrincipalFrom(r.Context())
	return p
}

// authenticate attaches the request's principal, or answers 401.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isPublic(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		p, err := s.opts.Authenticator.Authenticate(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", err.Error())
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// ownedJob loads a job the principal may see, answering 404 otherwise: a job
// of another account is reported exactly like one that does not exist, so ids
// cannot be probed across accounts.
func (s *Server) ownedJob(w http.ResponseWriter, r *http.Request, id uuid.UUID) (*store.Job, bool) {
	job, err := s.store.Jobs().Get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !principal(r).Owns(job.AccountID)) {
		writeError(w, http.StatusNotFound, "not_found", "no such job")
		return nil, false
	}
	if err != nil {
		s.opts.Logger.Error("could not read job", "job_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not read job")
		return nil, false
	}
	return job, true
}

// ownedDevice is ownedJob for devices.
func (s *Server) ownedDevice(w http.ResponseWriter, r *http.Request, id uuid.UUID) (*store.Device, bool) {
	dev, err := s.store.Devices().Get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !principal(r).Owns(dev.AccountID)) {
		writeError(w, http.StatusNotFound, "not_found", "no such device")
		return nil, false
	}
	if err != nil {
		s.opts.Logger.Error("could not read device", "device_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not read device")
		return nil, false
	}
	return dev, true
}
