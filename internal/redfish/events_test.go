package redfish

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/machine"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

// fakeEventSource is an EventSource sending what the test puts in ch.
type fakeEventSource struct{ ch chan machine.Event }

func (f *fakeEventSource) SubscribeEvents() (<-chan machine.Event, func()) {
	return f.ch, func() {}
}

// receivedEvent is an event POSTed to an eventReceiver.
type receivedEvent struct {
	header http.Header
	event  Event
}

// eventReceiver is an event destination. Requests fail with 500 while
// fail returns true (it gets the request number, from 1).
type eventReceiver struct {
	*httptest.Server
	mu       sync.Mutex
	requests int
	fail     func(n int) bool
	events   chan receivedEvent
}

func newEventReceiver(t *testing.T) *eventReceiver {
	rc := &eventReceiver{events: make(chan receivedEvent, 100)}
	rc.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc.mu.Lock()
		rc.requests++
		n, fail := rc.requests, rc.fail
		rc.mu.Unlock()
		if fail != nil && fail(n) {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var ev Event
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &ev); err != nil {
			t.Errorf("event body %q: %v", body, err)
		}
		rc.events <- receivedEvent{header: r.Header.Clone(), event: ev}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(rc.Close)
	return rc
}

func (rc *eventReceiver) setFail(f func(n int) bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.fail = f
}

func (rc *eventReceiver) requestCount() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.requests
}

// next returns the next event received, failing the test after 2 s.
func (rc *eventReceiver) next(t *testing.T) receivedEvent {
	t.Helper()
	select {
	case e := <-rc.events:
		return e
	case <-time.After(2 * time.Second):
		t.Fatal("no event received")
		return receivedEvent{}
	}
}

// none checks that no event arrives within 200 ms.
func (rc *eventReceiver) none(t *testing.T) {
	t.Helper()
	select {
	case e := <-rc.events:
		t.Fatalf("unexpected event: %+v", e.event)
	case <-time.After(200 * time.Millisecond):
	}
}

// newEventServer returns a server without auth whose machine events come
// from the returned channel; retries are 10 ms apart.
func newEventServer(t *testing.T) (*Server, chan machine.Event) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	srv.events.retryUnit = 10 * time.Millisecond
	src := &fakeEventSource{ch: make(chan machine.Event, 10)}
	srv.SetEventSource(src)
	t.Cleanup(func() { close(src.ch) })
	return srv, src.ch
}

// subscribe creates a subscription and returns its URI.
func subscribe(t *testing.T, srv *Server, body string) string {
	t.Helper()
	w := doReq(srv, "POST", subscriptionsPath, body, nil)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	loc := w.Header().Get("Location")
	require.NotEmpty(t, loc)
	return loc
}

func decodeBody(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m), w.Body.String())
	return m
}

func TestEventService_Get(t *testing.T) {
	srv, _ := newEventServer(t)

	root := decodeBody(t, doReq(srv, "GET", "/redfish/v1", "", nil))
	assert.Equal(t, map[string]interface{}{"@odata.id": eventServicePath}, root["EventService"])

	w := doReq(srv, "GET", eventServicePath+"/", "", nil)
	require.Equal(t, http.StatusOK, w.Code)
	es := decodeBody(t, w)
	assert.Equal(t, "#EventService.v1_10_0.EventService", es["@odata.type"])
	assert.Equal(t, eventServicePath, es["@odata.id"])
	assert.Equal(t, true, es["ServiceEnabled"])
	assert.Equal(t, 3.0, es["DeliveryRetryAttempts"])
	assert.Equal(t, 5.0, es["DeliveryRetryIntervalSeconds"])
	assert.Equal(t, []interface{}{"Event"}, es["EventFormatTypes"])
	assert.Equal(t, []interface{}{"ResourceEvent", "Base"}, es["RegistryPrefixes"])
	assert.Contains(t, es["ResourceTypes"], "ComputerSystem")
	assert.Equal(t, ssePath, es["ServerSentEventUri"])
	assert.Equal(t, map[string]interface{}{"@odata.id": subscriptionsPath}, es["Subscriptions"])
	assert.Equal(t, map[string]interface{}{"#EventService.SubmitTestEvent": map[string]interface{}{
		"target": eventServicePath + "/Actions/EventService.SubmitTestEvent"}}, es["Actions"])
	assert.Equal(t, w.Header().Get("ETag"), es["@odata.etag"])
}

