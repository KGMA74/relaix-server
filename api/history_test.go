package api

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/KGMA74/relaix-server/hub"
	"github.com/KGMA74/relaix-server/store"
)

// seedHistory writes n jobs one minute apart, oldest first, and returns them
// in that order.
func seedHistory(h *harness, n int, mutate func(i int, j *store.Job)) []*store.Job {
	out := make([]*store.Job, n)
	for i := range n {
		j := &store.Job{
			Recipient: "+33600000000",
			Body:      "hello",
			Mode:      store.ModeQueued,
			CreatedAt: testNow.Add(time.Duration(i-n) * time.Minute),
		}
		if mutate != nil {
			mutate(i, j)
		}
		out[i] = h.store.SeedJob(j)
	}
	return out
}

func TestListJobsIsNewestFirstAndPages(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	jobs := seedHistory(h, 5, nil)

	var seen []string
	path := "/jobs?limit=2"
	for page := 0; ; page++ {
		if page > 5 {
			t.Fatal("paging did not terminate")
		}
		rec := h.do(t, http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("page %d: status %d: %s", page, rec.Code, rec.Body)
		}
		resp := decode[listJobsResponse](t, rec)
		for _, j := range resp.Jobs {
			seen = append(seen, j.JobID)
		}
		if resp.NextCursor == "" {
			break
		}
		path = "/jobs?limit=2&cursor=" + resp.NextCursor
	}

	if len(seen) != len(jobs) {
		t.Fatalf("listed %d jobs, want %d", len(seen), len(jobs))
	}
	for i, id := range seen {
		if want := jobs[len(jobs)-1-i].ID.String(); id != want {
			t.Errorf("position %d = %s, want %s", i, id, want)
		}
	}
}

func TestListJobsLastFullPageHasNoCursor(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	seedHistory(h, 2, nil)

	resp := decode[listJobsResponse](t, h.do(t, http.MethodGet, "/jobs?limit=2", nil))
	if len(resp.Jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(resp.Jobs))
	}
	if resp.NextCursor != "" {
		t.Error("an exactly full last page must not offer a next cursor")
	}
}

func TestListJobsCarriesTheBody(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	seedHistory(h, 1, func(_ int, j *store.Job) { j.Body = "votre code est 1234" })

	resp := decode[listJobsResponse](t, h.do(t, http.MethodGet, "/jobs", nil))
	if len(resp.Jobs) != 1 || resp.Jobs[0].Body != "votre code est 1234" {
		t.Fatalf("history lost the message body: %+v", resp.Jobs)
	}
}

func TestListJobsFilters(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	dev := uuid.New()
	jobs := seedHistory(h, 4, func(i int, j *store.Job) {
		switch i {
		case 0:
			j.Recipient = "+237670000000"
		case 1:
			j.Status = store.JobSent
			j.AssignedDeviceID = &dev
		case 2:
			j.Status = store.JobFailed
			j.RequestedDeviceID = &dev
		}
	})

	cases := []struct {
		name  string
		query url.Values
		want  []*store.Job
	}{
		{"status", url.Values{"status": {"sent,failed"}}, []*store.Job{jobs[2], jobs[1]}},
		{"device", url.Values{"deviceId": {dev.String()}}, []*store.Job{jobs[2], jobs[1]}},
		{"recipient prefix", url.Values{"recipient": {"+237"}}, []*store.Job{jobs[0]}},
		{"from is inclusive", url.Values{"from": {jobs[2].CreatedAt.Format(time.RFC3339)}}, []*store.Job{jobs[3], jobs[2]}},
		{"to is exclusive", url.Values{"to": {jobs[1].CreatedAt.Format(time.RFC3339)}}, []*store.Job{jobs[0]}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := h.do(t, http.MethodGet, "/jobs?"+tc.query.Encode(), nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			resp := decode[listJobsResponse](t, rec)
			if len(resp.Jobs) != len(tc.want) {
				t.Fatalf("got %d jobs, want %d", len(resp.Jobs), len(tc.want))
			}
			for i, w := range tc.want {
				if resp.Jobs[i].JobID != w.ID.String() {
					t.Errorf("position %d = %s, want %s", i, resp.Jobs[i].JobID, w.ID)
				}
			}
		})
	}
}

