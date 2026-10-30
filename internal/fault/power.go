package fault

import (
	"fmt"
	"log"
)

// PowerActionError is the error of a power action failed by a
// power-action-fail fault. The IPMI server responds with CompletionCode.
type PowerActionError struct {
	FaultID        string
	Action         string
	CompletionCode uint8
}

func (e *PowerActionError) Error() string {
	return fmt.Sprintf("power action %s failed (injected fault %s)", e.Action, e.FaultID)
}

// PowerActionIgnoredError is returned for a power action that a
// power-action-ignored fault made a no-op. The command still succeeds for
// the client (Redfish 204, IPMI completion code 00h); the error only lets
// the caller log that a fault dropped it.
type PowerActionIgnoredError struct {
	FaultID string
	Action  string
}

func (e *PowerActionIgnoredError) Error() string {
	return fmt.Sprintf("power action %s ignored (injected fault %s)", e.Action, e.FaultID)
}

func (f *Fault) matchAction(action string) bool {
	return f.Action == "" || f.Action == action
}

// Reset runs the power action resetType with reset unless a
// power-action-fail fault fails it (*PowerActionError) or a
// power-action-ignored fault makes it a no-op (*PowerActionIgnoredError,
// to be treated as success). A nil store just runs it.
func (s *Store) Reset(resetType string, reset func(string) error) error {
	e := s.take(func(f *Fault) bool {
		return (f.Kind == PowerActionFail || f.Kind == PowerActionIgnored) && f.matchAction(resetType)
	}, "power action "+resetType)
	switch {
	case e == nil:
		return reset(resetType)
	case e.Kind == PowerActionFail:
		return &PowerActionError{FaultID: e.ID, Action: resetType, CompletionCode: uint8(*e.CompletionCode)}
	default:
		return &PowerActionIgnoredError{FaultID: e.ID, Action: resetType}
	}
}

// IgnoreGraceful reports whether a graceful-ignored fault keeps the ACPI
// power button event of GracefulShutdown or GracefulRestart from being
// sent to the VM. A nil store ignores none.
func (s *Store) IgnoreGraceful(resetType string) bool {
	e := s.take(func(f *Fault) bool {
		return f.Kind == GracefulIgnored && f.matchAction(resetType)
	}, "ACPI power button of "+resetType)
	if e != nil {
		log.Printf("%s: ACPI power button event not sent to the VM", resetType)
	}
	return e != nil
}
