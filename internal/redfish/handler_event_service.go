package redfish

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/tjst-t/qemu-bmc/internal/machine"
)

// sseKeepAlive is how often an idle SSE stream gets a comment line, so
// that proxies keep it open and a closed connection is noticed.
const sseKeepAlive = 15 * time.Second

// EventSource sends the machine events (implemented by *machine.Machine).
type EventSource interface {
	SubscribeEvents() (<-chan machine.Event, func())
}

// SetEventSource makes the machine's power state and setting changes Redfish
// events.
func (s *Server) SetEventSource(src EventSource) {
	ch, _ := src.SubscribeEvents()
	go func() {
		for e := range ch {
			if ev, ok := machineEvent(e); ok {
				s.events.publish(ev)
			}
		}
	}()
}

// SetEventInsecureTLS sets whether the certificates of https event
// destinations are not verified (EVENT_INSECURE_TLS).
func (s *Server) SetEventInsecureTLS(insecure bool) {
	s.events.setInsecureTLS(insecure)
}

// publishResourceEvent sends a ResourceEvent message (ResourceChanged,
// ResourceCreated, ResourceRemoved) about the resource at origin.
func (s *Server) publishResourceEvent(key, origin string) {
	s.events.publish(resourceEvent(key, origin))
}

// EventServiceResource is the Redfish EventService resource.
type EventServiceResource struct {
	ODataType                     string              `json:"@odata.type"`
	ODataID                       string              `json:"@odata.id"`
	ODataEtag                     string              `json:"@odata.etag,omitempty"`
	ID                            string              `json:"Id"`
	Name                          string              `json:"Name"`
	ServiceEnabled                bool                `json:"ServiceEnabled"`
	DeliveryRetryAttempts         int                 `json:"DeliveryRetryAttempts"`
	DeliveryRetryIntervalSeconds  int                 `json:"DeliveryRetryIntervalSeconds"`
	EventFormatTypes              []string            `json:"EventFormatTypes"`
	EventTypesForSubscription     []string            `json:"EventTypesForSubscription"`
	RegistryPrefixes              []string            `json:"RegistryPrefixes"`
	ResourceTypes                 []string            `json:"ResourceTypes"`
	ServerSentEventURI            string              `json:"ServerSentEventUri"`
	SSEFilterPropertiesSupported  map[string]bool     `json:"SSEFilterPropertiesSupported"`
	SubordinateResourcesSupported bool                `json:"SubordinateResourcesSupported"`
	Status                        ResourceStatus      `json:"Status"`
	Subscriptions                 ODataID             `json:"Subscriptions"`
	Actions                       EventServiceActions `json:"Actions"`
}

// EventServiceActions are the EventService actions.
type EventServiceActions struct {
	SubmitTestEvent ActionTarget `json:"#EventService.SubmitTestEvent"`
}

// ActionTarget is the target of a Redfish action.
type ActionTarget struct {
	Target string `json:"target"`
}

// EventDestination is a Redfish event subscription. HttpHeaders is always
// empty: the headers are write-only.
type EventDestination struct {
	ODataType            string                  `json:"@odata.type"`
	ODataID              string                  `json:"@odata.id"`
	ODataEtag            string                  `json:"@odata.etag,omitempty"`
	ID                   string                  `json:"Id"`
	Name                 string                  `json:"Name"`
	Destination          string                  `json:"Destination"`
	Protocol             string                  `json:"Protocol"`
	Context              string                  `json:"Context"`
	SubscriptionType     string                  `json:"SubscriptionType"`
	EventFormatType      string                  `json:"EventFormatType"`
	EventTypes           []string                `json:"EventTypes,omitempty"`
	RegistryPrefixes     []string                `json:"RegistryPrefixes"`
	ResourceTypes        []string                `json:"ResourceTypes"`
	MessageIDs           []string                `json:"MessageIds"`
	OriginResources      []ODataID               `json:"OriginResources"`
	OriginResourcesCount int                     `json:"OriginResources@odata.count"`
	SubordinateResources bool                    `json:"SubordinateResources"`
	HTTPHeaders          []map[string]string     `json:"HttpHeaders"`
	DeliveryRetryPolicy  string                  `json:"DeliveryRetryPolicy"`
	VerifyCertificate    bool                    `json:"VerifyCertificate"`
	Status               ResourceStatus          `json:"Status"`
	Actions              EventDestinationActions `json:"Actions"`
}