func TestListJobsValidation(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})

	for query, code := range map[string]string{
		"status=bogus":   "invalid_status",
		"deviceId=nope":  "invalid_device_id",
		"from=yesterday": "invalid_from",
		"to=2026-13-01":  "invalid_to",
		"limit=0":        "invalid_limit",
		"limit=abc":      "invalid_limit",
		"cursor=!!!":     "invalid_cursor",
		"cursor=YWJj":    "invalid_cursor",
	} {
		rec := h.do(t, http.MethodGet, "/jobs?"+query, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", query, rec.Code)
			continue
		}
		if got := decode[errorBody](t, rec).Error.Code; got != code {
			t.Errorf("%s: code %q, want %q", query, got, code)
		}
	}
}

func TestListJobsCapsTheLimit(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	seedHistory(h, maxListLimit+5, nil)

	resp := decode[listJobsResponse](t, h.do(t, http.MethodGet, "/jobs?limit=100000", nil))
	if len(resp.Jobs) != maxListLimit {
		t.Errorf("got %d jobs, want the %d cap", len(resp.Jobs), maxListLimit)
	}
	if resp.NextCursor == "" {
		t.Error("a capped page must still offer the rest")
	}
}

func TestJobEventsTimeline(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	job := seedHistory(h, 1, nil)[0]
	dev := uuid.New()

	ctx := context.Background()
	for i, e := range []*store.JobEvent{
		{JobID: job.ID, Status: store.JobPending, Reason: "accepted via api"},
		{JobID: job.ID, Status: store.JobAssigned, DeviceID: &dev},
		{JobID: job.ID, Status: store.JobSent},
	} {
		e.CreatedAt = testNow.Add(time.Duration(i) * time.Second)
		if err := h.store.Events().Append(ctx, e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	rec := h.do(t, http.MethodGet, "/jobs/"+job.ID.String()+"/events", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	resp := decode[struct {
		Events []eventResponse `json:"events"`
	}](t, rec)
	if len(resp.Events) != 3 {
		t.Fatalf("got %d events, want 3", len(resp.Events))
	}
	if resp.Events[0].Status != "pending" || resp.Events[2].Status != "sent" {
		t.Errorf("events out of order: %+v", resp.Events)
	}
	if resp.Events[1].DeviceID == nil || *resp.Events[1].DeviceID != dev.String() {
		t.Errorf("assignment event lost its device: %+v", resp.Events[1])
	}
}

func TestJobEventsUnknownJob(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})

	if rec := h.do(t, http.MethodGet, "/jobs/"+uuid.NewString()+"/events", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown job: status %d, want 404", rec.Code)
	}
	if rec := h.do(t, http.MethodGet, "/jobs/nope/events", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: status %d, want 400", rec.Code)
	}
}

func TestStatsCountsTheWindowAndTheFleet(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	seedHistory(h, 3, func(i int, j *store.Job) {
		switch i {
		case 0:
			// Outside a one-hour window.
			j.CreatedAt = testNow.Add(-2 * time.Hour)
			j.Status = store.JobSent
		case 1:
			j.Status = store.JobFailed
		}
	})

	on := h.store.SeedDevice(&store.Device{Label: "on", Enabled: true})
	h.store.SeedDevice(&store.Device{Label: "off", Enabled: false})
	_, state := readyDevice()
	state.DeviceID = on.ID
	h.hub.connected = []hub.DeviceState{state}
	h.hub.ready = []hub.DeviceState{state}

	rec := h.do(t, http.MethodGet, "/stats?window=1h", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	resp := decode[statsResponse](t, rec)

	if resp.Total != 2 || resp.Jobs["failed"] != 1 || resp.Jobs["pending"] != 1 || resp.Jobs["sent"] != 0 {
		t.Errorf("job counts = %+v total %d, want 1 failed + 1 pending in the window", resp.Jobs, resp.Total)
	}
	if _, ok := resp.Jobs["delivered"]; !ok {
		t.Error("every status must be present, including zeros")
	}
	want := deviceCounts{Total: 2, Enabled: 1, Connected: 1, Ready: 1}
	if resp.Devices != want {
		t.Errorf("devices = %+v, want %+v", resp.Devices, want)
	}
}

func TestStatsRejectsABadWindow(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey})
	for _, w := range []string{"abc", "-1h", "0s", "10000h"} {
		if rec := h.do(t, http.MethodGet, "/stats?window="+w, nil); rec.Code != http.StatusBadRequest {
			t.Errorf("window=%s: status %d, want 400", w, rec.Code)
		}
	}
}
