package api

import (
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/KGMA74/relaix-server/policy"
	"github.com/KGMA74/relaix-server/store"
)

// USSD limits. A USSD string is at most 182 characters on the air interface
// (GSM 03.38 packing of 160 octets); a reply typed into a menu is a short
// answer, and a session of more than a handful of screens is a menu the
// caller has lost their way in.
const (
	maxUSSDRunes  = 182
	maxReplyRunes = 182
	maxReplies    = 10
)

// ussdRequest is the body of POST /ussd.
type ussdRequest struct {
	// DeviceID is required: a code runs against one SIM's account, and
	// another phone's answer would be about another account.
	DeviceID *string `json:"deviceId"`
	// Code is dialed as given, e.g. "*144#" or "*144*2*1*70000000*5000#".
	Code string `json:"code"`
	// Replies are typed, in order, into the menus the code opens.
	Replies []string `json:"replies,omitempty"`
	// Paging follows an answer that ends with "n Suivant" over its screens.
	Paging *policy.Paging `json:"paging,omitempty"`
	// Extract is a pattern whose named groups, found in what the network
	// displayed, are returned as the job's fields.
	Extract string `json:"extract,omitempty"`
	// Sensitive keeps the code and replies out of every listing and wipes
	// them once the job is over: for a code or a reply carrying a PIN.
	Sensitive bool `json:"sensitive,omitempty"`

	Mode           string  `json:"mode,omitempty"`
	Priority       int     `json:"priority,omitempty"`
	ScheduledAt    *string `json:"scheduledAt,omitempty"`
	ExpiresAt      *string `json:"expiresAt,omitempty"`
	CallbackURL    string  `json:"callbackUrl,omitempty"`
	SubscriptionID *int32  `json:"subscriptionId,omitempty"`
}

// handleUSSD runs a USSD code on one phone. The answer comes back like an
// SMS outcome: GET /jobs/{id} (responses), the callback, the live stream.
func (s *Server) handleUSSD(w http.ResponseWriter, r *http.Request) {
	var req ussdRequest
	if err := decodeJSON(w, r, s.opts.MaxBodyBytes, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	job, apiErr := s.buildUSSDJob(&req)
	if apiErr != nil {
		writeError(w, apiErr.status, apiErr.code, apiErr.message)
		return
	}
	created, ok := s.createJobs(w, r, []*store.Job{job})
	if !ok {
		return
	}
	writeJSON(w, http.StatusAccepted, toJobResponse(created[0]))
}

func (s *Server) buildUSSDJob(req *ussdRequest) (*store.Job, *apiError) {
	if req.DeviceID == nil || *req.DeviceID == "" {
		return nil, badRequest("missing_device", "deviceId is required: a USSD code runs on one phone's SIM")
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		return nil, badRequest("missing_code", "code is required")
	}
	if !validUSSD(code) {
		return nil, badRequest("invalid_code",
			"code must be a USSD string: digits, * and #, starting with * or # and ending with #, e.g. *144#; {pin} stands for the SIM's PIN kept on the phone")
	}
	if len(req.Replies) > maxReplies {
		return nil, badRequest("too_many_replies", fmt.Sprintf("at most %d replies", maxReplies))
	}
	replies := make([]string, len(req.Replies))
	for i, reply := range req.Replies {
		reply = strings.TrimSpace(reply)
		if reply == "" || utf8.RuneCountInString(reply) > maxReplyRunes || strings.IndexFunc(reply, unicode.IsControl) >= 0 {
			return nil, badRequest("invalid_reply",
				fmt.Sprintf("reply %d must be 1 to %d printable characters", i, maxReplyRunes))
		}
		replies[i] = reply
	}

	job, apiErr := s.baseJob(&jobOptions{
		DeviceID: req.DeviceID, Mode: req.Mode, Priority: req.Priority,
		ScheduledAt: req.ScheduledAt, ExpiresAt: req.ExpiresAt, CallbackURL: req.CallbackURL,
		SubscriptionID: req.SubscriptionID,
	})
	if apiErr != nil {
		return nil, apiErr
	}
	if req.Paging != nil {
		p := *req.Paging
		p.Marker, p.Reply = strings.TrimSpace(p.Marker), strings.TrimSpace(p.Reply)
		if err := policy.ValidatePaging(&p); err != nil {
			return nil, badRequest("invalid_paging", err.Error())
		}
		req.Paging = &p
	}
	extract := strings.TrimSpace(req.Extract)
	if err := policy.ValidateExtract(extract); err != nil {
		return nil, badRequest("invalid_extract", err.Error())
	}

	job.Kind = store.KindUSSD
	job.Body = code
	job.Replies = replies
	job.Paging = req.Paging
	job.Extract = extract
	job.Sensitive = req.Sensitive
	return job, nil
}

// validUSSD checks the shape of a USSD string: a service code opens with * or
// # and closes with #, with digits and separators between. Whether the
// network knows it is the network's answer to give. {pin} may stand for the
// SIM's PIN, which only the phone holds.
func validUSSD(code string) bool {
	if len(code) < 2 || utf8.RuneCountInString(code) > maxUSSDRunes {
		return false
	}
	code = strings.ReplaceAll(code, store.PinPlaceholder, "0")
	if (code[0] != '*' && code[0] != '#') || code[len(code)-1] != '#' {
		return false
	}
	for _, c := range code {
		if !(c >= '0' && c <= '9') && c != '*' && c != '#' && c != '+' {
			return false
		}
	}
	return true
}