// EventDestinationActions are the EventDestination actions.
type EventDestinationActions struct {
	ResumeSubscription  ActionTarget `json:"#EventDestination.ResumeSubscription"`
	SuspendSubscription ActionTarget `json:"#EventDestination.SuspendSubscription"`
}

// etagOf returns an ETag of a resource's JSON (built without its etag).
func etagOf(v interface{}) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return fmt.Sprintf(`"%x"`, sum[:8])
}

// ifMatchFails writes 412 and returns true if the request has an If-Match
// other than etag (PATCH without If-Match is accepted).
func ifMatchFails(w http.ResponseWriter, r *http.Request, etag string) bool {
	if m := r.Header.Get("If-Match"); m != "" && m != "*" && m != etag {
		writeError(w, http.StatusPreconditionFailed, "PreconditionFailed", "ETag mismatch")
		return true
	}
	return false
}

func (s *Server) eventServiceResource() EventServiceResource {
	es := s.events
	es.mu.Lock()
	enabled, attempts, interval := es.enabled, es.retryAttempts, es.retryInterval
	es.mu.Unlock()
	state := "Enabled"
	if !enabled {
		state = "Disabled"
	}
	res := EventServiceResource{
		ODataType:                    "#EventService.v1_10_0.EventService",
		ODataID:                      eventServicePath,
		ID:                           "EventService",
		Name:                         "Event Service",
		ServiceEnabled:               enabled,
		DeliveryRetryAttempts:        attempts,
		DeliveryRetryIntervalSeconds: interval,
		EventFormatTypes:             []string{"Event"},
		EventTypesForSubscription:    eventTypes,
		RegistryPrefixes:             eventRegistryPrefixes,
		ResourceTypes:                eventResourceTypes,
		ServerSentEventURI:           ssePath,
		SSEFilterPropertiesSupported: map[string]bool{
			"EventFormatType":        false,
			"EventType":              true,
			"MessageId":              true,
			"MetricReportDefinition": false,
			"OriginResource":         true,
			"RegistryPrefix":         true,
			"ResourceType":           true,
		},
		SubordinateResourcesSupported: true,
		Status:                        ResourceStatus{State: state, Health: "OK"},
		Subscriptions:                 ODataID{ODataID: subscriptionsPath},
		Actions: EventServiceActions{
			SubmitTestEvent: ActionTarget{Target: eventServicePath + "/Actions/EventService.SubmitTestEvent"},
		},
	}
	res.ODataEtag = etagOf(res)
	return res
}

func (s *Server) handleGetEventService(w http.ResponseWriter, r *http.Request) {
	res := s.eventServiceResource()
	w.Header().Set("ETag", res.ODataEtag)
	writeJSON(w, http.StatusOK, res)
}