func TestEventService_Patch(t *testing.T) {
	srv, _ := newEventServer(t)

	w := doReq(srv, "PATCH", eventServicePath, `{"DeliveryRetryAttempts":1,"DeliveryRetryIntervalSeconds":2}`, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	es := decodeBody(t, w)
	assert.Equal(t, 1.0, es["DeliveryRetryAttempts"])
	assert.Equal(t, 2.0, es["DeliveryRetryIntervalSeconds"])

	for _, body := range []string{
		`{"DeliveryRetryAttempts":-1}`,
		`{"DeliveryRetryIntervalSeconds":0}`,
		`{"ServiceEnabled":"yes"}`,
		`{"ServiceEnabled":null}`,
		`{"EventFormatTypes":["Event"]}`,
		`{}`,
	} {
		assert.Equal(t, http.StatusBadRequest, doReq(srv, "PATCH", eventServicePath, body, nil).Code, body)
	}
	w = doReq(srv, "PATCH", eventServicePath, `{"ServiceEnabled":false}`, map[string]string{"If-Match": `"stale"`})
	assert.Equal(t, http.StatusPreconditionFailed, w.Code)
}

func TestEventService_Disabled_NoEvents(t *testing.T) {
	srv, machineEvents := newEventServer(t)
	rc := newEventReceiver(t)
	subscribe(t, srv, `{"Destination":"`+rc.URL+`","Protocol":"Redfish"}`)

	w := doReq(srv, "PATCH", eventServicePath, `{"ServiceEnabled":false}`, nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "Disabled", decodeBody(t, w)["Status"].(map[string]interface{})["State"])
	machineEvents <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PowerOff}
	rc.none(t)
	w = doReq(srv, "POST", eventServicePath+"/Actions/EventService.SubmitTestEvent", `{"MessageId":"Base.1.0.Success"}`, nil)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)

	require.Equal(t, http.StatusOK, doReq(srv, "PATCH", eventServicePath, `{"ServiceEnabled":true}`, nil).Code)
	// The PATCH itself is a ResourceChanged of the EventService
	assert.Equal(t, "ResourceEvent.1.3.ResourceChanged", rc.next(t).event.Events[0].MessageID)
}

