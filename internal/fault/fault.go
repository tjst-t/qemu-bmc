// Package fault injects BMC faults (no response, delays, errors, failed
// power actions) into the Redfish and IPMI servers, for testing how
// management tools handle a misbehaving BMC. Faults are managed through a
// separate HTTP API (FAULT_API_ADDR) so that Redfish itself can be made
// unresponsive.
package fault

import (
	"encoding/json"
	"fmt"
	"log"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Kind is the type of a fault.
type Kind string

const (
	RedfishDelay       Kind = "redfish-delay"
	RedfishError       Kind = "redfish-error"
	RedfishHang        Kind = "redfish-hang"
	RedfishDrop        Kind = "redfish-drop"
	IPMIDrop           Kind = "ipmi-drop"
	IPMIDelay          Kind = "ipmi-delay"
	IPMIError          Kind = "ipmi-error"
	PowerActionFail    Kind = "power-action-fail"
	PowerActionIgnored Kind = "power-action-ignored"
	GracefulIgnored    Kind = "graceful-ignored"
	BMCUnavailable     Kind = "bmc-unavailable"
)

// kindTargets maps each kind to its target: what the fault acts on.
var kindTargets = map[Kind]string{
	RedfishDelay:       "redfish",
	RedfishError:       "redfish",
	RedfishHang:        "redfish",
	RedfishDrop:        "redfish",
	IPMIDrop:           "ipmi",
	IPMIDelay:          "ipmi",
	IPMIError:          "ipmi",
	PowerActionFail:    "power",
	PowerActionIgnored: "power",
	GracefulIgnored:    "power",
	BMCUnavailable:     "bmc",
}

const (
	// DefaultHangSeconds is how long a redfish-hang keeps a connection
	// open when hangSeconds is not given.
	DefaultHangSeconds = 300
	// DefaultCompletionCode is the IPMI completion code of ipmi-error and
	// power-action-fail when completionCode is not given (unspecified error).
	DefaultCompletionCode = 0xFF
)

// Byte is a one-byte value given in JSON as a number or a string such as
// "0x0c".
type Byte uint8

// UnmarshalJSON accepts a number (0-255) or a decimal/hex string.
func (b *Byte) UnmarshalJSON(data []byte) error {
	var v uint64
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		n, err := strconv.ParseUint(strings.TrimSpace(s), 0, 8)
		if err != nil {
			return fmt.Errorf("invalid byte %q", s)
		}
		v = n
	} else if err := json.Unmarshal(data, &v); err != nil || v > 0xFF {
		return fmt.Errorf("invalid byte %s", data)
	}
	*b = Byte(v)
	return nil
}

