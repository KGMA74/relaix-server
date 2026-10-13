package scheduler

import (
	"context"
	"testing"

	"github.com/KGMA74/relaix-server/policy"
	"github.com/KGMA74/relaix-server/store"
	"github.com/KGMA74/relaix-server/store/storetest"
)

func TestUSSDGoesToItsDeviceAsAUssdJob(t *testing.T) {
	st := storetest.New()
	dev := st.SeedDevice(&store.Device{Enabled: true, Capabilities: []string{store.CapUSSD}}).ID
	// Sending limits are about SMS: a device at its cap still runs a code.
	full := st.SeedDevice(&store.Device{Enabled: true, Capabilities: []string{store.CapUSSD},
		Policy: policy.Policy{Accept: policy.AcceptRules{Countries: []string{"FR"}}}}).ID
	h := newFakeHub(dev, full)
	s := newScheduler(t, st, h, Options{})

	job := st.SeedJob(&store.Job{Kind: store.KindUSSD, Body: "*144#", Mode: store.ModeQueued,
		RequestedDeviceID: &full, SubscriptionID: ptr(int32(2))})
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}

	if got := st.JobByID(job.ID); got.Status != store.JobAssigned {
		t.Fatalf("status = %q, want assigned (recipient rules do not apply to USSD)", got.Status)
	}
	msgs := h.sentTo(full)
	if len(msgs) != 1 {
		t.Fatalf("sent %d messages, want 1", len(msgs))
	}
	u := msgs[0].GetUssdJob()
	if u == nil || u.GetCode() != "*144#" || u.GetSubscriptionId() != 2 || u.GetJobId() != job.ID.String() {
		t.Errorf("pushed %v, want a UssdJob for *144# on SIM 2", msgs[0])
	}
	if len(h.sentTo(dev)) != 0 {
		t.Error("a USSD job went to a device it did not name")
	}
}

func TestUSSDFailsOnAnAgentThatCannotRunIt(t *testing.T) {
	st := storetest.New()
	old := st.SeedDevice(&store.Device{Enabled: true}).ID
	single := st.SeedDevice(&store.Device{Enabled: true, Capabilities: []string{store.CapUSSD}}).ID
	h := newFakeHub(old, single)
	s := newScheduler(t, st, h, Options{})

	plain := st.SeedJob(&store.Job{Kind: store.KindUSSD, Body: "*144#", Mode: store.ModeQueued, RequestedDeviceID: &old})
	menu := st.SeedJob(&store.Job{Kind: store.KindUSSD, Body: "*144#", Mode: store.ModeQueued,
		RequestedDeviceID: &single, Replies: []string{"1"}})
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	for _, j := range []*store.Job{plain, menu} {
		got := st.JobByID(j.ID)
		if got.Status != store.JobFailed || got.ErrorCode != "unsupported_by_device" {
			t.Errorf("job %v: status %q code %q, want failed unsupported_by_device", j.Body, got.Status, got.ErrorCode)
		}
	}
	if h.totalSent() != 0 {
		t.Errorf("pushed %d jobs to agents that would drop them", h.totalSent())
	}
}

func TestUSSDWaitsForItsDeviceOrFailsWhenImmediate(t *testing.T) {
	st := storetest.New()
	dev := st.SeedDevice(&store.Device{Enabled: true, Capabilities: []string{store.CapUSSD}}).ID
	h := newFakeHub() // nothing ready
	s := newScheduler(t, st, h, Options{})

	queued := st.SeedJob(&store.Job{Kind: store.KindUSSD, Body: "*144#", Mode: store.ModeQueued, RequestedDeviceID: &dev})
	now := st.SeedJob(&store.Job{Kind: store.KindUSSD, Body: "*144#", Mode: store.ModeImmediate, RequestedDeviceID: &dev})
	none := st.SeedJob(&store.Job{Kind: store.KindUSSD, Body: "*144#", Mode: store.ModeQueued})
	if err := s.Tick(context.Background()); err != nil {
		t.Fatalf("Tick: %v", err)
	}
	if got := st.JobByID(queued.ID); got.Status != store.JobPending {
		t.Errorf("queued: status %q, want pending", got.Status)
	}
	if got := st.JobByID(now.ID); got.Status != store.JobFailed || got.ErrorCode != "device_unavailable" {
		t.Errorf("immediate: status %q code %q", got.Status, got.ErrorCode)
	}
	if got := st.JobByID(none.ID); got.Status != store.JobFailed || got.ErrorCode != "device_required" {
		t.Errorf("no device: status %q code %q", got.Status, got.ErrorCode)
	}
}