func TestSubscription_CRUD(t *testing.T) {
	srv, _ := newEventServer(t)

	loc := subscribe(t, srv, `{"Destination":"https://receiver.example/events","Protocol":"Redfish",
		"Context":"ctx-1","RegistryPrefixes":["ResourceEvent"],"ResourceTypes":["ComputerSystem"],
		"OriginResources":[{"@odata.id":"/redfish/v1/Systems/1"}],
		"HttpHeaders":[{"X-Token":"secret"}]}`)
	assert.Equal(t, subscriptionsPath+"/1", loc)

	w := doReq(srv, "GET", loc+"/", "", nil)
	require.Equal(t, http.StatusOK, w.Code)
	sub := decodeBody(t, w)
	assert.Equal(t, "#EventDestination.v1_13_0.EventDestination", sub["@odata.type"])
	assert.Equal(t, loc, sub["@odata.id"])
	assert.Equal(t, "1", sub["Id"])
	assert.Equal(t, "https://receiver.example/events", sub["Destination"])
	assert.Equal(t, "Redfish", sub["Protocol"])
	assert.Equal(t, "ctx-1", sub["Context"])
	assert.Equal(t, "RedfishEvent", sub["SubscriptionType"])
	assert.Equal(t, []interface{}{"ResourceEvent"}, sub["RegistryPrefixes"])
	assert.Equal(t, []interface{}{"ComputerSystem"}, sub["ResourceTypes"])
	assert.Equal(t, []interface{}{map[string]interface{}{"@odata.id": "/redfish/v1/Systems/1"}}, sub["OriginResources"])
	assert.Equal(t, "SuspendRetries", sub["DeliveryRetryPolicy"])
	assert.Equal(t, map[string]interface{}{"State": "Enabled", "Health": "OK"}, sub["Status"])
	// The headers are write-only
	assert.Equal(t, []interface{}{}, sub["HttpHeaders"])
	assert.NotContains(t, w.Body.String(), "secret")
	assert.NotContains(t, sub, "EventTypes")

	list := decodeBody(t, doReq(srv, "GET", subscriptionsPath, "", nil))
	assert.Equal(t, 1.0, list["Members@odata.count"])
	assert.Equal(t, []interface{}{map[string]interface{}{"@odata.id": loc}}, list["Members"])

	w = doReq(srv, "PATCH", loc, `{"Context":"ctx-2","DeliveryRetryPolicy":"TerminateAfterRetries"}`, nil)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	sub = decodeBody(t, w)
	assert.Equal(t, "ctx-2", sub["Context"])
	assert.Equal(t, "TerminateAfterRetries", sub["DeliveryRetryPolicy"])
	assert.Equal(t, http.StatusBadRequest, doReq(srv, "PATCH", loc, `{"Destination":"http://x/"}`, nil).Code)
	assert.Equal(t, http.StatusBadRequest, doReq(srv, "PATCH", loc, `{"DeliveryRetryPolicy":"Sometimes"}`, nil).Code)
	assert.Equal(t, http.StatusPreconditionFailed,
		doReq(srv, "PATCH", loc, `{"Context":"x"}`, map[string]string{"If-Match": `"stale"`}).Code)

	assert.Equal(t, http.StatusNoContent, doReq(srv, "DELETE", loc, "", nil).Code)
	assert.Equal(t, http.StatusNotFound, doReq(srv, "GET", loc, "", nil).Code)
	assert.Equal(t, http.StatusNotFound, doReq(srv, "DELETE", loc, "", nil).Code)
	assert.Equal(t, 0.0, decodeBody(t, doReq(srv, "GET", subscriptionsPath, "", nil))["Members@odata.count"])
}

func TestSubscription_CreateInvalid(t *testing.T) {
	srv, _ := newEventServer(t)
	for _, body := range []string{
		`{"Protocol":"Redfish"}`,
		`{"Destination":"http://x/"}`,
		`{"Destination":"ftp://x/","Protocol":"Redfish"}`,
		`{"Destination":"/events","Protocol":"Redfish"}`,
		`{"Destination":"http://x/","Protocol":"SNMPv2c"}`,
		`{"Destination":"http://x/","Protocol":"Redfish","SubscriptionType":"SSE"}`,
		`{"Destination":"http://x/","Protocol":"Redfish","EventTypes":["Nope"]}`,
		`{"Destination":"http://x/","Protocol":"Redfish","RegistryPrefixes":["Nope"]}`,
		`{"Destination":"http://x/","Protocol":"Redfish","ResourceTypes":["Nope"]}`,
		`{"Destination":"http://x/","Protocol":"Redfish","OriginResources":[{"@odata.id":"/x"}]}`,
		`{"Destination":"http://x/","Protocol":"Redfish","DeliveryRetryPolicy":"Sometimes"}`,
		`not json`,
	} {
		assert.Equal(t, http.StatusBadRequest, doReq(srv, "POST", subscriptionsPath, body, nil).Code, body)
	}
}

func TestSubscription_Limit(t *testing.T) {
	srv, _ := newEventServer(t)
	for i := 0; i < maxSubscriptions; i++ {
		subscribe(t, srv, `{"Destination":"http://x/","Protocol":"Redfish"}`)
	}
	w := doReq(srv, "POST", subscriptionsPath, `{"Destination":"http://x/","Protocol":"Redfish"}`, nil)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Equal(t, http.StatusNoContent, doReq(srv, "DELETE", subscriptionsPath+"/1", "", nil).Code)
	subscribe(t, srv, `{"Destination":"http://x/","Protocol":"Redfish"}`)
}

