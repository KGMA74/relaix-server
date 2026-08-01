package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/KGMA74/relaix-server/hub"
	"github.com/KGMA74/relaix-server/store"
)

// devicePatchRequest is the operator kill switch: reversible, and it leaves
// the row in place so the fleet list still shows the phone exists.
type devicePatchRequest struct {
	Enabled *bool `json:"enabled"`
}

// handleDeleteDevice retires a phone permanently.
//
// The jobs it sent survive — their device columns are ON DELETE SET NULL — so
// deleting a handset removes it from the fleet without erasing the record of
// what it did.
//
// An open stream dies on its own rather than being torn down here: every
// message on Connect is authenticated against the stored token hash, which is
// gone with the row, so the next frame is rejected. Disabling works the same
// way, which is why neither needs to reach into the hub.
func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parseID(w, r)
	if !ok {
		return
	}

	err := s.store.Devices().Delete(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such device")
		return
	case err != nil:
		s.opts.Logger.Error("could not delete device", "device_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not delete device")
		return
	}

	s.opts.Logger.Info("device deleted", "device_id", id)
	w.WriteHeader(http.StatusNoContent)
}

// handlePatchDevice flips the enabled flag.
//
// Separate from DELETE on purpose: an operator silencing a phone for an
// afternoon and one retiring it for good want different things, and only one
// of them is reversible.
func (s *Server) handlePatchDevice(w http.ResponseWriter, r *http.Request) {
	id, ok := s.parseID(w, r)
	if !ok {
		return
	}

	var req devicePatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "body is not valid JSON")
		return
	}
	if req.Enabled == nil {
		// A pointer, so "enabled": false is distinguishable from an empty
		// body; without that check the zero value would silently disable a
		// device on a malformed request.
		writeError(w, http.StatusBadRequest, "invalid_request", "enabled is required")
		return
	}

	ctx := r.Context()
	err := s.store.Devices().SetEnabled(ctx, id, *req.Enabled)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "no such device")
		return
	case err != nil:
		s.opts.Logger.Error("could not update device", "device_id", id, "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not update device")
		return
	}

	s.opts.Logger.Info("device enabled flag set", "device_id", id, "enabled", *req.Enabled)

	device, err := s.store.Devices().Get(ctx, id)
	if err != nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, deviceResponse{
		DeviceID:    device.ID.String(),
		Label:       device.Label,
		PhoneNumber: device.PhoneNumber,
		Enabled:     device.Enabled,
		Model:       device.Model,
		OSVersion:   device.OSVersion,
		Carrier:     device.Carrier,
		CreatedAt:   device.CreatedAt.UTC().Format(time.RFC3339),
	})
}

// deviceResponse merges what the database knows about a device with what the
// hub knows about its connection. Neither half is enough on its own: the
// database has the identity and the hub has the liveness.
type deviceResponse struct {
	DeviceID    string          `json:"deviceId"`
	Label       string          `json:"label"`
	PhoneNumber string          `json:"phoneNumber,omitempty"`
	Enabled     bool            `json:"enabled"`
	Connected   bool            `json:"connected"`
	Ready       bool            `json:"ready"`
	Model       string          `json:"model,omitempty"`
	OSVersion   string          `json:"osVersion,omitempty"`
	Carrier     string          `json:"carrier,omitempty"`
	Health      *healthResponse `json:"health,omitempty"`
	LastSeenAt  *string         `json:"lastSeenAt,omitempty"`
	CreatedAt   string          `json:"createdAt"`
}

