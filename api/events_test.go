package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KGMA74/relaix-server/events"
)

// readEvent returns the next "event:" name and its "data:" line from an SSE
// stream, skipping comments and the retry hint.
func readEvent(t *testing.T, r *bufio.Reader) (name, data string) {
	t.Helper()
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = strings.TrimPrefix(line, "data: ")
		case line == "" && name != "":
			return name, data
		}
	}
}

func TestEventsStreamsPublishedChanges(t *testing.T) {
	broker := events.New()
	h := newHarness(t, Options{APIKey: testAPIKey, Events: broker})
	srv := httptest.NewServer(h.handler)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}

	r := bufio.NewReader(resp.Body)
	// "ready" first: from then on the subscription exists and nothing is missed.
	if name, _ := readEvent(t, r); name != "ready" {
		t.Fatalf("first event %q, want ready", name)
	}

	broker.Publish(events.Event{Type: events.DeviceEnrolled, DeviceID: "d-1", Reenrolled: true})
	name, data := readEvent(t, r)
	if name != events.DeviceEnrolled {
		t.Fatalf("event %q, want %s", name, events.DeviceEnrolled)
	}
	for _, want := range []string{`"deviceId":"d-1"`, `"reenrolled":true`, `"type":"device.enrolled"`} {
		if !strings.Contains(data, want) {
			t.Errorf("data %s lacks %s", data, want)
		}
	}
}

// The stream ends when the broker closes — what lets the server shut down
// instead of waiting on open dashboards.
func TestEventsStreamEndsWhenTheBrokerCloses(t *testing.T) {
	broker := events.New()
	h := newHarness(t, Options{APIKey: testAPIKey, Events: broker})
	srv := httptest.NewServer(h.handler)
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/events", nil)
	req.Header.Set("Authorization", "Bearer "+testAPIKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()
	r := bufio.NewReader(resp.Body)
	readEvent(t, r)

	broker.Close()
	done := make(chan struct{})
	go func() {
		for {
			if _, err := r.ReadString('\n'); err != nil {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream still open after the broker closed")
	}
}

func TestEventsRequiresTheKey(t *testing.T) {
	h := newHarness(t, Options{APIKey: testAPIKey, Events: events.New()})
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/events", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}