func TestSubscription_PowerEventDelivered(t *testing.T) {
	srv, machineEvents := newEventServer(t)
	rc := newEventReceiver(t)
	subscribe(t, srv, `{"Destination":"`+rc.URL+`/events","Protocol":"Redfish","Context":"my-ctx",
		"HttpHeaders":[{"X-Token":"secret"}]}`)

	machineEvents <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PoweringOn}
	machineEvents <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PowerOn}
	machineEvents <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PoweringOff}
	machineEvents <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PowerOff}

	got := rc.next(t)
	assert.Equal(t, "application/json", got.header.Get("Content-Type"))
	assert.Equal(t, "secret", got.header.Get("X-Token"))
	assert.Equal(t, "#Event.v1_7_0.Event", got.event.ODataType)
	assert.Equal(t, "my-ctx", got.event.Context)
	require.Len(t, got.event.Events, 1)
	rec := got.event.Events[0]
	assert.Equal(t, "ResourceEvent.1.3.ResourcePoweringOn", rec.MessageID)
	assert.Equal(t, "StatusChange", rec.EventType)
	assert.Equal(t, "OK", rec.MessageSeverity)
	assert.Equal(t, "/redfish/v1/Systems/1", rec.OriginOfCondition.ODataID)
	assert.Equal(t, "The resource '/redfish/v1/Systems/1' is powering on.", rec.Message)
	assert.Equal(t, got.event.ID, rec.EventID)
	assert.NotEmpty(t, rec.EventTimestamp)
	// In order
	for _, want := range []string{"ResourcePoweredOn", "ResourcePoweringOff", "ResourcePoweredOff"} {
		assert.Equal(t, "ResourceEvent.1.3."+want, rc.next(t).event.Events[0].MessageID)
	}

	machineEvents <- machine.Event{Type: machine.EventResourceChanged, Resource: machine.ResourceVirtualMedia}
	rec = rc.next(t).event.Events[0]
	assert.Equal(t, "ResourceEvent.1.3.ResourceChanged", rec.MessageID)
	assert.Equal(t, "ResourceUpdated", rec.EventType)
	assert.Equal(t, "/redfish/v1/Systems/1/VirtualMedia/CD1", rec.OriginOfCondition.ODataID)
}

func TestSubscription_Filters(t *testing.T) {
	power := resourceEvent("ResourcePoweredOn", systemPath, systemPath)
	media := resourceEvent("ResourceChanged", systemVirtualMediaPath+"/CD1")
	test := event{record: EventRecord{EventType: "Alert", MessageID: "Base.1.0.Success"}}

	for _, tc := range []struct {
		name string
		sub  subscription
		want []bool // power, media, test
	}{
		{"all", subscription{}, []bool{true, true, true}},
		{"EventTypes", subscription{eventTypes: []string{"StatusChange", "Alert"}}, []bool{true, false, true}},
		{"RegistryPrefixes", subscription{registryPrefixes: []string{"Base"}}, []bool{false, false, true}},
		{"ResourceTypes", subscription{resourceTypes: []string{"VirtualMedia"}}, []bool{false, true, false}},
		{"MessageIds", subscription{messageIDs: []string{"ResourceEvent.ResourcePoweredOn"}}, []bool{true, false, false}},
		{"OriginResources", subscription{originResources: []string{systemPath}}, []bool{true, false, false}},
		{"SubordinateResources", subscription{originResources: []string{systemPath}, subordinate: true}, []bool{true, true, false}},
		{"both", subscription{registryPrefixes: []string{"ResourceEvent"}, resourceTypes: []string{"ComputerSystem"}}, []bool{true, false, false}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := []bool{tc.sub.matches(power), tc.sub.matches(media), tc.sub.matches(test)}
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestSubscription_FilteredDelivery(t *testing.T) {
	srv, machineEvents := newEventServer(t)
	rc := newEventReceiver(t)
	subscribe(t, srv, `{"Destination":"`+rc.URL+`","Protocol":"Redfish","EventTypes":["ResourceUpdated"]}`)

	machineEvents <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PowerOff}
	machineEvents <- machine.Event{Type: machine.EventResourceChanged, Resource: machine.ResourceBoot}
	rec := rc.next(t).event.Events[0]
	assert.Equal(t, "ResourceEvent.1.3.ResourceChanged", rec.MessageID)
	assert.Equal(t, systemPath, rec.OriginOfCondition.ODataID)
	rc.none(t)
}

func TestSubscription_RetriedUntilDelivered(t *testing.T) {
	srv, machineEvents := newEventServer(t)
	rc := newEventReceiver(t)
	rc.setFail(func(n int) bool { return n <= 2 })
	subscribe(t, srv, `{"Destination":"`+rc.URL+`","Protocol":"Redfish"}`)

	machineEvents <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PowerOff}
	machineEvents <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PowerOn}
	// The first event is retried before the second one is sent
	assert.Equal(t, "ResourceEvent.1.3.ResourcePoweredOff", rc.next(t).event.Events[0].MessageID)
	assert.Equal(t, "ResourceEvent.1.3.ResourcePoweredOn", rc.next(t).event.Events[0].MessageID)
	assert.Equal(t, 4, rc.requestCount())
}