// handlePatchEventService changes ServiceEnabled, DeliveryRetryAttempts and
// DeliveryRetryIntervalSeconds.
func (s *Server) handlePatchEventService(w http.ResponseWriter, r *http.Request) {
	if ifMatchFails(w, r, s.eventServiceResource().ODataEtag) {
		return
	}
	var req map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "Invalid request body")
		return
	}
	var enabled *bool
	var attempts, interval *int
	for name, raw := range req {
		var err error
		switch name {
		case "ServiceEnabled":
			err = json.Unmarshal(raw, &enabled)
		case "DeliveryRetryAttempts":
			err = json.Unmarshal(raw, &attempts)
			if err == nil && attempts != nil && (*attempts < 0 || *attempts > 100) {
				err = errors.New("must be between 0 and 100")
			}
		case "DeliveryRetryIntervalSeconds":
			err = json.Unmarshal(raw, &interval)
			if err == nil && interval != nil && (*interval < 1 || *interval > 3600) {
				err = errors.New("must be between 1 and 3600")
			}
		default:
			if strings.HasPrefix(name, "@") {
				continue
			}
			writeError(w, http.StatusBadRequest, "PropertyNotWritable", name+" cannot be changed")
			return
		}
		if err == nil && string(raw) == "null" {
			err = errors.New("must not be null")
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "PropertyValueError", name+": "+err.Error())
			return
		}
	}
	if enabled == nil && attempts == nil && interval == nil {
		writeError(w, http.StatusBadRequest, "PropertyMissing",
			"ServiceEnabled, DeliveryRetryAttempts or DeliveryRetryIntervalSeconds is required")
		return
	}

	es := s.events
	es.mu.Lock()
	if enabled != nil {
		es.enabled = *enabled
	}
	if attempts != nil {
		es.retryAttempts = *attempts
	}
	if interval != nil {
		es.retryInterval = *interval
	}
	es.mu.Unlock()
	s.publishResourceEvent("ResourceChanged", eventServicePath)
	s.handleGetEventService(w, r)
}

