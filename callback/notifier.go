// Package callback delivers job outcomes back to the caller.
//
// Callers do not poll for the fate of every message: when a job reaches a
// terminal state the control plane POSTs the outcome to the URL they gave. The
// receiver will be down sometimes, so delivery is not fire-and-forget — see
// watcher.go — and every request is signed so the receiver can tell a real
// callback from anyone who guessed the endpoint.
//
// See docs/architecture.md §6 in the monorepo.
package callback

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/KGMA74/relaix-server/store"
)

// Header names carried on every callback.
const (
	// HeaderSignature is "sha256=" followed by the hex MAC.
	HeaderSignature = "X-Relaix-Signature"
	// HeaderTimestamp is the unix second the request was signed. It is part of
	// the signed material, so it cannot be edited to extend a replay window.
	HeaderTimestamp = "X-Relaix-Timestamp"
	// HeaderDelivery identifies one attempt, so a receiver can correlate its
	// logs with ours across retries.
	HeaderDelivery = "X-Relaix-Delivery"
)

// ErrPermanent marks a failure that retrying cannot fix.
//
// The distinction matters because the two cases want opposite treatment: a
// receiver that is down deserves patient retries, while a receiver answering
// 400 or 404 will answer the same way in an hour, and hammering it is just
// noise in someone else's logs.
var ErrPermanent = errors.New("callback: permanent failure")

// Payload is the JSON body of a callback.
//
// Field names are the API's public vocabulary — camelCase, matching what the
// REST API accepts — rather than the database's column names, so the storage
// schema can change without breaking every receiver.
type Payload struct {
	JobID        string  `json:"jobId"`
	Status       string  `json:"status"`
	Recipient    string  `json:"recipient"`
	DeviceID     *string `json:"deviceId,omitempty"`
	PartsSent    int     `json:"partsSent"`
	ErrorCode    string  `json:"errorCode,omitempty"`
	ErrorMessage string  `json:"errorMessage,omitempty"`
	CompletedAt  *string `json:"completedAt,omitempty"`
	Attempt      int     `json:"attempt"`
}

// Options configures a Notifier.
type Options struct {
	// Secret signs every callback. Required.
	Secret []byte

	// Timeout bounds one attempt. Default 10s: long enough for a slow
	// receiver, short enough that one hung endpoint cannot hold a watcher slot
	// open indefinitely.
	Timeout time.Duration

	// Client is used if set, otherwise one is built from Timeout.
	Client *http.Client

	Now    func() time.Time
	Logger *slog.Logger
}

func (o *Options) withDefaults() {
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Second
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: o.Timeout}
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// Notifier POSTs signed job outcomes.
type Notifier struct {
	opts Options
}

// NewNotifier creates a Notifier.
func NewNotifier(opts Options) *Notifier {
	opts.withDefaults()
	return &Notifier{opts: opts}
}

// Notify delivers one job outcome. It returns nil when the receiver accepted
// the callback, an error wrapping ErrPermanent when retrying is pointless, and
// a plain error when it is worth trying again.
func (n *Notifier) Notify(ctx context.Context, job *store.Job) error {
	if job.Callback.URL == "" {
		return nil
	}

	body, err := json.Marshal(payloadFor(job))
	if err != nil {
		// A payload we cannot marshal will not marshal on the next attempt
		// either.
		return fmt.Errorf("%w: marshal payload: %v", ErrPermanent, err)
	}

	ts := n.opts.Now().UTC()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, job.Callback.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrPermanent, err)
	}

	stamp := strconv.FormatInt(ts.Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "relaix-gateway")
	req.Header.Set(HeaderTimestamp, stamp)
	req.Header.Set(HeaderDelivery, job.ID.String())
	req.Header.Set(HeaderSignature, "sha256="+Sign(n.opts.Secret, stamp, body))

	resp, err := n.opts.Client.Do(req)
	if err != nil {
		// Connection refused, DNS failure, timeout: all worth retrying.
		return fmt.Errorf("callback: post: %w", err)
	}
	defer resp.Body.Close()

	// Drain before closing so the connection can be reused rather than torn
	// down on every callback.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusTooManyRequests:
		// Explicitly a "come back later", not a rejection.
		return fmt.Errorf("callback: rejected with %d", resp.StatusCode)
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		// The receiver understood and refused. It will refuse identically in an
		// hour, so stop.
		return fmt.Errorf("%w: status %d", ErrPermanent, resp.StatusCode)
	default:
		return fmt.Errorf("callback: status %d", resp.StatusCode)
	}
}

// Sign returns the hex HMAC-SHA256 over the timestamp and body.
//
// The timestamp is inside the signed material and separated from the body by a
// dot, so a receiver that checks freshness cannot be fooled by editing the
// header, and no body can be constructed that shifts the boundary.
func Sign(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks a received signature in constant time.
//
// Exported because it is the half receivers have to implement, and shipping it
// is cheaper than watching people reimplement an HMAC comparison with ==.
func Verify(secret []byte, timestamp string, body []byte, signature string) bool {
	expected := "sha256=" + Sign(secret, timestamp, body)
	return hmac.Equal([]byte(expected), []byte(signature))
}

func payloadFor(job *store.Job) Payload {
	p := Payload{
		JobID:        job.ID.String(),
		Status:       string(job.Status),
		Recipient:    job.Recipient,
		PartsSent:    job.PartsSent,
		ErrorCode:    job.ErrorCode,
		ErrorMessage: job.ErrorMessage,
		// One-based: the first delivery is attempt 1, not attempt 0, because
		// the number appears in receivers' logs and support conversations.
		Attempt: job.Callback.Attempts + 1,
	}
	if job.AssignedDeviceID != nil {
		id := job.AssignedDeviceID.String()
		p.DeviceID = &id
	}
	if job.CompletedAt != nil {
		at := job.CompletedAt.UTC().Format(time.RFC3339)
		p.CompletedAt = &at
	}
	return p
}
