package web

import (
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/raesene/baremetalvmm/internal/cluster"
	"github.com/raesene/baremetalvmm/internal/config"
	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/vm"
)

// Event kinds and sources for the dashboard activity log.
const (
	kindVM      = "vm"
	kindCluster = "cluster"

	sourceWeb      = "web"      // action taken in the web console
	sourceAPI      = "api"      // action taken through the JSON API
	sourceDetected = "detected" // change noticed by polling (CLI, systemd, crash...)

	stateDeleted = "deleted"

	eventHistorySize = 50
	pollInterval     = 5 * time.Second
)

// Event is one line in the activity log. State is either the object's new
// state or, for one-off notes, the action taken (snapshot, restore, failed).
type Event struct {
	Time   time.Time `json:"time"`
	Kind   string    `json:"kind"`
	Name   string    `json:"name"`
	State  string    `json:"state"`
	Source string    `json:"source"`
	Detail string    `json:"detail,omitempty"`
	Level  string    `json:"level"`
}

// eventLevel maps a state or action to the log severity shown in the UI.
func eventLevel(state string) string {
	switch state {
	case "running", "restored":
		return "ok"
	case "starting", "stopping", "creating", stateDeleted:
		return "warn"
	case "error", "failed":
		return "err"
	default:
		return "info"
	}
}

// requestSource reports whether a request came from the JSON API or the UI.
func requestSource(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		return sourceAPI
	}
	return sourceWeb
}

// SSEBroker records VM and cluster activity, keeps a short in-memory history
// for the dashboard, and fans new events out to connected /events streams.
//
// Changes come from two places: console handlers record what they do as they
// do it, and a poller notices everything else (CLI use, crashes, provisioning).
// Both write to the same last-known-state table so a change is only logged
// once, and objects a handler is working on are held so the poller does not
// claim the change as "detected".
type SSEBroker struct {
	mu          sync.Mutex
	subscribers map[chan Event]struct{}
	history     []Event           // oldest first, capped at eventHistorySize
	states      map[string]string // kind/name -> last known state
	seeded      map[string]bool   // kind -> baseline taken
	held        map[string]int    // kind/name -> active console operations
	now         func() time.Time
}

func NewSSEBroker() *SSEBroker {
	return &SSEBroker{
		subscribers: make(map[chan Event]struct{}),
		states:      make(map[string]string),
		seeded:      make(map[string]bool),
		held:        make(map[string]int),
		now:         time.Now,
	}
}

func eventKey(kind, name string) string { return kind + "/" + name }

func (b *SSEBroker) Subscribe() chan Event {
	ch := make(chan Event, 16)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *SSEBroker) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	if _, ok := b.subscribers[ch]; ok {
		delete(b.subscribers, ch)
		close(ch)
	}
	b.mu.Unlock()
}

// Recent returns the event history, newest first.
func (b *SSEBroker) Recent() []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Event, len(b.history))
	for i, e := range b.history {
		out[len(b.history)-1-i] = e
	}
	return out
}

// publishLocked appends an event to the history and sends it to subscribers.
// Slow subscribers miss live events rather than blocking; the history still
// has them. Callers must hold b.mu.
func (b *SSEBroker) publishLocked(e Event) {
	e.Time = b.now()
	e.Level = eventLevel(e.State)
	b.history = append(b.history, e)
	if over := len(b.history) - eventHistorySize; over > 0 {
		b.history = append([]Event(nil), b.history[over:]...)
	}
	for ch := range b.subscribers {
		select {
		case ch <- e:
		default:
		}
	}
}

// Record logs a state change made by the console. It is ignored if the state
// is already the last one seen, so the poller and handlers never double-log.
func (b *SSEBroker) Record(kind, name, state, source, detail string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := eventKey(kind, name)
	if b.states[key] == state {
		return
	}
	b.states[key] = state
	b.publishLocked(Event{Kind: kind, Name: name, State: state, Source: source, Detail: detail})
}

// Note logs a one-off action (snapshot, restore, failure) without touching
// the last-known state.
func (b *SSEBroker) Note(kind, name, action, source, detail string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.publishLocked(Event{Kind: kind, Name: name, State: action, Source: source, Detail: detail})
}

// Hold stops the poller reporting changes to an object while a console action
// runs, so the outcome is attributed to the console. Call the returned func
// when the action finishes.
func (b *SSEBroker) Hold(kind, name string) func() {
	key := eventKey(kind, name)
	b.mu.Lock()
	b.held[key]++
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			if b.held[key]--; b.held[key] <= 0 {
				delete(b.held, key)
			}
			b.mu.Unlock()
		})
	}
}

// observe compares a polled snapshot of one kind's states with the last known
// states and logs the differences as detected changes. The first snapshot of
// each kind only sets the baseline. A nil snapshot (poll error) is ignored.
func (b *SSEBroker) observe(kind string, current map[string]string) {
	if current == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.seeded[kind] {
		for name, state := range current {
			b.states[eventKey(kind, name)] = state
		}
		b.seeded[kind] = true
		return
	}

	for name, state := range current {
		key := eventKey(kind, name)
		if b.held[key] > 0 || b.states[key] == state {
			continue
		}
		b.states[key] = state
		b.publishLocked(Event{Kind: kind, Name: name, State: state, Source: sourceDetected})
	}

	prefix := kind + "/"
	for key, last := range b.states {
		if !strings.HasPrefix(key, prefix) || b.held[key] > 0 {
			continue
		}
		name := strings.TrimPrefix(key, prefix)
		if _, ok := current[name]; ok {
			continue
		}
		delete(b.states, key)
		if last != stateDeleted {
			b.publishLocked(Event{Kind: kind, Name: name, State: stateDeleted, Source: sourceDetected})
		}
	}
}

// Start polls VM and cluster state until the process exits.
func (b *SSEBroker) Start(cfg *config.Config) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		b.observe(kindVM, pollVMStates(cfg))
		b.observe(kindCluster, pollClusterStates(cfg))
		<-ticker.C
	}
}

func pollVMStates(cfg *config.Config) map[string]string {
	vms, err := vm.List(cfg.GetPaths().VMs)
	if err != nil {
		log.Printf("activity poll: listing VMs: %v", err)
		return nil
	}
	fcClient := firecracker.NewClient()
	states := make(map[string]string, len(vms))
	for _, v := range vms {
		fcClient.UpdateVMState(v)
		states[v.Name] = string(v.State)
	}
	return states
}

func pollClusterStates(cfg *config.Config) map[string]string {
	clusters, err := cluster.List(cfg.GetPaths().Clusters)
	if err != nil {
		log.Printf("activity poll: listing clusters: %v", err)
		return nil
	}
	states := make(map[string]string, len(clusters))
	for _, cl := range clusters {
		states[cl.Name] = string(cl.State)
	}
	return states
}
