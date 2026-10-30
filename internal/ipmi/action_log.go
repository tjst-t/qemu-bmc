package ipmi

import (
	"fmt"
	"net"
	"strings"

	"github.com/tjst-t/qemu-bmc/internal/audit"
)

// chassisControlResetTypes are the Redfish ResetTypes of the chassis
// control commands (what handleChassisControl runs).
var chassisControlResetTypes = map[byte]string{
	ChassisControlPowerDown:  "ForceOff",
	ChassisControlPowerUp:    "On",
	ChassisControlPowerCycle: "PowerCycle",
	ChassisControlHardReset:  "ForceRestart",
	ChassisControlSoftOff:    "GracefulShutdown",
	ChassisControlPulse:      "Nmi",
}

// newAction returns the action line of msg if it is a logged write
// command: Chassis Control (kind power) or Set System Boot Options with
// the boot flags parameter (kind boot). Other commands return nil.
func newAction(msg *IPMIMessage, via, remote, user string) *audit.Entry {
	if msg.GetNetFn() != NetFnChassis {
		return nil
	}
	switch msg.Command {
	case CmdChassisControl:
		e := audit.New(audit.KindPower, via, remote).SetUser(user)
		if len(msg.Data) < 1 {
			return e.Set("reset_type", "")
		}
		rt, ok := chassisControlResetTypes[msg.Data[0]]
		if !ok {
			rt = fmt.Sprintf("0x%02x", msg.Data[0])
		}
		return e.Set("reset_type", rt)
	case CmdSetBootOptions:
		if len(msg.Data) < 1 || msg.Data[0]&0x7F != 5 {
			return nil
		}
		e := audit.New(audit.KindBoot, via, remote).SetUser(user)
		if len(msg.Data) < 6 {
			return e
		}
		o := parseBootFlags(msg.Data[1:])
		return e.Set("enabled", o.Enabled).Set("target", o.Target).Set("mode", o.Mode)
	}
	return nil
}

// finishAction sets the result of an action line from the completion code
// (unless the handler set it) and writes the line.
func finishAction(e *audit.Entry, code CompletionCode) {
	if e == nil {
		return
	}
	if !e.HasResult() {
		if code == CompletionCodeOK {
			e.Result(audit.Accepted)
		} else {
			e.Result(audit.Rejected, "cc", ccString(code))
		}
	}
	e.Log()
}

func ccString(code CompletionCode) string {
	return fmt.Sprintf("0x%02x", uint8(code))
}

// sessionUser returns the user name of an IPMI session ("" if none).
func sessionUser(s *Session) string {
	if s == nil {
		return ""
	}
	return strings.TrimRight(string(s.UserName), "\x00")
}

// addrString returns the address ("" if nil).
func addrString(addr net.Addr) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}
