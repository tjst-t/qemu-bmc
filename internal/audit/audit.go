// Package audit logs the write commands qemu-bmc receives (power actions,
// boot overrides, VirtualMedia, BIOS settings, Secure Boot, firmware
// updates), one line per command, so that tests and audits can count them:
//
//	action: kind=power via=redfish remote=192.0.2.10 user=admin reset_type=ForceOff result=accepted
//
// The line starts with "action: " and has key=value fields: kind, via,
// remote and user first, then the fields of the kind, then result and the
// details of the result. A value that is empty is "-"; a value with spaces,
// quotes, "=" or control characters is quoted (Go syntax).
package audit

import (
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// Prefix starts every action line.
const Prefix = "action:"

// The kinds of commands.
const (
	KindPower        = "power"
	KindBoot         = "boot"
	KindVMediaInsert = "vmedia-insert"
	KindVMediaEject  = "vmedia-eject"
	KindBios         = "bios"
	KindSecureBoot   = "secureboot"
	KindUpdate       = "update"
)

// Where a command came from.
const (
	ViaRedfish = "redfish"
	// ViaIPMI is IPMI over LAN (RMCP/RMCP+)
	ViaIPMI = "ipmi"
	// ViaIPMIVM is the in-band IPMI of the guest (VM_IPMI_ADDR)
	ViaIPMIVM = "ipmi-vm"
)

// The results of a command.
const (
	// Accepted: the command was carried out (or started, e.g. an update task)
	Accepted = "accepted"
	// Rejected: the command was refused (invalid, not possible in the
	// current state); nothing was changed
	Rejected = "rejected"
	// Failed: the command was valid but failed inside qemu-bmc
	Failed = "failed"
	// Fault: an injected fault failed or dropped the command
	Fault = "fault"
)

type field struct{ key, value string }

// Entry is one action line being built. The zero value is not usable; use
// New. A nil *Entry ignores every call, so that code without an entry
// (e.g. handlers called directly in tests) needs no checks.
type Entry struct {
	mu      sync.Mutex
	head    []field
	fields  []field
	result  string
	details []field
	logged  bool
}

// New starts the line of a command of the given kind received via via from
// remote (an address "host:port" or a host; empty if unknown).
func New(kind, via, remote string) *Entry {
	return &Entry{head: []field{
		{"kind", kind},
		{"via", via},
		{"remote", RemoteHost(remote)},
		{"user", ""},
	}}
}

// RemoteHost returns the host of a "host:port" address, or addr itself.
func RemoteHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// SetUser sets the user that sent the command.
func (e *Entry) SetUser(user string) *Entry {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.head[3].value = user
	return e
}

// Set sets a field of the command (replacing a field of the same key).
func (e *Entry) Set(key, value string) *Entry {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fields = setField(e.fields, key, value)
	return e
}

// Result sets the result and its details (key, value pairs, e.g.
// "status", "409", "reason", "ActionNotSupported"). A later call replaces
// the result and its details.
func (e *Entry) Result(result string, details ...string) *Entry {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.result = result
	e.details = nil
	for i := 0; i+1 < len(details); i += 2 {
		e.details = setField(e.details, details[i], details[i+1])
	}
	return e
}

// HasResult reports whether Result was called.
func (e *Entry) HasResult() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.result != ""
}

// Log writes the line once (later calls do nothing).
func (e *Entry) Log() {
	if e == nil {
		return
	}
	e.mu.Lock()
	if e.logged {
		e.mu.Unlock()
		return
	}
	e.logged = true
	line := e.stringLocked()
	e.mu.Unlock()
	log.Print(line)
}

// String returns the line.
func (e *Entry) String() string {
	if e == nil {
		return ""
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stringLocked()
}

func (e *Entry) stringLocked() string {
	var b strings.Builder
	b.WriteString(Prefix)
	write := func(fs []field) {
		for _, f := range fs {
			b.WriteByte(' ')
			b.WriteString(f.key)
			b.WriteByte('=')
			b.WriteString(Quote(f.value))
		}
	}
	write(e.head)
	write(e.fields)
	result := e.result
	if result == "" {
		result = Accepted
	}
	write([]field{{"result", result}})
	write(e.details)
	return b.String()
}

func setField(fs []field, key, value string) []field {
	for i := range fs {
		if fs[i].key == key {
			fs[i].value = value
			return fs
		}
	}
	return append(fs, field{key, value})
}

// Quote returns a field value as written in a line: "-" if empty, quoted
// (Go syntax) if it has spaces, quotes, "=" or non-printable characters.
func Quote(v string) string {
	if v == "" {
		return "-"
	}
	if strings.IndexFunc(v, func(r rune) bool {
		return r == '"' || r == '=' || r == '\\' || unicode.IsSpace(r) || !unicode.IsPrint(r)
	}) >= 0 {
		return strconv.Quote(v)
	}
	return v
}
