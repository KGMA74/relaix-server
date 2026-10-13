package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/KGMA74/relaix-server/store"
	"github.com/google/uuid"
)

// inboundResponse is one received SMS.
type inboundResponse struct {
	MessageID      string  `json:"messageId"`
	DeviceID       *string `json:"deviceId,omitempty"`
	SubscriptionID *int32  `json:"subscriptionId,omitempty"`
	Sender         string  `json:"sender"`
	Body           string  `json:"body"`
	ReceivedAt     string  `json:"receivedAt"`
	CreatedAt      string  `json:"createdAt"`
	// AutomationID and Fields: set when an automation forwarded it.
	AutomationID string            `json:"automationId,omitempty"`
	Fields       map[string]string `json:"fields,omitempty"`
	// Webhook is where the message was posted, when it was: delivered, still
	// retrying, or given up on.
	Webhook *webhookResponse `json:"webhook,omitempty"`
}

type webhookResponse struct {
	Delivered bool   `json:"delivered"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"lastError,omitempty"`
}

type listInboxResponse struct {
	Messages   []inboundResponse `json:"messages"`
	NextCursor string            `json:"nextCursor,omitempty"`
}

// handleListInbox serves the SMS the phones received and forwarded, most
// recent first.
//
//	GET /inbox?deviceId=…&sender=OrangeMoney&from=…&to=…&limit=50&cursor=…
func (s *Server) handleListInbox(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.InboundFilter{Limit: defaultListLimit, Sender: strings.TrimSpace(q.Get("sender"))}
	if p := principal(r); !p.AllAccounts {
		f.AccountID = &p.AccountID
	}
	if raw := q.Get("deviceId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_device_id", "deviceId must be a uuid")
			return
		}
		f.DeviceID = &id
	}
	for _, bound := range []struct {
		name string
		dst  **time.Time
	}{{"from", &f.ReceivedFrom}, {"to", &f.ReceivedTo}} {
		if raw := q.Get(bound.name); raw != "" {
			at, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_"+bound.name, bound.name+" must be RFC 3339")
				return
			}
			*bound.dst = &at
		}
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be a positive integer")
			return
		}
		f.Limit = min(n, maxListLimit)
	}
	if raw := q.Get("cursor"); raw != "" {
		c, err := decodeCursor(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_cursor", "cursor is not one this server issued")
			return
		}
		f.After = &store.InboundCursor{ReceivedAt: c.CreatedAt, ID: c.ID}
	}

	// One extra row tells whether another page exists.
	limit := f.Limit
	f.Limit = limit + 1
	msgs, err := s.store.Inbound().List(r.Context(), f)
	if err != nil {
		s.opts.Logger.Error("could not list received SMS", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list received SMS")
		return
	}
	resp := listInboxResponse{Messages: make([]inboundResponse, 0, min(len(msgs), limit))}
	if len(msgs) > limit {
		msgs = msgs[:limit]
		last := msgs[len(msgs)-1]
		resp.NextCursor = encodeCursor(&store.JobCursor{CreatedAt: last.ReceivedAt, ID: last.ID})
	}
	for _, m := range msgs {
		resp.Messages = append(resp.Messages, toInboundResponse(m))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGetInbound(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parseID(w, r)
	if !ok {
		return
	}
	m, err := s.store.Inbound().Get(r.Context(), id)
	// Another account's message is answered like one that does not exist.
	if errors.Is(err, store.ErrNotFound) || (err == nil && !principal(r).Owns(m.AccountID)) {
		writeError(w, http.StatusNotFound, "not_found", "no such message")
		return
	}
	if err != nil {
		s.opts.Logger.Error("could not read received SMS", "id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not read message")
		return
	}
	writeJSON(w, http.StatusOK, toInboundResponse(m))
}

func toInboundResponse(m *store.InboundMessage) inboundResponse {
	resp := inboundResponse{
		MessageID:      m.ID.String(),
		SubscriptionID: m.SubscriptionID,
		Sender:         m.Sender,
		Body:           m.Body,
		ReceivedAt:     m.ReceivedAt.UTC().Format(time.RFC3339),
		CreatedAt:      m.CreatedAt.UTC().Format(time.RFC3339),
		AutomationID:   m.AutomationID,
	}
	if len(m.Fields) > 0 {
		resp.Fields = m.Fields
	}
	if m.DeviceID != nil {
		id := m.DeviceID.String()
		resp.DeviceID = &id
	}
	if m.Webhook.URL != "" {
		resp.Webhook = &webhookResponse{
			Delivered: m.Webhook.DeliveredAt != nil,
			Attempts:  m.Webhook.Attempts,
			LastError: m.Webhook.LastError,
		}
	}
	return resp
}