// handleSubmitTestEvent sends an event made of the action parameters.
func (s *Server) handleSubmitTestEvent(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EventGroupID      *int            `json:"EventGroupId"`
		EventID           string          `json:"EventId"`
		EventTimestamp    string          `json:"EventTimestamp"`
		EventType         string          `json:"EventType"`
		Message           *string         `json:"Message"`
		MessageArgs       []string        `json:"MessageArgs"`
		MessageID         string          `json:"MessageId"`
		MessageSeverity   string          `json:"MessageSeverity"`
		Severity          string          `json:"Severity"`
		OriginOfCondition json.RawMessage `json:"OriginOfCondition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "Invalid request body")
		return
	}
	if req.MessageID == "" {
		writeError(w, http.StatusBadRequest, "ActionParameterMissing", "MessageId is required")
		return
	}
	if req.EventType == "" {
		req.EventType = "Other"
	}
	if !contains(eventTypes, req.EventType) {
		writeError(w, http.StatusBadRequest, "ActionParameterValueNotInList",
			"EventType must be one of "+strings.Join(eventTypes, ", "))
		return
	}
	if req.MessageSeverity == "" {
		req.MessageSeverity = req.Severity
	}
	if req.MessageSeverity == "" {
		req.MessageSeverity = "OK"
	}
	if !contains([]string{"OK", "Warning", "Critical"}, req.MessageSeverity) {
		writeError(w, http.StatusBadRequest, "ActionParameterValueNotInList",
			"MessageSeverity must be OK, Warning or Critical")
		return
	}
	if req.EventTimestamp != "" {
		if _, err := time.Parse(time.RFC3339, req.EventTimestamp); err != nil {
			writeError(w, http.StatusBadRequest, "ActionParameterValueFormatError",
				"EventTimestamp must be an RFC 3339 date-time")
			return
		}
	}
	var origin string
	if len(req.OriginOfCondition) > 0 && string(req.OriginOfCondition) != "null" {
		refs, err := parseODataRefs([]json.RawMessage{req.OriginOfCondition})
		if err != nil {
			writeError(w, http.StatusBadRequest, "ActionParameterValueTypeError", "OriginOfCondition: "+err.Error())
			return
		}
		origin = refs[0]
	}

	// A known ResourceEvent message gets its text if none is given
	ev := event{record: EventRecord{
		EventType:       req.EventType,
		EventID:         req.EventID,
		EventTimestamp:  req.EventTimestamp,
		EventGroupID:    req.EventGroupID,
		MessageArgs:     req.MessageArgs,
		MessageID:       req.MessageID,
		MessageSeverity: req.MessageSeverity,
	}}
	if req.Message != nil {
		ev.record.Message = *req.Message
	} else if registryPrefix(req.MessageID) == "ResourceEvent" {
		parts := strings.Split(req.MessageID, ".")
		ev.record.Message = resourceEvent(parts[len(parts)-1], "", req.MessageArgs...).record.Message
	}
	if origin != "" {
		ev.record.OriginOfCondition = &ODataID{ODataID: origin}
		ev.resourceType = resourceTypeOf(origin)
	}

	s.events.mu.Lock()
	enabled := s.events.enabled
	s.events.mu.Unlock()
	if !enabled {
		writeError(w, http.StatusServiceUnavailable, "ServiceDisabled", "The event service is disabled (ServiceEnabled is false)")
		return
	}
	s.events.publish(ev)
	w.WriteHeader(http.StatusNoContent)
}

// parseODataRefs reads resource references given as {"@odata.id": uri} or
// as a URI string.
func parseODataRefs(raws []json.RawMessage) ([]string, error) {
	refs := make([]string, 0, len(raws))
	for _, raw := range raws {
		var uri string
		if err := json.Unmarshal(raw, &uri); err != nil {
			var ref ODataID
			if err := json.Unmarshal(raw, &ref); err != nil {
				return nil, errors.New(`must be {"@odata.id": "<uri>"}`)
			}
			uri = ref.ODataID
		}
		if !strings.HasPrefix(uri, "/redfish/v1") {
			return nil, fmt.Errorf("%q is not a /redfish/v1 resource", uri)
		}
		refs = append(refs, strings.TrimSuffix(uri, "/"))
	}
	return refs, nil
}

func subscriptionPath(id string) string {
	return subscriptionsPath + "/" + id
}

func (s *Server) eventDestination(sub subscription) EventDestination {
	s.events.mu.Lock()
	insecure := s.events.insecureTLS
	s.events.mu.Unlock()
	status := ResourceStatus{State: "Enabled", Health: "OK"}
	if sub.suspended {
		status = ResourceStatus{State: "Disabled", Health: "Warning"}
	}
	origins := make([]ODataID, 0, len(sub.originResources))
	for _, o := range sub.originResources {
		origins = append(origins, ODataID{ODataID: o})
	}
	path := subscriptionPath(sub.id)
	res := EventDestination{
		ODataType:            "#EventDestination.v1_13_0.EventDestination",
		ODataID:              path,
		ID:                   sub.id,
		Name:                 "Event Subscription " + sub.id,
		Destination:          sub.destination,
		Protocol:             "Redfish",
		Context:              sub.context,
		SubscriptionType:     "RedfishEvent",
		EventFormatType:      "Event",
		EventTypes:           sub.eventTypes,
		RegistryPrefixes:     nonNil(sub.registryPrefixes),
		ResourceTypes:        nonNil(sub.resourceTypes),
		MessageIDs:           nonNil(sub.messageIDs),
		OriginResources:      origins,
		OriginResourcesCount: len(origins),
		SubordinateResources: sub.subordinate,
		HTTPHeaders:          []map[string]string{},
		DeliveryRetryPolicy:  sub.policy,
		VerifyCertificate:    !insecure,
		Status:               status,
		Actions: EventDestinationActions{
			ResumeSubscription:  ActionTarget{Target: path + "/Actions/EventDestination.ResumeSubscription"},
			SuspendSubscription: ActionTarget{Target: path + "/Actions/EventDestination.SuspendSubscription"},
		},
	}
	res.ODataEtag = etagOf(res)
	return res
}

func nonNil(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

func (s *Server) handleSubscriptionCollection(w http.ResponseWriter, r *http.Request) {
	ids := s.events.ids()
	members := make([]ODataID, 0, len(ids))
	for _, id := range ids {
		members = append(members, ODataID{ODataID: subscriptionPath(id)})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"@odata.type":         "#EventDestinationCollection.EventDestinationCollection",
		"@odata.id":           subscriptionsPath,
		"Name":                "Event Subscriptions Collection",
		"Members@odata.count": len(members),
		"Members":             members,
	})
}

// handleCreateSubscription subscribes Destination (http or https) to the
// events, optionally filtered by EventTypes (the old form), RegistryPrefixes,
// ResourceTypes, MessageIds and OriginResources (with SubordinateResources).
// HttpHeaders are sent with every event.
func (s *Server) handleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Destination          *string             `json:"Destination"`
		Protocol             *string             `json:"Protocol"`
		Context              *string             `json:"Context"`
		SubscriptionType     *string             `json:"SubscriptionType"`
		EventFormatType      *string             `json:"EventFormatType"`
		EventTypes           []string            `json:"EventTypes"`
		RegistryPrefixes     []string            `json:"RegistryPrefixes"`
		ResourceTypes        []string            `json:"ResourceTypes"`
		MessageIDs           []string            `json:"MessageIds"`
		OriginResources      []json.RawMessage   `json:"OriginResources"`
		SubordinateResources *bool               `json:"SubordinateResources"`
		HTTPHeaders          []map[string]string `json:"HttpHeaders"`
		DeliveryRetryPolicy  *string             `json:"DeliveryRetryPolicy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "Invalid request body: "+err.Error())
		return
	}
	bad := func(code, msg string) { writeError(w, http.StatusBadRequest, code, msg) }
	if req.Destination == nil || req.Protocol == nil {
		bad("PropertyMissing", "Destination and Protocol are required")
		return
	}
	if u, err := url.Parse(*req.Destination); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		bad("PropertyValueFormatError", "Destination must be an http or https URL")
		return
	}
	if *req.Protocol != "Redfish" {
		bad("PropertyValueNotInList", "Protocol must be Redfish")
		return
	}
	if req.SubscriptionType != nil && *req.SubscriptionType != "RedfishEvent" {
		bad("PropertyValueNotInList", "SubscriptionType must be RedfishEvent (use "+ssePath+" for SSE)")
		return
	}
	if req.EventFormatType != nil && *req.EventFormatType != "Event" {
		bad("PropertyValueNotInList", "EventFormatType must be Event")
		return
	}
	for _, check := range []struct {
		name    string
		values  []string
		allowed []string
	}{
		{"EventTypes", req.EventTypes, eventTypes},
		{"RegistryPrefixes", req.RegistryPrefixes, eventRegistryPrefixes},
		{"ResourceTypes", req.ResourceTypes, eventResourceTypes},
	} {
		for _, v := range check.values {
			if !contains(check.allowed, v) {
				bad("PropertyValueNotInList", fmt.Sprintf("%s: %q is not one of %s", check.name, v, strings.Join(check.allowed, ", ")))
				return
			}
		}
	}
	origins, err := parseODataRefs(req.OriginResources)
	if err != nil {
		bad("PropertyValueFormatError", "OriginResources: "+err.Error())
		return
	}
	policy := defaultDeliveryRetryPolicy
	if req.DeliveryRetryPolicy != nil {
		policy = *req.DeliveryRetryPolicy
		if !contains(deliveryRetryPolicies, policy) {
			bad("PropertyValueNotInList", "DeliveryRetryPolicy must be one of "+strings.Join(deliveryRetryPolicies, ", "))
			return
		}
	}

	sub := &subscription{
		destination:      *req.Destination,
		eventTypes:       req.EventTypes,
		registryPrefixes: req.RegistryPrefixes,
		resourceTypes:    req.ResourceTypes,
		messageIDs:       req.MessageIDs,
		originResources:  origins,
		subordinate:      req.SubordinateResources != nil && *req.SubordinateResources,
		policy:           policy,
	}
	if req.Context != nil {
		sub.context = *req.Context
	}
	for _, h := range req.HTTPHeaders {
		for name, value := range h {
			sub.headers = append(sub.headers, [2]string{name, value})
		}
	}

	if err := s.events.add(sub); errors.Is(err, errTooManySubscriptions) {
		writeError(w, http.StatusServiceUnavailable, "CreateLimitReachedForResource",
			fmt.Sprintf("At most %d subscriptions; delete unused ones", maxSubscriptions))
		return
	}
	log.Printf("events: subscription %s created: %s", sub.id, sub.destination)
	created, _ := s.events.get(sub.id)
	res := s.eventDestination(created)
	w.Header().Set("Location", res.ODataID)
	w.Header().Set("ETag", res.ODataEtag)
	writeJSON(w, http.StatusCreated, res)
}

