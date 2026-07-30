// Package api is the REST interface callers and operators use.
//
// It is the only public surface of the control plane: devices talk gRPC, and
// everything else — submitting a message, asking what became of it, listing the
// fleet, minting an enrollment token — happens here.
package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/devices"
	"github.com/KGMA74/relaix-server/hub"
	"github.com/KGMA74/relaix-server/store"
)

// Hub is the part of *hub.Hub the API uses.
type Hub interface {
	ListConnected(ctx context.Context) ([]hub.DeviceState, error)
	ListReady(ctx context.Context) ([]hub.DeviceState, error)
}

// Enroller mints enrollment tokens.
type Enroller interface {
	MintEnrollmentToken(ctx context.Context) (*devices.MintedToken, error)
}

// Options configures the API.
type Options struct {
	// APIKey guards every route. Empty disables authentication, which is only
	// reasonable behind a trusted proxy — and is logged loudly at startup,
	// because an open /admin endpoint mints credentials for joining the fleet.
	APIKey string

	// PublicURL is the address agents should dial, encoded into enrollment QR
	// codes alongside the token so a phone needs no manual configuration.
	PublicURL string

	// MaxBodyBytes bounds a request body. Default 64 KiB: an SMS body is at
	// most a few hundred bytes, so anything larger is a mistake or an attack.
	MaxBodyBytes int64

	// Pinger backs the readiness probe. Nil makes /readyz answer purely on the
	// process being up.
	Pinger Pinger

	Now    func() time.Time
	Logger *slog.Logger
}

func (o *Options) withDefaults() {
	if o.MaxBodyBytes <= 0 {
		o.MaxBodyBytes = 64 << 10
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// Server serves the REST API.
type Server struct {
	store    store.Store
	hub      Hub
	enroller Enroller
	opts     Options
}

// New creates a Server.
func New(s store.Store, h Hub, e Enroller, opts Options) *Server {
	opts.withDefaults()
	if opts.APIKey == "" {
		opts.Logger.Warn("API authentication is disabled; /admin can mint enrollment tokens")
	}
	return &Server{store: s, hub: h, enroller: e, opts: opts}
}

// Handler returns the routed, wrapped handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Probes are outside authentication: an orchestrator's health check has no
	// credentials to offer, and requiring them would mean the gateway is
	// restarted for being unauthenticated rather than for being unhealthy.
	mux.HandleFunc("GET /healthz", s.handleLive)
	mux.HandleFunc("GET /readyz", s.handleReady)

	mux.HandleFunc("POST /send", s.handleSend)
	mux.HandleFunc("GET /jobs/{id}", s.handleGetJob)
	mux.HandleFunc("DELETE /jobs/{id}", s.handleCancelJob)
	mux.HandleFunc("GET /devices", s.handleListDevices)
	mux.HandleFunc("POST /admin/devices/enroll-token", s.handleEnrollToken)

	// Ordered outermost first: recovery has to wrap logging so a panic is still
	// logged with its request id, and authentication runs inside both so
	// rejected requests are recorded too.
	return s.recover(s.logRequests(s.authenticate(mux)))
}

// publicPaths bypass authentication. Only the probes: an orchestrator's health
// check has no credentials to offer, and requiring them would get the gateway
// restarted for being unauthenticated rather than for being unhealthy.
var publicPaths = map[string]bool{
	"/healthz": true,
	"/readyz":  true,
}

// authenticate checks the bearer token.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.opts.APIKey == "" || publicPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		// Constant time, so the comparison does not leak the key one byte at a
		// time to anyone willing to measure.
		if subtle.ConstantTimeCompare([]byte(header[len(prefix):]), []byte(s.opts.APIKey)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// logRequests records one line per request, with a generated id echoed back so
// a caller reporting a problem can point at the exact request.
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := s.opts.Now()
		id := uuid.NewString()
		w.Header().Set("X-Request-Id", id)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		s.opts.Logger.Info("http request",
			"request_id", id,
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", s.opts.Now().Sub(start),
		)
	})
}

// recover turns a panic into a 500 instead of a dropped connection, and keeps
// the process alive: one bad request must not take the gateway down.
func (s *Server) recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.opts.Logger.Error("panic serving request",
					"path", r.URL.Path, "panic", v)
				writeError(w, http.StatusInternalServerError, "internal", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status code for the access log.
type statusRecorder struct {
	http.ResponseWriter
	status  int
	written bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if r.written {
		return
	}
	r.written = true
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	r.written = true
	return r.ResponseWriter.Write(b)
}

// ---------------------------------------------------------------------------
// responses
// ---------------------------------------------------------------------------

// errorBody is the shape of every failure. A stable machine-readable code sits
// beside the human message, so callers can branch on the code without parsing
// prose that may be reworded.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorBody{Error: errorDetail{Code: code, Message: message}})
}

// decodeJSON reads a bounded, strict JSON body. Unknown fields are rejected: a
// caller who misspells "scheduledAt" should be told, not silently ignored and
// left wondering why their message went out immediately.
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}
