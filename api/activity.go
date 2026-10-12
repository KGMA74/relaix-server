package api

import (
	"net/http"
	"time"

	"github.com/KGMA74/relaix-server/store"
)

// activityBucketFor picks a bucket width that gives the chart a readable
// number of columns: 12 to 30 for the windows the dashboard offers.
func activityBucketFor(window time.Duration) time.Duration {
	switch {
	case window <= time.Hour:
		return 5 * time.Minute
	case window <= 24*time.Hour:
		return time.Hour
	case window <= 7*24*time.Hour:
		return 6 * time.Hour
	default:
		return 24 * time.Hour
	}
}

type activityBucket struct {
	Start string `json:"start"`
	// Delivered counts sent and delivered: both mean the network took it.
	Delivered int `json:"delivered"`
	Failed    int `json:"failed"`
	// InFlight counts pending and assigned.
	InFlight  int `json:"inFlight"`
	Cancelled int `json:"cancelled"`
}

type activityTotals struct {
	Total     int `json:"total"`
	Delivered int `json:"delivered"`
	Failed    int `json:"failed"`
}

type activityResponse struct {
	Window        string           `json:"window"`
	BucketSeconds int              `json:"bucketSeconds"`
	Buckets       []activityBucket `json:"buckets"`
	// Current covers the buckets above; Previous the window just before it,
	// so a figure can say whether it went up or down.
	Current  activityTotals `json:"current"`
	Previous activityTotals `json:"previous"`
}

// handleActivity serves the overview's chart: jobs per bucket and outcome.
//
//	GET /stats/activity?window=24h
//
// Buckets are aligned on their width (whole hours, whole days in UTC) and the
// last one is the one now falls in, so the series does not shift while it is
// being watched. Every bucket is present, empty ones included.
func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	window := 24 * time.Hour
	if raw := r.URL.Query().Get("window"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 || d > maxStatsWindow {
			writeError(w, http.StatusBadRequest, "invalid_window",
				"window must be a positive duration of at most 2160h")
			return
		}
		window = d
	}
	bucket := activityBucketFor(window)
	n := int((window + bucket - 1) / bucket)

	end := s.opts.Now().UTC().Truncate(bucket).Add(bucket)
	// Two windows: the one shown, and the one before it for the comparison.
	from := end.Add(-time.Duration(2*n) * bucket)

	counts, err := s.store.Jobs().CountByBucket(r.Context(), store.AllAccounts(), from, bucket)
	if err != nil {
		s.opts.Logger.Error("could not count activity", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not compute activity")
		return
	}

	all := make([]activityBucket, 2*n)
	for i := range all {
		all[i].Start = from.Add(time.Duration(i) * bucket).Format(time.RFC3339)
	}
	for _, c := range counts {
		i := int(c.Start.Sub(from) / bucket)
		if i < 0 || i >= len(all) {
			continue
		}
		b := &all[i]
		switch c.Status {
		case store.JobSent, store.JobDelivered:
			b.Delivered += c.Count
		case store.JobFailed:
			b.Failed += c.Count
		case store.JobPending, store.JobAssigned:
			b.InFlight += c.Count
		case store.JobCancelled:
			b.Cancelled += c.Count
		}
	}

	resp := activityResponse{
		Window:        window.String(),
		BucketSeconds: int(bucket.Seconds()),
		Buckets:       all[n:],
	}
	for i, b := range all {
		t := &resp.Previous
		if i >= n {
			t = &resp.Current
		}
		t.Total += b.Delivered + b.Failed + b.InFlight + b.Cancelled
		t.Delivered += b.Delivered
		t.Failed += b.Failed
	}
	writeJSON(w, http.StatusOK, resp)
}
