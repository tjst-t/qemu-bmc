package redfish

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tjst-t/qemu-bmc/internal/machine"
)

const (
	eventServicePath  = "/redfish/v1/EventService"
	subscriptionsPath = eventServicePath + "/Subscriptions"
	ssePath           = eventServicePath + "/SSE"

	// maxSubscriptions bounds the number of event subscriptions; creating
	// one more fails with 503 until one is deleted.
	maxSubscriptions = 32

	defaultDeliveryRetryAttempts        = 3
	defaultDeliveryRetryIntervalSeconds = 5
	defaultDeliveryRetryPolicy          = "SuspendRetries"

	// subscriptionQueue is how many events wait for delivery to one
	// destination (e.g. while it is retried) before new ones are dropped.
	subscriptionQueue = 256
	// sseQueue is the same for a Server-Sent Events connection.
	sseQueue = 64
	// deliveryTimeout bounds one delivery attempt.
	deliveryTimeout = 10 * time.Second
	// maxRetryBackoff caps the retry interval of RetryForeverWithBackoff.
	maxRetryBackoff = 10 * time.Minute

	// resourceEventRegistry is the DMTF ResourceEvent registry version the
	// MessageIds refer to (1.1 added the power state messages).
	resourceEventRegistry = "ResourceEvent.1.3"
)

// Redfish EventType values (deprecated in favour of RegistryPrefixes and
// ResourceTypes, still accepted in subscriptions as the old form)
var eventTypes = []string{"StatusChange", "ResourceUpdated", "ResourceAdded", "ResourceRemoved", "Alert", "Other"}

// The registries and resource types the events come from
var (
	eventRegistryPrefixes = []string{"ResourceEvent", "Base", "TaskEvent"}
	eventResourceTypes    = []string{"ComputerSystem", "VirtualMedia", "SecureBoot", "Session", "SessionService", "EventService",
		"Task", "SoftwareInventory"}
)

var deliveryRetryPolicies = []string{"TerminateAfterRetries", "SuspendRetries", "RetryForever", "RetryForeverWithBackoff"}

// resourceEvents are the ResourceEvent messages qemu-bmc sends.
var resourceEvents = map[string]struct {
	eventType string
	message   string // %1 = MessageArgs[0]
}{
	"ResourcePoweredOn":   {"StatusChange", "The resource '%1' has powered on."},
	"ResourcePoweredOff":  {"StatusChange", "The resource '%1' has powered off."},
	"ResourcePoweringOn":  {"StatusChange", "The resource '%1' is powering on."},
	"ResourcePoweringOff": {"StatusChange", "The resource '%1' is powering off."},
	"ResourceChanged":     {"ResourceUpdated", "One or more resource properties have changed."},
	"ResourceCreated":     {"ResourceAdded", "The resource was created successfully."},
	"ResourceRemoved":     {"ResourceRemoved", "The resource was removed successfully."},
}

// powerMessages maps a power state to its ResourceEvent message.
var powerMessages = map[machine.PowerState]string{
	machine.PowerOn:     "ResourcePoweredOn",
	machine.PowerOff:    "ResourcePoweredOff",
	machine.PoweringOn:  "ResourcePoweringOn",
	machine.PoweringOff: "ResourcePoweringOff",
}

// machineResources maps a machine setting to the resource it is shown in.
var machineResources = map[machine.Resource]string{
	machine.ResourceBoot:         systemPath,
	machine.ResourceVirtualMedia: systemVirtualMediaPath + "/CD1",
	machine.ResourceSecureBoot:   systemPath + "/SecureBoot",
}

// EventRecord is a Redfish EventRecord (an entry of Event.Events).
type EventRecord struct {
	EventType         string   `json:"EventType"`
	EventID           string   `json:"EventId"`
	EventTimestamp    string   `json:"EventTimestamp"`
	EventGroupID      *int     `json:"EventGroupId,omitempty"`
	MemberID          string   `json:"MemberId"`
	Message           string   `json:"Message,omitempty"`
	MessageArgs       []string `json:"MessageArgs"`
	MessageID         string   `json:"MessageId"`
	MessageSeverity   string   `json:"MessageSeverity,omitempty"`
	OriginOfCondition *ODataID `json:"OriginOfCondition,omitempty"`
}