func (s *Server) handleGetSubscription(w http.ResponseWriter, r *http.Request) {
	sub, ok := s.events.get(mux.Vars(r)["subid"])
	if !ok {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "Subscription not found")
		return
	}
	res := s.eventDestination(sub)
	w.Header().Set("ETag", res.ODataEtag)
	writeJSON(w, http.StatusOK, res)
}

// handlePatchSubscription changes Context and DeliveryRetryPolicy.
func (s *Server) handlePatchSubscription(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["subid"]
	sub, ok := s.events.get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "Subscription not found")
		return
	}
	if ifMatchFails(w, r, s.eventDestination(sub).ODataEtag) {
		return
	}
	var req map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "Invalid request body")
		return
	}
	var context, newPolicy *string
	for name, raw := range req {
		var err error
		switch name {
		case "Context":
			err = json.Unmarshal(raw, &context)
			if err == nil && context == nil {
				context = new(string) // null clears it
			}
		case "DeliveryRetryPolicy":
			err = json.Unmarshal(raw, &newPolicy)
			if err == nil && (newPolicy == nil || !contains(deliveryRetryPolicies, *newPolicy)) {
				err = errors.New("must be one of " + strings.Join(deliveryRetryPolicies, ", "))
			}
		default:
			if strings.HasPrefix(name, "@") {
				continue
			}
			writeError(w, http.StatusBadRequest, "PropertyNotWritable", name+" cannot be changed")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "PropertyValueError", name+": "+err.Error())
			return
		}
	}
	if context == nil && newPolicy == nil {
		writeError(w, http.StatusBadRequest, "PropertyMissing", "Context or DeliveryRetryPolicy is required")
		return
	}
	if !s.events.update(id, func(sub *subscription) {
		if context != nil {
			sub.context = *context
		}
		if newPolicy != nil {
			sub.policy = *newPolicy
		}
	}) {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "Subscription not found")
		return
	}
	s.handleGetSubscription(w, r)
}