// Fault is one injected fault. Only the fields of its kind apply.
type Fault struct {
	ID     string `json:"id"`
	Kind   Kind   `json:"kind"`
	Target string `json:"target,omitempty"`

	// redfish-*: method (any if empty) and path (prefix or glob, any if
	// empty) of the requests affected
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`
	// redfish-delay, ipmi-delay
	DelayMs int `json:"delayMs,omitempty"`
	// redfish-error: HTTP status
	Status int `json:"status,omitempty"`
	// redfish-hang: seconds before the server closes the connection
	HangSeconds int `json:"hangSeconds,omitempty"`

	// ipmi-*: NetFn and command (any if unset); the messages opening and
	// closing a session are only affected with includeSession
	NetFn          *Byte `json:"netfn,omitempty"`
	Cmd            *Byte `json:"cmd,omitempty"`
	IncludeSession bool  `json:"includeSession,omitempty"`
	// ipmi-error, power-action-fail
	CompletionCode *Byte `json:"completionCode,omitempty"`

	// power-*, graceful-ignored: Redfish ResetType (IPMI chassis control
	// maps to On, ForceOff, PowerCycle, ForceRestart, GracefulShutdown,
	// Nmi); any if empty
	Action string `json:"action,omitempty"`

	// Count is how many times the fault applies before it is removed
	// (unlimited if unset); DurationSeconds how long it lasts (unlimited
	// if unset)
	Count           *int     `json:"count,omitempty"`
	DurationSeconds *float64 `json:"durationSeconds,omitempty"`

	// Reported by GET /faults
	Applied   int        `json:"applied"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// validate checks the fault and fills in defaults.
func (f *Fault) validate() error {
	target, ok := kindTargets[f.Kind]
	if !ok {
		return fmt.Errorf("unknown kind %q", f.Kind)
	}
	if f.Target != "" && f.Target != target {
		return fmt.Errorf("target of %s is %q, not %q", f.Kind, target, f.Target)
	}
	f.Target = target
	if f.Count != nil && *f.Count <= 0 {
		return fmt.Errorf("count must be positive")
	}
	if f.DurationSeconds != nil && *f.DurationSeconds <= 0 {
		return fmt.Errorf("durationSeconds must be positive")
	}
	if f.DelayMs < 0 || f.HangSeconds < 0 {
		return fmt.Errorf("delayMs and hangSeconds must not be negative")
	}
	f.Method = strings.ToUpper(f.Method)
	if f.Path != "" {
		if _, err := path.Match(f.Path, ""); err != nil {
			return fmt.Errorf("invalid path pattern %q", f.Path)
		}
	}
	switch f.Kind {
	case RedfishDelay, IPMIDelay:
		if f.DelayMs == 0 {
			return fmt.Errorf("%s needs delayMs", f.Kind)
		}
	case RedfishError:
		if f.Status == 0 {
			f.Status = 500
		}
		if f.Status < 400 || f.Status > 599 {
			return fmt.Errorf("status must be 400-599")
		}
	case RedfishHang:
		if f.HangSeconds == 0 {
			f.HangSeconds = DefaultHangSeconds
		}
	case IPMIError, PowerActionFail:
		if f.CompletionCode == nil {
			cc := Byte(DefaultCompletionCode)
			f.CompletionCode = &cc
		}
	}
	return nil
}

// Parse decodes a JSON array of faults (FAULTS).
func Parse(data string) ([]Fault, error) {
	var faults []Fault
	dec := json.NewDecoder(strings.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&faults); err != nil {
		return nil, err
	}
	return faults, nil
}

// entry is a fault in the store. released is closed when the fault is
// deleted, ending the hangs it causes.
type entry struct {
	Fault
	expires  time.Time // zero if unlimited
	released chan struct{}
}

// snapshot returns a copy of the fault that does not share the remaining
// count with the store.
func (e *entry) snapshot() Fault {
	f := e.Fault
	if f.Count != nil {
		n := *f.Count
		f.Count = &n
	}
	return f
}

func (e *entry) expired(now time.Time) bool {
	return !e.expires.IsZero() && !now.Before(e.expires)
}

// Store holds the active faults. The zero value is not usable; use
// NewStore.
type Store struct {
	mu     sync.Mutex
	faults []*entry
	nextID int
	now    func() time.Time
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{now: time.Now}
}

// Add validates a fault, gives it an id and adds it after the existing
// faults. The first matching fault in this order applies.
func (s *Store) Add(f Fault) (Fault, error) {
	if err := f.validate(); err != nil {
		return Fault{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	f.ID = strconv.Itoa(s.nextID)
	f.Applied, f.ExpiresAt = 0, nil
	if f.Count != nil {
		n := *f.Count // decremented in place
		f.Count = &n
	}
	e := &entry{Fault: f, released: make(chan struct{})}
	if f.DurationSeconds != nil {
		e.expires = s.now().Add(time.Duration(*f.DurationSeconds * float64(time.Second)))
		t := e.expires
		e.ExpiresAt = &t
	}
	s.faults = append(s.faults, e)
	log.Printf("fault: %s %s added", f.ID, f.Kind)
	return e.snapshot(), nil
}

// List returns the active faults.
func (s *Store) List() []Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	list := make([]Fault, 0, len(s.faults))
	for _, e := range s.faults {
		list = append(list, e.snapshot())
	}
	return list
}

// Delete removes a fault; false if there is none with this id.
func (s *Store) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.faults {
		if e.ID == id {
			s.faults = append(s.faults[:i], s.faults[i+1:]...)
			close(e.released)
			log.Printf("fault: %s %s deleted", e.ID, e.Kind)
			return true
		}
	}
	return false
}

// Clear removes all faults.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.faults {
		close(e.released)
	}
	if len(s.faults) > 0 {
		log.Printf("fault: all %d deleted", len(s.faults))
	}
	s.faults = nil
}

// prune removes expired faults. Callers hold mu.
func (s *Store) prune() {
	now := s.now()
	kept := s.faults[:0]
	for _, e := range s.faults {
		if e.expired(now) {
			log.Printf("fault: %s %s expired", e.ID, e.Kind)
			continue
		}
		kept = append(kept, e)
	}
	s.faults = kept
}

// take applies the first active fault matching match: it counts the use,
// removes the fault when its count is used up, logs it and returns it.
// what describes the request for the log. nil if no fault matches.
func (s *Store) take(match func(*Fault) bool, what string) *entry {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prune()
	for i, e := range s.faults {
		if !match(&e.Fault) {
			continue
		}
		e.Applied++
		if e.Count != nil {
			*e.Count--
			if *e.Count == 0 {
				s.faults = append(s.faults[:i], s.faults[i+1:]...)
			}
		}
		log.Printf("fault: %s %s applied to %s", e.ID, e.Kind, what)
		// The caller reads the fault without the lock
		c := *e
		c.Count = nil
		return &c
	}
	return nil
}

// wait blocks until d passes (forever if d <= 0), the fault is deleted or
// expires, or done is closed.
func (e *entry) wait(d time.Duration, done <-chan struct{}) {
	var timeout <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	var expiry <-chan time.Time
	if !e.expires.IsZero() {
		t := time.NewTimer(time.Until(e.expires))
		defer t.Stop()
		expiry = t.C
	}
	select {
	case <-timeout:
	case <-expiry:
	case <-e.released:
	case <-done:
	}
}