func TestSubscription_RetriesExhausted_Suspended(t *testing.T) {
	srv, machineEvents := newEventServer(t)
	rc := newEventReceiver(t)
	rc.setFail(func(int) bool { return true })
	loc := subscribe(t, srv, `{"Destination":"`+rc.URL+`","Protocol":"Redfish"}`)

	machineEvents <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PowerOff}
	// One attempt and DeliveryRetryAttempts (3) retries
	assert.Eventually(t, func() bool {
		sub := decodeBody(t, doReq(srv, "GET", loc, "", nil))
		return sub["Status"].(map[string]interface{})["State"] == "Disabled"
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, 4, rc.requestCount())

	// Suspended: nothing is sent until resumed
	rc.setFail(nil)
	machineEvents <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PowerOn}
	rc.none(t)
	assert.Equal(t, 4, rc.requestCount())
	w := doReq(srv, "POST", loc+"/Actions/EventDestination.ResumeSubscription", `{}`, nil)
	require.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "Enabled", decodeBody(t, doReq(srv, "GET", loc, "", nil))["Status"].(map[string]interface{})["State"])
	machineEvents <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PowerOff}
	assert.Equal(t, "ResourceEvent.1.3.ResourcePoweredOff", rc.next(t).event.Events[0].MessageID)

	require.Equal(t, http.StatusNoContent, doReq(srv, "POST", loc+"/Actions/EventDestination.SuspendSubscription", `{}`, nil).Code)
	machineEvents <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PowerOn}
	rc.none(t)
}

func TestSubscription_RetriesExhausted_Terminated(t *testing.T) {
	srv, machineEvents := newEventServer(t)
	rc := newEventReceiver(t)
	rc.setFail(func(int) bool { return true })
	require.Equal(t, http.StatusOK, doReq(srv, "PATCH", eventServicePath, `{"DeliveryRetryAttempts":1}`, nil).Code)
	loc := subscribe(t, srv, `{"Destination":"`+rc.URL+`","Protocol":"Redfish","DeliveryRetryPolicy":"TerminateAfterRetries"}`)

	machineEvents <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PowerOff}
	assert.Eventually(t, func() bool {
		return doReq(srv, "GET", loc, "", nil).Code == http.StatusNotFound
	}, 2*time.Second, 10*time.Millisecond)
	assert.Equal(t, 2, rc.requestCount())
}

func TestSubscription_RetryForever(t *testing.T) {
	srv, machineEvents := newEventServer(t)
	rc := newEventReceiver(t)
	rc.setFail(func(n int) bool { return n <= 6 })
	subscribe(t, srv, `{"Destination":"`+rc.URL+`","Protocol":"Redfish","DeliveryRetryPolicy":"RetryForever"}`)

	machineEvents <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PowerOff}
	assert.Equal(t, "ResourceEvent.1.3.ResourcePoweredOff", rc.next(t).event.Events[0].MessageID)
	assert.Equal(t, 7, rc.requestCount())
}