func (s *Server) handleDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	id := mux.Vars(r)["subid"]
	if !s.events.remove(id) {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "Subscription not found")
		return
	}
	log.Printf("events: subscription %s deleted", id)
	w.WriteHeader(http.StatusNoContent)
}

// handleSuspendSubscription and handleResumeSubscription stop and restart
// the delivery to a subscription (events in between are not delivered; a
// subscription suspended after failed retries is resumed the same way).
func (s *Server) handleSuspendSubscription(w http.ResponseWriter, r *http.Request) {
	s.setSuspended(w, r, true)
}

func (s *Server) handleResumeSubscription(w http.ResponseWriter, r *http.Request) {
	s.setSuspended(w, r, false)
}

func (s *Server) setSuspended(w http.ResponseWriter, r *http.Request, suspended bool) {
	if !s.events.update(mux.Vars(r)["subid"], func(sub *subscription) { sub.suspended = suspended }) {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "Subscription not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSSE streams the events as Server-Sent Events ("id: <EventId>" and
// "data: <Event JSON>"), filtered by $filter (see parseSSEFilter).
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	filter, err := parseSSEFilter(r.URL.Query().Get("$filter"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "QueryParameterValueFormatError", err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "InternalError", "streaming not supported")
		return
	}
	client, remove := s.events.addSSE(filter)
	defer remove()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	keepAlive := time.NewTicker(sseKeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
		case ev := <-client.ch:
			data, err := json.Marshal(ev.payload(""))
			if err != nil {
				continue
			}
			if _, err := fmt.Fprintf(w, "id: %s\ndata: %s\n\n", ev.record.EventID, data); err != nil {
				return
			}
		}
		flusher.Flush()
	}
}