type healthResponse struct {
	BatteryLevel int  `json:"batteryLevel"`
	IsCharging   bool `json:"isCharging"`
	// SignalStrength is the normalized 0-4 level, not dBm — see
	// docs/protocol.md for why raw values are not comparable across a fleet.
	SignalStrength int    `json:"signalStrength"`
	NetworkType    string `json:"networkType,omitempty"`
	SimReady       bool   `json:"simReady"`
	SentLastHour   int    `json:"sentLastHour"`
	PermissionsOK  bool   `json:"permissionsOk"`
	ReportedAt     string `json:"reportedAt"`
}

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	stored, err := s.store.Devices().List(ctx)
	if err != nil {
		s.opts.Logger.Error("could not list devices", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list devices")
		return
	}

	connected, err := s.hub.ListConnected(ctx)
	if err != nil {
		s.opts.Logger.Error("could not list connections", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list devices")
		return
	}
	ready, err := s.hub.ListReady(ctx)
	if err != nil {
		s.opts.Logger.Error("could not list ready devices", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not list devices")
		return
	}

	live := make(map[string]hub.DeviceState, len(connected))
	for _, d := range connected {
		live[d.DeviceID.String()] = d
	}
	isReady := make(map[string]bool, len(ready))
	for _, d := range ready {
		isReady[d.DeviceID.String()] = true
	}

	out := make([]deviceResponse, 0, len(stored))
	for _, d := range stored {
		id := d.ID.String()
		conn, online := live[id]

		resp := deviceResponse{
			DeviceID:    id,
			Label:       d.Label,
			PhoneNumber: d.PhoneNumber,
			Enabled:     d.Enabled,
			Connected:   online,
			Ready:       isReady[id],
			Model:       d.Model,
			OSVersion:   d.OSVersion,
			Carrier:     d.Carrier,
			CreatedAt:   d.CreatedAt.UTC().Format(time.RFC3339),
		}

		// Prefer the hub's health: it is the live snapshot, while the stored
		// one is whatever survived the last write.
		health := d.Health
		if online && conn.Health != nil {
			health = conn.Health
		}
		resp.Health = toHealthResponse(health)

		if online {
			at := conn.LastSeenAt.UTC().Format(time.RFC3339)
			resp.LastSeenAt = &at
		} else if d.LastSeenAt != nil {
			at := d.LastSeenAt.UTC().Format(time.RFC3339)
			resp.LastSeenAt = &at
		}

		out = append(out, resp)
	}

	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// enrollTokenResponse is what an operator gets to onboard a phone.
type enrollTokenResponse struct {
	// Token is returned once and never stored in plaintext.
	Token     string `json:"token"`
	ExpiresAt string `json:"expiresAt"`
	// Payload is what the QR encodes: the endpoint alongside the token, so the
	// agent needs no separate manual configuration step.
	Payload string `json:"payload"`
	// QRCodePNG is the same payload as a base64 PNG, for embedding directly in
	// an operator UI. Request ?format=png to get the image itself instead.
	QRCodePNG string `json:"qrCodePngBase64"`
}

// enrollPayload is the JSON encoded into the QR code.
type enrollPayload struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
}

func (s *Server) handleEnrollToken(w http.ResponseWriter, r *http.Request) {
	if s.enroller == nil {
		writeError(w, http.StatusNotImplemented, "not_configured",
			"enrollment is not configured")
		return
	}

	minted, err := s.enroller.MintEnrollmentToken(r.Context())
	if err != nil {
		s.opts.Logger.Error("could not mint enrollment token", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not mint token")
		return
	}

	payload, err := json.Marshal(enrollPayload{
		Endpoint: s.opts.PublicURL,
		Token:    minted.Plaintext,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "could not build payload")
		return
	}

	// Medium recovery: the QR is scanned off a screen at close range, so the
	// extra redundancy of a higher level would only make the code denser for
	// no gain.
	png, err := qrcode.Encode(string(payload), qrcode.Medium, 512)
	if err != nil {
		s.opts.Logger.Error("could not render qr code", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not render qr code")
		return
	}

	if r.URL.Query().Get("format") == "png" {
		// The token is in the image and nowhere else in this response, so it
		// must not be cached by anything between here and the operator.
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(png)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, enrollTokenResponse{
		Token:     minted.Plaintext,
		ExpiresAt: minted.Record.ExpiresAt.UTC().Format(time.RFC3339),
		Payload:   string(payload),
		QRCodePNG: base64.StdEncoding.EncodeToString(png),
	})
}

func toHealthResponse(h *store.DeviceHealth) *healthResponse {
	if h == nil {
		return nil
	}
	return &healthResponse{
		BatteryLevel:   h.BatteryLevel,
		IsCharging:     h.IsCharging,
		SignalStrength: h.SignalStrength,
		NetworkType:    h.NetworkType,
		SimReady:       h.SimReady,
		SentLastHour:   h.SentLastHour,
		PermissionsOK:  h.PermissionsOK,
		ReportedAt:     h.ReportedAt.UTC().Format(time.RFC3339),
	}
}