func TestSubmitTestEvent(t *testing.T) {
	srv, _ := newEventServer(t)
	rc := newEventReceiver(t)
	subscribe(t, srv, `{"Destination":"`+rc.URL+`","Protocol":"Redfish","Context":"c"}`)

	w := doReq(srv, "POST", eventServicePath+"/Actions/EventService.SubmitTestEvent", `{
		"EventId":"test-1","EventTimestamp":"2026-10-25T10:00:00Z","EventType":"Alert",
		"Message":"Hello","MessageArgs":["a"],"MessageId":"Base.1.0.Success","MessageSeverity":"Warning",
		"OriginOfCondition":"/redfish/v1/Systems/1","EventGroupId":7}`, nil)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	got := rc.next(t).event
	assert.Equal(t, "c", got.Context)
	group := 7
	assert.Equal(t, EventRecord{
		EventType: "Alert", EventID: "test-1", EventTimestamp: "2026-10-25T10:00:00Z", EventGroupID: &group,
		MemberID: "0", Message: "Hello", MessageArgs: []string{"a"}, MessageID: "Base.1.0.Success",
		MessageSeverity: "Warning", OriginOfCondition: &ODataID{ODataID: "/redfish/v1/Systems/1"},
	}, got.Events[0])

	// Only MessageId is required; a known ResourceEvent message gets its text
	w = doReq(srv, "POST", eventServicePath+"/Actions/EventService.SubmitTestEvent",
		`{"MessageId":"ResourceEvent.1.3.ResourcePoweredOn","MessageArgs":["node1"]}`, nil)
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	rec := rc.next(t).event.Events[0]
	assert.Equal(t, "The resource 'node1' has powered on.", rec.Message)
	assert.Equal(t, "Other", rec.EventType)

	for _, body := range []string{`{}`, `{"MessageId":"x","EventType":"Nope"}`, `{"MessageId":"x","MessageSeverity":"Bad"}`,
		`{"MessageId":"x","EventTimestamp":"yesterday"}`, `{"MessageId":"x","OriginOfCondition":"/elsewhere"}`} {
		assert.Equal(t, http.StatusBadRequest,
			doReq(srv, "POST", eventServicePath+"/Actions/EventService.SubmitTestEvent", body, nil).Code, body)
	}
}

func TestSessionEvents(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "admin", "password", "")
	rc := newEventReceiver(t)
	w := doReq(srv, "POST", subscriptionsPath, `{"Destination":"`+rc.URL+`","Protocol":"Redfish","ResourceTypes":["Session"]}`,
		map[string]string{"Authorization": "Basic YWRtaW46cGFzc3dvcmQ="})
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())

	token, location := login(t, srv)
	rec := rc.next(t).event.Events[0]
	assert.Equal(t, "ResourceEvent.1.3.ResourceCreated", rec.MessageID)
	assert.Equal(t, location, rec.OriginOfCondition.ODataID)
	require.Equal(t, http.StatusNoContent, doReq(srv, "DELETE", location, "", map[string]string{"X-Auth-Token": token}).Code)
	rec = rc.next(t).event.Events[0]
	assert.Equal(t, "ResourceEvent.1.3.ResourceRemoved", rec.MessageID)
	assert.Equal(t, location, rec.OriginOfCondition.ODataID)
}

// sseLines reads "data:" lines of an SSE stream into a channel.
func sseLines(body io.Reader) <-chan string {
	ch := make(chan string, 100)
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(body)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
				ch <- data
			}
		}
	}()
	return ch
}

func nextSSE(t *testing.T, ch <-chan string) Event {
	t.Helper()
	select {
	case data := <-ch:
		var ev Event
		require.NoError(t, json.Unmarshal([]byte(data), &ev), data)
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no SSE event")
		return Event{}
	}
}

