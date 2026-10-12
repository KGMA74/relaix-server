package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/KGMA74/relaix-server/store"
)

func TestActivityBucketsCountsAndPreviousWindow(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey, Now: func() time.Time { return time.Now() }})
	ctx := context.Background()
	for range 3 {
		if _, err := h.store.Jobs().Create(ctx, &store.Job{Recipient: "+22670000000", Body: "x", Mode: store.ModeQueued}); err != nil {
			t.Fatal(err)
		}
	}

	rec := h.do(t, http.MethodGet, "/stats/activity?window=24h", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	got := decode[activityResponse](t, rec)
	if got.BucketSeconds != 3600 || len(got.Buckets) != 24 {
		t.Fatalf("bucket %ds × %d, want 3600s × 24", got.BucketSeconds, len(got.Buckets))
	}
	// The jobs were created now, so they land in the last bucket.
	if last := got.Buckets[len(got.Buckets)-1]; last.InFlight != 3 {
		t.Errorf("last bucket = %+v, want 3 in flight", last)
	}
	if got.Current.Total != 3 || got.Previous.Total != 0 {
		t.Errorf("current %+v previous %+v, want 3 and 0", got.Current, got.Previous)
	}
	first, _ := time.Parse(time.RFC3339, got.Buckets[0].Start)
	if first.Minute() != 0 || first.Second() != 0 {
		t.Errorf("buckets not aligned on the hour: %v", first)
	}
}

func TestActivityRejectsBadWindow(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	if rec := h.do(t, http.MethodGet, "/stats/activity?window=nope", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}
