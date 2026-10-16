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

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/policy"
	"github.com/KGMA74/relaix-server/store"
)

// Header names carried on every callback.
const (
	// HeaderSignature is "sha256=" followed by the hex MAC.
	HeaderSignature = "X-Relaix-Signature"
	// HeaderTimestamp is the unix second the request was signed. It is part of
	// the signed material, so it cannot be edited to extend a replay window.
	HeaderTimestamp = "X-Relaix-Timestamp"
	// HeaderDelivery names what the callback is about — the job, or the
	// received message — and stays the same across retries: a receiver that
	// has already handled a delivery id answers 2xx and does nothing, so a
	// retry after a lost response never credits a payment twice.
	HeaderDelivery = "X-Relaix-Delivery"
)

// ErrPermanent marks a failure that retrying cannot fix.
//
// The distinction matters because the two cases want opposite treatment: a
// receiver that is down deserves patient retries, while a receiver answering
// 400 or 404 will answer the same way in an hour, and hammering it is just
// noise in someone else's logs.
var ErrPermanent = errors.New("callback: permanent failure")

// Payload is the data of a job event (Event, JobEvent).
//
// Field names are the API's public vocabulary — camelCase, matching what the
// REST API accepts — rather than the database's column names, so the storage
// schema can change without breaking every receiver.
type Payload struct {
	JobID string `json:"jobId"`
	// Kind is "sms" or "ussd".
	Kind         string  `json:"kind"`
	Status       string  `json:"status"`
	Recipient    string  `json:"recipient"`
	DeviceID     *string `json:"deviceId,omitempty"`
	PartsSent    int     `json:"partsSent"`
	ErrorCode    string  `json:"errorCode,omitempty"`
	ErrorMessage string  `json:"errorMessage,omitempty"`
	CompletedAt  *string `json:"completedAt,omitempty"`
	Attempt      int     `json:"attempt"`
	// Responses is what the network displayed, for a USSD job.
	Responses []string `json:"responses,omitempty"`
}

// Options configures a Notifier.
type Options struct {
	// Secret signs every callback. Required.
	Secret []byte

	// SecretFor, when set, gives an account its own signing secret, so one
	// customer cannot forge callbacks to another. An empty result falls back
	// to Secret; an error is retried like a failed delivery.
	SecretFor func(ctx context.Context, account uuid.UUID) ([]byte, error)

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
	return n.post(ctx, job.Callback.URL, job.AccountID, job.ID, Event{Event: JobEvent(job), Data: payloadFor(job)})
}

// Event is the body of every webhook: what happened, and its payload. One
// shape for all of them, so a receiver routes on event before reading data.
type Event struct {
	Event string `json:"event"`
	Data  any    `json:"data"`
}

// Event names.
const (
	EventSMSReceived = "sms.received"
)

// JobEvent names a job's final state: "<kind>.<status>" — sms.sent,
// sms.delivered, sms.failed, sms.cancelled — except that a USSD code the
// network answered is ussd.completed: nothing was "sent" from the caller's
// point of view.
func JobEvent(job *store.Job) string {
	kind, status := string(job.Kind), string(job.Status)
	if kind == "" {
		kind = "sms"
	}
	if kind == "ussd" && job.Status == store.JobSent {
		status = "completed"
	}
	return kind + "." + status
}

// NotifyInbound posts one received SMS, signed and classified exactly like a
// job callback.
func (n *Notifier) NotifyInbound(ctx context.Context, m *store.InboundMessage) error {
	if m.Webhook.URL == "" {
		return nil
	}
	return n.post(ctx, m.Webhook.URL, m.AccountID, m.ID, Event{Event: EventSMSReceived, Data: inboundPayloadFor(m)})
}

// post signs and sends one payload. delivery names what it is about, for the
// receiver's logs; the account picks the signing secret.
func (n *Notifier) post(ctx context.Context, url string, account, delivery uuid.UUID, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		// A payload we cannot marshal will not marshal on the next attempt
		// either.
		return fmt.Errorf("%w: marshal payload: %v", ErrPermanent, err)
	}

	ts := n.opts.Now().UTC()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("%w: build request: %v", ErrPermanent, err)
	}

	secret := n.opts.Secret
	if n.opts.SecretFor != nil {
		own, err := n.opts.SecretFor(ctx, account)
		if err != nil {
			return fmt.Errorf("callback: signing secret: %w", err)
		}
		if len(own) > 0 {
			secret = own
		}
	}

	stamp := strconv.FormatInt(ts.Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "relaix-gateway")
	req.Header.Set(HeaderTimestamp, stamp)
	req.Header.Set(HeaderDelivery, delivery.String())
	req.Header.Set(HeaderSignature, "sha256="+Sign(secret, stamp, body))

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
		Kind:         string(job.Kind),
		Status:       string(job.Status),
		Responses:    job.Responses,
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

// InboundPayload is the data of an sms.received event.
type InboundPayload struct {
	MessageID      string  `json:"messageId"`
	DeviceID       *string `json:"deviceId,omitempty"`
	SubscriptionID *int32  `json:"subscriptionId,omitempty"`
	Sender         string  `json:"sender"`
	Body           string  `json:"body"`
	ReceivedAt     string  `json:"receivedAt"`
	Attempt        int     `json:"attempt"`
	// AutomationID and Fields: set when an automation forwarded it.
	AutomationID string            `json:"automationId,omitempty"`
	Fields       map[string]string `json:"fields,omitempty"`
	// Numbers repeats the fields that read as numbers, as numbers ("5 000"
	// gives 5000), so a receiver does not parse amounts itself.
	Numbers map[string]json.Number `json:"numbers,omitempty"`
}

func inboundPayloadFor(m *store.InboundMessage) InboundPayload {
	p := InboundPayload{
		MessageID:      m.ID.String(),
		SubscriptionID: m.SubscriptionID,
		Sender:         m.Sender,
		Body:           m.Body,
		ReceivedAt:     m.ReceivedAt.UTC().Format(time.RFC3339),
		Attempt:        m.Webhook.Attempts + 1,
		AutomationID:   m.AutomationID,
	}
	if len(m.Fields) > 0 {
		p.Fields = m.Fields
		for k, v := range policy.Numbers(m.Fields) {
			if p.Numbers == nil {
				p.Numbers = map[string]json.Number{}
			}
			p.Numbers[k] = json.Number(v)
		}
	}
	if m.DeviceID != nil {
		id := m.DeviceID.String()
		p.DeviceID = &id
	}
	return p
}