func TestSSE(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "admin", "password", "")
	src := &fakeEventSource{ch: make(chan machine.Event, 10)}
	srv.SetEventSource(src)
	defer close(src.ch)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Authentication required
	resp, err := http.Get(ts.URL + ssePath)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	get := func(query string) *http.Response {
		req, _ := http.NewRequest("GET", ts.URL+ssePath+query, nil)
		req.SetBasicAuth("admin", "password")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		return resp
	}
	resp = get("?$filter=" + strings.ReplaceAll("MessageId eq 'ResourceEvent.1.3.ResourcePoweredOff' or ResourceType eq 'VirtualMedia'", " ", "%20"))
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))
	filtered := sseLines(resp.Body)
	all := get("")
	defer all.Body.Close()
	unfiltered := sseLines(all.Body)
	// Both streams are registered once their headers are sent
	time.Sleep(50 * time.Millisecond)

	src.ch <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PowerOn}
	src.ch <- machine.Event{Type: machine.EventPowerState, PowerState: machine.PowerOff}
	src.ch <- machine.Event{Type: machine.EventResourceChanged, Resource: machine.ResourceVirtualMedia}

	assert.Equal(t, "ResourceEvent.1.3.ResourcePoweredOff", nextSSE(t, filtered).Events[0].MessageID)
	assert.Equal(t, "ResourceEvent.1.3.ResourceChanged", nextSSE(t, filtered).Events[0].MessageID)
	for _, want := range []string{"ResourcePoweredOn", "ResourcePoweredOff", "ResourceChanged"} {
		ev := nextSSE(t, unfiltered)
		assert.Equal(t, "ResourceEvent.1.3."+want, ev.Events[0].MessageID)
		assert.Empty(t, ev.Context)
	}

	bad := get("?$filter=Severity%20gt%201")
	bad.Body.Close()
	assert.Equal(t, http.StatusBadRequest, bad.StatusCode)
}

func TestParseSSEFilter(t *testing.T) {
	power := resourceEvent("ResourcePoweredOn", systemPath, systemPath)
	media := resourceEvent("ResourceChanged", systemVirtualMediaPath+"/CD1")
	for _, tc := range []struct {
		filter       string
		power, media bool
	}{
		{"", true, true},
		{"EventType eq 'StatusChange'", true, false},
		{"EventType eq StatusChange", true, false},
		{"MessageId eq 'ResourceEvent.1.3.ResourceChanged'", false, true},
		{"MessageId eq 'ResourceEvent.ResourceChanged'", false, true},
		{"OriginResource eq '/redfish/v1/Systems/1'", true, false},
		{"RegistryPrefix eq 'ResourceEvent'", true, true},
		{"ResourceType eq 'VirtualMedia'", false, true},
		{"EventType eq 'StatusChange' or ResourceType eq 'VirtualMedia'", true, true},
		{"RegistryPrefix eq 'ResourceEvent' and EventType eq 'ResourceUpdated'", false, true},
	} {
		f, err := parseSSEFilter(tc.filter)
		require.NoError(t, err, tc.filter)
		assert.Equal(t, tc.power, f(power), tc.filter)
		assert.Equal(t, tc.media, f(media), tc.filter)
	}
	for _, bad := range []string{"EventType ne 'Alert'", "Foo eq 'x'", "(EventType eq 'Alert')"} {
		_, err := parseSSEFilter(bad)
		assert.Error(t, err, bad)
	}
}

func TestResourceTypeOf(t *testing.T) {
	for uri, want := range map[string]string{
		"/redfish/v1/Systems/1":                   "ComputerSystem",
		"/redfish/v1/Systems/1/VirtualMedia/CD1":  "VirtualMedia",
		"/redfish/v1/Managers/1/VirtualMedia/CD1": "VirtualMedia",
		"/redfish/v1/Systems/1/SecureBoot":        "SecureBoot",
		"/redfish/v1/SessionService/Sessions/3":   "Session",
		"/redfish/v1/EventService":                "EventService",
		"/redfish/v1/Chassis/1":                   "Chassis",
		"/redfish/v1/Systems":                     "",
	} {
		assert.Equal(t, want, resourceTypeOf(uri), uri)
	}
}