// Event is the Redfish Event resource sent to a destination or SSE client.
type Event struct {
	ODataType   string        `json:"@odata.type"`
	ID          string        `json:"Id"`
	Name        string        `json:"Name"`
	Context     string        `json:"Context,omitempty"`
	Events      []EventRecord `json:"Events"`
	EventsCount int           `json:"Events@odata.count"`
}

// event is a published event: its record and the resource type of its
// OriginOfCondition (for ResourceTypes filters).
type event struct {
	record       EventRecord
	resourceType string
}

func (ev event) origin() string {
	if ev.record.OriginOfCondition == nil {
		return ""
	}
	return ev.record.OriginOfCondition.ODataID
}

// payload returns the Event sent for ev, with the subscription's context.
func (ev event) payload(context string) Event {
	return Event{
		ODataType:   "#Event.v1_7_0.Event",
		ID:          ev.record.EventID,
		Name:        "Event",
		Context:     context,
		Events:      []EventRecord{ev.record},
		EventsCount: 1,
	}
}

// subscription is an EventDestination: events are POSTed to destination
// in order by its own goroutine (run).
type subscription struct {
	id               string
	destination      string
	context          string
	eventTypes       []string
	registryPrefixes []string
	resourceTypes    []string
	messageIDs       []string
	originResources  []string
	subordinate      bool
	headers          [][2]string // name, value
	// context, policy and suspended are guarded by eventService.mu
	policy    string
	suspended bool

	queue  chan event
	ctx    context.Context // done once deleted
	cancel context.CancelFunc
}

// matches reports whether ev passes the subscription's filters. Empty
// filters match everything.
func (sub *subscription) matches(ev event) bool {
	r := ev.record
	if len(sub.eventTypes) > 0 && !contains(sub.eventTypes, r.EventType) {
		return false
	}
	if len(sub.registryPrefixes) > 0 && !contains(sub.registryPrefixes, registryPrefix(r.MessageID)) {
		return false
	}
	if len(sub.resourceTypes) > 0 && !contains(sub.resourceTypes, ev.resourceType) {
		return false
	}
	if len(sub.messageIDs) > 0 && !anyMatch(sub.messageIDs, func(id string) bool { return messageIDMatches(id, r.MessageID) }) {
		return false
	}
	if len(sub.originResources) > 0 && !anyMatch(sub.originResources, func(o string) bool {
		return originMatches(o, ev.origin(), sub.subordinate)
	}) {
		return false
	}
	return true
}

// sseClient is a connection to the Server-Sent Events stream.
type sseClient struct {
	ch     chan event
	filter func(event) bool
}

// eventService holds the event subscriptions (in memory only) and SSE
// connections, and delivers the published events to them.
type eventService struct {
	mu            sync.Mutex
	enabled       bool
	retryAttempts int
	retryInterval int // seconds
	// retryUnit is the unit of retryInterval (shorter in tests)
	retryUnit   time.Duration
	subs        map[string]*subscription
	nextSubID   int
	nextEventID uint64
	sse         map[*sseClient]struct{}
	client      *http.Client
	insecureTLS bool
	now         func() time.Time
}

func newEventService() *eventService {
	es := &eventService{
		enabled:       true,
		retryAttempts: defaultDeliveryRetryAttempts,
		retryInterval: defaultDeliveryRetryIntervalSeconds,
		retryUnit:     time.Second,
		subs:          make(map[string]*subscription),
		sse:           make(map[*sseClient]struct{}),
		now:           time.Now,
	}
	es.setInsecureTLS(true)
	return es
}

// setInsecureTLS sets whether https destinations' certificates are not
// verified.
func (es *eventService) setInsecureTLS(insecure bool) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: insecure} //nolint:gosec // EVENT_INSECURE_TLS
	es.mu.Lock()
	defer es.mu.Unlock()
	es.insecureTLS = insecure
	es.client = &http.Client{Transport: transport, Timeout: deliveryTimeout}
}

