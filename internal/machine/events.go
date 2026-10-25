package machine

import (
	"log"
	"sync"
	"time"
)

// EventType is the kind of a machine Event.
type EventType string

const (
	// EventPowerState reports a change of GetPowerState (including the
	// PoweringOn/PoweringOff states and a guest or QEMU going down by
	// itself).
	EventPowerState EventType = "PowerState"
	// EventResourceChanged reports a change of a machine setting (Resource).
	EventResourceChanged EventType = "ResourceChanged"
)

// Resource is the machine setting an EventResourceChanged is about.
type Resource string

const (
	ResourceBoot         Resource = "Boot" // boot override or HTTP boot URI
	ResourceVirtualMedia Resource = "VirtualMedia"
	ResourceSecureBoot   Resource = "SecureBoot"
)

// Event is a change of the machine state, sent to the SubscribeEvents
// subscribers.
type Event struct {
	Type       EventType
	PowerState PowerState // EventPowerState: the new state
	Resource   Resource   // EventResourceChanged
}

const (
	// eventBuffer is the channel size of a subscriber; events are dropped
	// (and logged) when it is full.
	eventBuffer = 64
	// defaultPowerPoll is how often the power state is polled while there
	// are subscribers, to catch changes nothing tells the machine about (a
	// guest powering off, QEMU exiting, a graceful timeout).
	defaultPowerPoll = time.Second
)

// eventBus sends events to a list of subscribers and, while there are any,
// watches the power state.
type eventBus struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
	// stop ends the power watcher; nil while there are no subscribers.
	stop chan struct{}

	// powerMu serializes power state checks so that changes are published
	// in order. last is the last state published.
	powerMu sync.Mutex
	last    PowerState
}

// SubscribeEvents returns a channel receiving the machine events and a
// function ending the subscription. Events are dropped for a subscriber not
// keeping up.
func (m *Machine) SubscribeEvents() (<-chan Event, func()) {
	b := &m.events
	ch := make(chan Event, eventBuffer)
	// Lock order: powerMu, then mu (as in checkPower)
	b.powerMu.Lock()
	b.mu.Lock()
	if b.subs == nil {
		b.subs = make(map[chan Event]struct{})
	}
	b.subs[ch] = struct{}{}
	if b.stop == nil {
		b.stop = make(chan struct{})
		// The current state is the baseline, not a change
		if state, err := m.GetPowerState(); err == nil {
			b.last = state
		}
		go m.watchPower(b.stop)
	}
	b.mu.Unlock()
	b.powerMu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			delete(b.subs, ch)
			if len(b.subs) == 0 && b.stop != nil {
				close(b.stop)
				b.stop = nil
			}
		})
	}
}

// publish sends e to every subscriber without blocking.
func (m *Machine) publish(e Event) {
	b := &m.events
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
			log.Printf("events: subscriber not keeping up, %s event dropped", e.Type)
		}
	}
}

// publishChange sends an EventResourceChanged for r.
func (m *Machine) publishChange(r Resource) {
	m.publish(Event{Type: EventResourceChanged, Resource: r})
}

// subscribed reports whether anyone receives events.
func (m *Machine) subscribed() bool {
	m.events.mu.Lock()
	defer m.events.mu.Unlock()
	return len(m.events.subs) > 0
}

// checkPower publishes an EventPowerState if the power state changed since
// the last check. It is called right after the machine changes the state
// (so that short states such as Off during a PowerCycle are seen) and
// periodically by the power watcher. Callers must not hold m.mu.
func (m *Machine) checkPower() {
	if !m.subscribed() {
		return
	}
	b := &m.events
	b.powerMu.Lock()
	defer b.powerMu.Unlock()
	state, err := m.GetPowerState()
	if err != nil || state == b.last {
		return
	}
	b.last = state
	m.publish(Event{Type: EventPowerState, PowerState: state})
}

// watchPower polls the power state until stop is closed.
func (m *Machine) watchPower(stop chan struct{}) {
	interval := m.powerPoll
	if interval <= 0 {
		interval = defaultPowerPoll
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			m.checkPower()
		}
	}
}
