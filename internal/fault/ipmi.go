package fault

import (
	"fmt"
	"time"
)

// IPMIRequest describes an IPMI request (IPMI over LAN) for the faults.
type IPMIRequest struct {
	// HasCommand is false for the messages that are no IPMI command (ASF
	// presence ping, RMCP+ Open Session and RAKP); NetFn and Cmd are then
	// unset
	HasCommand bool
	NetFn      uint8
	Cmd        uint8
	// Session marks the messages opening or closing a session
	Session bool
}

func (r IPMIRequest) String() string {
	if !r.HasCommand {
		return "IPMI session setup"
	}
	return fmt.Sprintf("IPMI netfn=0x%02x cmd=0x%02x", r.NetFn, r.Cmd)
}

// IPMIAction is what to do with an IPMI request.
type IPMIAction struct {
	// Drop: send no response
	Drop bool
	// Delay: send the response this much later
	Delay time.Duration
	// Error: respond with CompletionCode instead of handling the request
	Error          bool
	CompletionCode uint8
}

// matchIPMI reports whether an ipmi-* fault applies to request r.
func (f *Fault) matchIPMI(r IPMIRequest) bool {
	if r.Session && !f.IncludeSession {
		return false
	}
	if f.NetFn == nil && f.Cmd == nil {
		return true
	}
	if !r.HasCommand {
		return false
	}
	return (f.NetFn == nil || uint8(*f.NetFn) == r.NetFn) &&
		(f.Cmd == nil || uint8(*f.Cmd) == r.Cmd)
}

// IPMI returns the ipmi-* or bmc-unavailable fault action for an IPMI
// request. A nil store returns no action.
func (s *Store) IPMI(r IPMIRequest) IPMIAction {
	e := s.take(func(f *Fault) bool {
		if f.Kind == BMCUnavailable {
			return true
		}
		if f.Kind == IPMIError && !r.HasCommand {
			// Only commands have a completion code
			return false
		}
		return f.Target == "ipmi" && f.matchIPMI(r)
	}, r.String())
	if e == nil {
		return IPMIAction{}
	}
	switch e.Kind {
	case IPMIDelay:
		return IPMIAction{Delay: time.Duration(e.DelayMs) * time.Millisecond}
	case IPMIError:
		return IPMIAction{Error: true, CompletionCode: uint8(*e.CompletionCode)}
	default: // ipmi-drop, bmc-unavailable
		return IPMIAction{Drop: true}
	}
}