// publish sends ev to the matching subscriptions and SSE connections,
// unless the service is disabled. An empty EventId or EventTimestamp is
// filled in.
func (es *eventService) publish(ev event) {
	es.mu.Lock()
	defer es.mu.Unlock()
	if !es.enabled {
		return
	}
	es.nextEventID++
	if ev.record.EventID == "" {
		ev.record.EventID = strconv.FormatUint(es.nextEventID, 10)
	}
	if ev.record.EventTimestamp == "" {
		ev.record.EventTimestamp = es.now().UTC().Format(time.RFC3339)
	}
	if ev.record.MessageArgs == nil {
		ev.record.MessageArgs = []string{}
	}
	ev.record.MemberID = "0"

	for _, id := range es.sortedIDsLocked() {
		sub := es.subs[id]
		if sub.suspended || !sub.matches(ev) {
			continue
		}
		select {
		case sub.queue <- ev:
		default:
			log.Printf("events: subscription %s: queue full, event %s dropped", sub.id, ev.record.EventID)
		}
	}
	for c := range es.sse {
		if !c.filter(ev) {
			continue
		}
		select {
		case c.ch <- ev:
		default:
			log.Printf("events: SSE client not keeping up, event %s dropped", ev.record.EventID)
		}
	}
}

var errTooManySubscriptions = errors.New("too many subscriptions")

// add creates a subscription from sub's settings and starts delivering to
// it.
func (es *eventService) add(sub *subscription) error {
	es.mu.Lock()
	defer es.mu.Unlock()
	if len(es.subs) >= maxSubscriptions {
		return errTooManySubscriptions
	}
	es.nextSubID++
	sub.id = strconv.Itoa(es.nextSubID)
	sub.queue = make(chan event, subscriptionQueue)
	sub.ctx, sub.cancel = context.WithCancel(context.Background())
	es.subs[sub.id] = sub
	go es.run(sub)
	return nil
}

func (es *eventService) remove(id string) bool {
	es.mu.Lock()
	defer es.mu.Unlock()
	sub, ok := es.subs[id]
	if !ok {
		return false
	}
	sub.cancel()
	delete(es.subs, id)
	return true
}

// get returns a copy of subscription id.
func (es *eventService) get(id string) (subscription, bool) {
	es.mu.Lock()
	defer es.mu.Unlock()
	sub, ok := es.subs[id]
	if !ok {
		return subscription{}, false
	}
	return *sub, true
}

// update changes a subscription under the lock; it reports false if the
// subscription does not exist.
func (es *eventService) update(id string, f func(sub *subscription)) bool {
	es.mu.Lock()
	defer es.mu.Unlock()
	sub, ok := es.subs[id]
	if ok {
		f(sub)
	}
	return ok
}

func (es *eventService) ids() []string {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.sortedIDsLocked()
}

func (es *eventService) sortedIDsLocked() []string {
	ids := make([]string, 0, len(es.subs))
	for id := range es.subs {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, _ := strconv.Atoi(ids[i])
		b, _ := strconv.Atoi(ids[j])
		return a < b
	})
	return ids
}

// addSSE registers an SSE connection; the returned function removes it.
func (es *eventService) addSSE(filter func(event) bool) (*sseClient, func()) {
	c := &sseClient{ch: make(chan event, sseQueue), filter: filter}
	es.mu.Lock()
	es.sse[c] = struct{}{}
	es.mu.Unlock()
	return c, func() {
		es.mu.Lock()
		delete(es.sse, c)
		es.mu.Unlock()
	}
}

// run delivers the events queued for sub, one at a time, until it is
// deleted.
func (es *eventService) run(sub *subscription) {
	for {
		select {
		case <-sub.ctx.Done():
			return
		case ev := <-sub.queue:
			es.mu.Lock()
			suspended := sub.suspended
			es.mu.Unlock()
			if suspended {
				continue // queued before the subscription was suspended
			}
			es.deliver(sub, ev)
		}
	}
}

