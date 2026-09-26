package web

import (
	"fmt"
	"net/http/httptest"
	"testing"
	"time"
)

func states(kv ...string) map[string]string {
	m := make(map[string]string)
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	return m
}

func eventSummary(events []Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = fmt.Sprintf("%s/%s=%s(%s)", e.Kind, e.Name, e.State, e.Source)
	}
	return out
}

func assertEvents(t *testing.T, b *SSEBroker, want ...string) {
	t.Helper()
	got := eventSummary(b.Recent())
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func TestObserveFirstPollOnlySetsBaseline(t *testing.T) {
	b := NewSSEBroker()
	b.observe(kindVM, states("a", "running", "b", "stopped"))
	assertEvents(t, b)
}

func TestObserveLogsDetectedChangesOnce(t *testing.T) {
	b := NewSSEBroker()
	b.observe(kindVM, states("a", "stopped"))
	b.observe(kindVM, states("a", "running", "new", "created"))
	b.observe(kindVM, states("a", "running", "new", "created"))

	// Recent is newest first; map iteration order makes the two new events
	// arrive in either order, so check them as a set.
	got := eventSummary(b.Recent())
	if len(got) != 2 {
		t.Fatalf("events = %v, want 2", got)
	}
	want := map[string]bool{"vm/a=running(detected)": true, "vm/new=created(detected)": true}
	for _, g := range got {
		if !want[g] {
			t.Fatalf("unexpected event %q in %v", g, got)
		}
	}
}

func TestObserveDetectsDeletion(t *testing.T) {
	b := NewSSEBroker()
	b.observe(kindVM, states("a", "running"))
	b.observe(kindVM, states())
	b.observe(kindVM, states())
	assertEvents(t, b, "vm/a=deleted(detected)")
}

func TestObserveIgnoresFailedPoll(t *testing.T) {
	b := NewSSEBroker()
	b.observe(kindVM, states("a", "running"))
	b.observe(kindVM, nil) // listing failed: must not look like everything was deleted
	assertEvents(t, b)
}

func TestKindsAreTrackedSeparately(t *testing.T) {
	b := NewSSEBroker()
	b.observe(kindVM, states("lab", "running"))
	b.observe(kindCluster, states("lab", "creating"))
	b.observe(kindCluster, states("lab", "running"))
	b.observe(kindVM, states("lab", "running"))
	assertEvents(t, b, "cluster/lab=running(detected)")
}

func TestRecordDeduplicatesWithPoller(t *testing.T) {
	b := NewSSEBroker()
	b.observe(kindVM, states())
	b.Record(kindVM, "a", "created", sourceWeb, "")
	b.Record(kindVM, "a", "created", sourceWeb, "")
	b.observe(kindVM, states("a", "created"))
	assertEvents(t, b, "vm/a=created(web)")
}

func TestRecordedDeleteIsNotRedetected(t *testing.T) {
	b := NewSSEBroker()
	b.observe(kindVM, states("a", "stopped"))
	b.Record(kindVM, "a", stateDeleted, sourceAPI, "")
	b.observe(kindVM, states())
	assertEvents(t, b, "vm/a=deleted(api)")
}

func TestHoldAttributesChangeToConsole(t *testing.T) {
	b := NewSSEBroker()
	b.observe(kindVM, states("a", "stopped"))

	release := b.Hold(kindVM, "a")
	b.Record(kindVM, "a", "starting", sourceWeb, "")
	b.observe(kindVM, states("a", "running")) // poller sees the result first
	b.Record(kindVM, "a", "running", sourceWeb, "")
	release()
	release() // releasing twice is harmless

	b.observe(kindVM, states("a", "running"))
	assertEvents(t, b, "vm/a=running(web)", "vm/a=starting(web)")
}

func TestReleasedHoldLetsPollerCatchUp(t *testing.T) {
	b := NewSSEBroker()
	b.observe(kindVM, states("a", "stopped"))
	release := b.Hold(kindVM, "a")
	b.Record(kindVM, "a", "starting", sourceWeb, "")
	release() // start failed without recording a final state

	b.observe(kindVM, states("a", "error"))
	assertEvents(t, b, "vm/a=error(detected)", "vm/a=starting(web)")
}

func TestNoteDoesNotChangeKnownState(t *testing.T) {
	b := NewSSEBroker()
	b.observe(kindVM, states("a", "running"))
	b.Note(kindVM, "a", "snapshot", sourceWeb, "created clean")
	b.observe(kindVM, states("a", "running"))
	assertEvents(t, b, "vm/a=snapshot(web)")
}

func TestHistoryIsCappedAndNewestFirst(t *testing.T) {
	b := NewSSEBroker()
	for i := 0; i < eventHistorySize+10; i++ {
		b.Note(kindVM, fmt.Sprintf("vm-%d", i), "snapshot", sourceWeb, "")
	}
	recent := b.Recent()
	if len(recent) != eventHistorySize {
		t.Fatalf("history length = %d, want %d", len(recent), eventHistorySize)
	}
	if recent[0].Name != fmt.Sprintf("vm-%d", eventHistorySize+9) {
		t.Fatalf("newest event = %s", recent[0].Name)
	}
	if recent[len(recent)-1].Name != "vm-10" {
		t.Fatalf("oldest kept event = %s", recent[len(recent)-1].Name)
	}
}

func TestEventsCarryTimeAndLevel(t *testing.T) {
	b := NewSSEBroker()
	fixed := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return fixed }
	b.Record(kindCluster, "lab", "error", sourceWeb, "provisioning failed")
	e := b.Recent()[0]
	if !e.Time.Equal(fixed) || e.Level != "err" || e.Detail != "provisioning failed" {
		t.Fatalf("event = %+v", e)
	}
}

func TestSubscribersReceiveEvents(t *testing.T) {
	b := NewSSEBroker()
	ch := b.Subscribe()
	b.Record(kindVM, "a", "running", sourceWeb, "")
	select {
	case e := <-ch:
		if e.Name != "a" || e.State != "running" {
			t.Fatalf("got %+v", e)
		}
	default:
		t.Fatal("subscriber did not receive event")
	}
	b.Unsubscribe(ch)
	b.Unsubscribe(ch) // second unsubscribe must not panic on a closed channel
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed after unsubscribe")
	}
}

func TestEventLevel(t *testing.T) {
	cases := map[string]string{
		"running": "ok", "restored": "ok",
		"starting": "warn", "stopping": "warn", "creating": "warn", "deleted": "warn",
		"error": "err", "failed": "err",
		"created": "info", "stopped": "info", "snapshot": "info",
	}
	for state, want := range cases {
		if got := eventLevel(state); got != want {
			t.Errorf("eventLevel(%q) = %q, want %q", state, got, want)
		}
	}
}

func TestRequestSource(t *testing.T) {
	if got := requestSource(httptest.NewRequest("POST", "/api/v1/vms/a/start", nil)); got != sourceAPI {
		t.Errorf("api request source = %q", got)
	}
	if got := requestSource(httptest.NewRequest("POST", "/vms/a/start", nil)); got != sourceWeb {
		t.Errorf("web request source = %q", got)
	}
}