// deliver POSTs ev to sub's destination, retrying as the delivery retry
// settings and sub's DeliveryRetryPolicy say. When the retries are used up
// the event is dropped and the subscription is suspended
// (SuspendRetries) or deleted (TerminateAfterRetries); RetryForever and
// RetryForeverWithBackoff retry until delivered or deleted.
func (es *eventService) deliver(sub *subscription, ev event) {
	es.mu.Lock()
	subContext := sub.context
	es.mu.Unlock()
	body, err := json.Marshal(ev.payload(subContext))
	if err != nil {
		log.Printf("events: encoding event %s: %v", ev.record.EventID, err)
		return
	}
	for attempt := 1; ; attempt++ {
		es.mu.Lock()
		client := es.client
		es.mu.Unlock()
		err := sub.post(client, body)
		if err == nil {
			return
		}
		if sub.ctx.Err() != nil {
			return // deleted
		}

		es.mu.Lock()
		policy, retries := sub.policy, es.retryAttempts
		interval := time.Duration(es.retryInterval) * es.retryUnit
		es.mu.Unlock()
		log.Printf("events: subscription %s: delivering event %s to %s failed (attempt %d): %v",
			sub.id, ev.record.EventID, sub.destination, attempt, err)

		switch policy {
		case "RetryForever":
		case "RetryForeverWithBackoff":
			for i := 1; i < attempt && interval < maxRetryBackoff; i++ {
				interval *= 2
			}
			interval = min(interval, maxRetryBackoff)
		default:
			if attempt > retries {
				es.retriesExhausted(sub, policy, ev)
				return
			}
		}
		select {
		case <-sub.ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// retriesExhausted ends the delivery of ev to sub after the last retry.
func (es *eventService) retriesExhausted(sub *subscription, policy string, ev event) {
	if policy == "TerminateAfterRetries" {
		log.Printf("events: subscription %s: event %s not delivered, deleting the subscription (TerminateAfterRetries)",
			sub.id, ev.record.EventID)
		es.remove(sub.id)
		return
	}
	log.Printf("events: subscription %s: event %s not delivered, suspending the subscription (SuspendRetries)",
		sub.id, ev.record.EventID)
	es.mu.Lock()
	sub.suspended = true
	es.mu.Unlock()
}

// post sends one delivery attempt; any 2xx response is a success.
func (sub *subscription) post(client *http.Client, body []byte) error {
	req, err := http.NewRequestWithContext(sub.ctx, http.MethodPost, sub.destination, bytes.NewReader(body))
	if err != nil {
		return err
	}
	for _, h := range sub.headers {
		req.Header.Set(h[0], h[1])
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// resourceEvent returns a ResourceEvent registry event about origin.
func resourceEvent(key, origin string, args ...string) event {
	msg := resourceEvents[key]
	message := msg.message
	for i, a := range args {
		message = strings.ReplaceAll(message, "%"+strconv.Itoa(i+1), a)
	}
	if args == nil {
		args = []string{}
	}
	return event{
		record: EventRecord{
			EventType:         msg.eventType,
			Message:           message,
			MessageArgs:       args,
			MessageID:         resourceEventRegistry + "." + key,
			MessageSeverity:   "OK",
			OriginOfCondition: &ODataID{ODataID: origin},
		},
		resourceType: resourceTypeOf(origin),
	}
}

// machineEvent returns the Redfish event of a machine event, false if there
// is none.
func machineEvent(e machine.Event) (event, bool) {
	switch e.Type {
	case machine.EventPowerState:
		key, ok := powerMessages[e.PowerState]
		if !ok {
			return event{}, false
		}
		return resourceEvent(key, systemPath, systemPath), true
	case machine.EventResourceChanged:
		origin, ok := machineResources[e.Resource]
		if !ok {
			return event{}, false
		}
		return resourceEvent("ResourceChanged", origin), true
	}
	return event{}, false
}

// resourceTypeOf returns the Redfish schema name of the resource at uri, ""
// if unknown.
func resourceTypeOf(uri string) string {
	segs := strings.Split(strings.Trim(strings.TrimPrefix(uri, "/redfish/v1"), "/"), "/")
	n := len(segs)
	switch {
	case n == 2 && segs[0] == "Systems":
		return "ComputerSystem"
	case n == 2 && segs[0] == "Managers":
		return "Manager"
	case n == 2 && segs[0] == "Chassis":
		return "Chassis"
	case n == 4 && segs[2] == "VirtualMedia":
		return "VirtualMedia"
	case n == 3 && segs[0] == "Systems" && segs[2] == "SecureBoot":
		return "SecureBoot"
	case n == 1 && segs[0] == "SessionService":
		return "SessionService"
	case n == 3 && segs[0] == "SessionService" && segs[1] == "Sessions":
		return "Session"
	case n == 1 && segs[0] == "EventService":
		return "EventService"
	case n == 3 && segs[0] == "EventService" && segs[1] == "Subscriptions":
		return "EventDestination"
	case n == 3 && segs[0] == "TaskService" && segs[1] == "Tasks":
		return "Task"
	case n == 3 && segs[0] == "UpdateService" && segs[1] == "FirmwareInventory":
		return "SoftwareInventory"
	}
	return ""
}

// registryPrefix returns the registry of a MessageId ("ResourceEvent" of
// "ResourceEvent.1.3.ResourceChanged").
func registryPrefix(messageID string) string {
	prefix, _, _ := strings.Cut(messageID, ".")
	return prefix
}

// messageIDMatches reports whether a MessageId filter matches id: exactly,
// or as Registry.Key without the version.
func messageIDMatches(filter, id string) bool {
	if filter == id {
		return true
	}
	f := strings.Split(filter, ".")
	parts := strings.Split(id, ".")
	return len(f) == 2 && len(parts) >= 2 && f[0] == parts[0] && f[1] == parts[len(parts)-1]
}

// originMatches reports whether origin is resource (or, with subordinate,
// below it).
func originMatches(resource, origin string, subordinate bool) bool {
	resource = strings.TrimSuffix(resource, "/")
	return origin == resource || (subordinate && strings.HasPrefix(origin, resource+"/"))
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func anyMatch(list []string, f func(string) bool) bool {
	for _, v := range list {
		if f(v) {
			return true
		}
	}
	return false
}

var (
	filterOr   = regexp.MustCompile(`(?i)\s+or\s+`)
	filterAnd  = regexp.MustCompile(`(?i)\s+and\s+`)
	filterTerm = regexp.MustCompile(`(?i)^(\w+)\s+eq\s+(?:'([^']*)'|(\S+))$`)
)

// parseSSEFilter parses the simple $filter forms of the SSE stream: terms
// "<Property> eq '<value>'" on EventType, MessageId, OriginResource,
// RegistryPrefix or ResourceType, joined by "and" / "or" ("and" binding
// tighter; no parentheses).
func parseSSEFilter(filter string) (func(event) bool, error) {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return func(event) bool { return true }, nil
	}
	type term struct{ prop, value string }
	var ors [][]term
	for _, orPart := range filterOr.Split(filter, -1) {
		var ands []term
		for _, andPart := range filterAnd.Split(strings.TrimSpace(orPart), -1) {
			m := filterTerm.FindStringSubmatch(strings.TrimSpace(andPart))
			if m == nil {
				return nil, fmt.Errorf("unsupported $filter term %q: use <Property> eq '<value>'", andPart)
			}
			value := m[2]
			if m[3] != "" {
				value = m[3]
			}
			switch m[1] {
			case "EventType", "MessageId", "OriginResource", "RegistryPrefix", "ResourceType":
			default:
				return nil, fmt.Errorf("unsupported $filter property %q", m[1])
			}
			ands = append(ands, term{m[1], value})
		}
		ors = append(ors, ands)
	}
	matchTerm := func(t term, ev event) bool {
		switch t.prop {
		case "EventType":
			return ev.record.EventType == t.value
		case "MessageId":
			return messageIDMatches(t.value, ev.record.MessageID)
		case "OriginResource":
			return originMatches(t.value, ev.origin(), false)
		case "RegistryPrefix":
			return registryPrefix(ev.record.MessageID) == t.value
		case "ResourceType":
			return ev.resourceType == t.value
		}
		return false
	}
	return func(ev event) bool {
		for _, ands := range ors {
			ok := true
			for _, t := range ands {
				if !matchTerm(t, ev) {
					ok = false
					break
				}
			}
			if ok {
				return true
			}
		}
		return false
	}, nil
}
