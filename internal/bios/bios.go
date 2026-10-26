// Package bios keeps the BIOS attributes reported by Redfish Systems/1/Bios:
// their current values, the settings pending until the next boot and the
// file they are saved to. The values do not change how the VM runs; BootMode
// and SecureBoot only reflect the VM's configuration.
package bios

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Attribute types (Redfish AttributeRegistry Type)
const (
	TypeEnumeration = "Enumeration"
	TypeString      = "String"
)

// Attribute is the definition of a BIOS attribute.
type Attribute struct {
	Name        string
	DisplayName string
	HelpText    string
	Type        string   // TypeEnumeration or TypeString
	Values      []string // allowed values of an Enumeration
	MaxLength   int      // maximum length of a String
	Default     string   // built-in default (read-only attributes: their value)
	ReadOnly    bool
}

// Definitions are the BIOS attributes, in registry order. Read-only ones get
// their value from the function set with Store.SetReadOnlyValue.
var Definitions = []Attribute{
	{Name: "BootMode", DisplayName: "Boot Mode", HelpText: "Firmware the VM boots (VM_BOOT_MODE); read-only.",
		Type: TypeEnumeration, Values: []string{"Uefi", "Bios"}, Default: "Bios", ReadOnly: true},
	{Name: "SecureBoot", DisplayName: "Secure Boot", HelpText: "UEFI Secure Boot setting (Systems/1/SecureBoot SecureBootEnable); read-only.",
		Type: TypeEnumeration, Values: []string{"Enabled", "Disabled"}, Default: "Disabled", ReadOnly: true},
	{Name: "ProcVirtualization", DisplayName: "Virtualization Technology", HelpText: "Processor virtualization extensions (VT-x / AMD-V).",
		Type: TypeEnumeration, Values: []string{"Enabled", "Disabled"}, Default: "Enabled"},
	{Name: "ProcHyperThreading", DisplayName: "Hyper-Threading", HelpText: "Logical processors (simultaneous multithreading).",
		Type: TypeEnumeration, Values: []string{"Enabled", "Disabled"}, Default: "Enabled"},
	{Name: "SriovGlobalEnable", DisplayName: "SR-IOV Global Enable", HelpText: "Single Root I/O Virtualization support.",
		Type: TypeEnumeration, Values: []string{"Enabled", "Disabled"}, Default: "Disabled"},
	{Name: "PxeDev1EnDis", DisplayName: "PXE Device 1", HelpText: "PXE boot on the first network device.",
		Type: TypeEnumeration, Values: []string{"Enabled", "Disabled"}, Default: "Enabled"},
	{Name: "BootRetry", DisplayName: "Boot Retry", HelpText: "Retry the boot sequence when no device boots.",
		Type: TypeEnumeration, Values: []string{"Enabled", "Disabled"}, Default: "Enabled"},
	{Name: "PowerProfile", DisplayName: "System Profile", HelpText: "Processor power and performance profile.",
		Type: TypeEnumeration, Values: []string{"MaxPerformance", "Balanced", "PowerSaving"}, Default: "MaxPerformance"},
	{Name: "NumLock", DisplayName: "Keyboard NumLock", HelpText: "NumLock state at boot.",
		Type: TypeEnumeration, Values: []string{"On", "Off"}, Default: "On"},
	{Name: "SerialConsoleRedirection", DisplayName: "Serial Console Redirection", HelpText: "Redirect the firmware console to the serial port.",
		Type: TypeEnumeration, Values: []string{"Enabled", "Disabled"}, Default: "Enabled"},
	{Name: "AssetTag", DisplayName: "Asset Tag", HelpText: "System asset tag.",
		Type: TypeString, MaxLength: 64, Default: ""},
}

// Base message registry message keys of the validation errors
const (
	MsgPropertyUnknown        = "PropertyUnknown"
	MsgPropertyValueTypeError = "PropertyValueTypeError"
	MsgPropertyValueNotInList = "PropertyValueNotInList"
	MsgPropertyNotWritable    = "PropertyNotWritable"
	MsgStringValueTooLong     = "StringValueTooLong"
)

// Error is an invalid attribute in a settings request.
type Error struct {
	MessageKey string   // one of the Msg* constants
	Attribute  string   // attribute name
	Args       []string // Base message registry MessageArgs
	Message    string
}

func (e *Error) Error() string { return e.Message }

// Errors are all the invalid attributes of a request.
type Errors []*Error

func (e Errors) Error() string {
	if len(e) == 1 {
		return e[0].Message
	}
	return fmt.Sprintf("%s (and %d more)", e[0].Message, len(e)-1)
}

// state is what is saved to the file.
type state struct {
	Attributes   map[string]string `json:"Attributes"`
	Pending      map[string]string `json:"Pending"`
	ResetPending bool              `json:"ResetPending,omitempty"`
}

// Store holds the BIOS attributes. Pending settings apply at the next boot
// (Apply). It is safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	path     string // "" = not saved
	defaults map[string]string
	state    state
	readOnly map[string]func() string
}

// definition returns the definition of an attribute.
func definition(name string) (Attribute, bool) {
	for _, a := range Definitions {
		if a.Name == name {
			return a, true
		}
	}
	return Attribute{}, false
}

// NewStore creates a store saved to path ("" = memory only). overrides is
// a JSON object replacing the built-in defaults of writable attributes
// (BIOS_ATTRIBUTES, "" = none). The values saved in path are loaded; values
// the definitions no longer allow are dropped.
func NewStore(path, overrides string) (*Store, error) {
	s := &Store{
		path:     path,
		defaults: map[string]string{},
		readOnly: map[string]func() string{},
	}
	for _, a := range Definitions {
		if !a.ReadOnly {
			s.defaults[a.Name] = a.Default
		}
	}
	if overrides != "" {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(overrides), &raw); err != nil {
			return nil, fmt.Errorf("BIOS attributes must be a JSON object: %w", err)
		}
		values, err := validate(raw)
		if err != nil {
			return nil, err
		}
		for k, v := range values {
			s.defaults[k] = v
		}
	}
	s.state = state{Attributes: copyMap(s.defaults), Pending: map[string]string{}}
	if path != "" {
		s.load()
	}
	return s, nil
}

// load reads the saved state, keeping only valid values.
func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return
	}
	var saved state
	if err == nil {
		err = json.Unmarshal(data, &saved)
	}
	if err != nil {
		log.Printf("WARN: BIOS: ignoring %s: %v", s.path, err)
		return
	}
	keep := func(from map[string]string, to map[string]string) {
		for k, v := range from {
			if verr := checkValue(k, v); verr != nil {
				log.Printf("WARN: BIOS: %s: dropping %s=%q: %s", s.path, k, v, verr.Message)
				continue
			}
			to[k] = v
		}
	}
	keep(saved.Attributes, s.state.Attributes)
	keep(saved.Pending, s.state.Pending)
	s.state.ResetPending = saved.ResetPending
	log.Printf("BIOS: attributes loaded from %s", s.path)
}

// save writes the state to the file. Callers must hold mu.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("saving BIOS attributes: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("saving BIOS attributes: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("saving BIOS attributes: %w", err)
	}
	return nil
}

// SetReadOnlyValue makes the read-only attribute name report the value
// returned by fn (one of its allowed values).
func (s *Store) SetReadOnlyValue(name string, fn func() string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readOnly[name] = fn
}

// readOnlyValue returns the value of a read-only attribute. Callers must
// hold mu.
func (s *Store) readOnlyValue(a Attribute) string {
	if fn := s.readOnly[a.Name]; fn != nil {
		return fn()
	}
	return a.Default
}

// Attributes returns the current values of all attributes.
func (s *Store) Attributes() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	attrs := copyMap(s.state.Attributes)
	for _, a := range Definitions {
		if a.ReadOnly {
			attrs[a.Name] = s.readOnlyValue(a)
		}
	}
	return attrs
}

// Pending returns the settings applied at the next boot.
func (s *Store) Pending() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return copyMap(s.state.Pending)
}

// ResetPending reports whether the attributes are reset to their defaults
// at the next boot (before the pending settings apply).
func (s *Store) ResetPending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.ResetPending
}

// SetPending validates attrs (attribute name → JSON value) and adds them to
// the pending settings. Invalid attributes are returned as Errors and
// nothing is changed.
func (s *Store) SetPending(attrs map[string]json.RawMessage) error {
	values, err := validate(attrs)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range values {
		s.state.Pending[k] = v
	}
	if len(values) > 0 {
		log.Printf("BIOS: pending until the next boot: %v", values)
	}
	return s.save()
}

// ResetToDefaults resets the attributes to their defaults at the next boot,
// dropping the settings pending so far.
func (s *Store) ResetToDefaults() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.ResetPending = true
	s.state.Pending = map[string]string{}
	log.Printf("BIOS: reset to defaults at the next boot")
	return s.save()
}

// Apply applies the pending settings (and a pending reset to the defaults);
// it is called when the machine boots. It reports whether anything was
// pending.
func (s *Store) Apply() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.state.ResetPending && len(s.state.Pending) == 0 {
		return false
	}
	if s.state.ResetPending {
		s.state.Attributes = copyMap(s.defaults)
		log.Printf("BIOS: attributes reset to defaults")
	}
	for k, v := range s.state.Pending {
		s.state.Attributes[k] = v
	}
	if len(s.state.Pending) > 0 {
		log.Printf("BIOS: applied %v", s.state.Pending)
	}
	s.state.Pending = map[string]string{}
	s.state.ResetPending = false
	if err := s.save(); err != nil {
		log.Printf("WARN: BIOS: %v", err)
	}
	return true
}

// Registry returns the attribute definitions, with Default the default of
// a writable attribute (BIOS_ATTRIBUTES applied) and the current value of a
// read-only one.
func (s *Store) Registry() []Attribute {
	s.mu.Lock()
	defer s.mu.Unlock()
	defs := make([]Attribute, len(Definitions))
	copy(defs, Definitions)
	for i, a := range defs {
		if a.ReadOnly {
			defs[i].Default = s.readOnlyValue(a)
		} else {
			defs[i].Default = s.defaults[a.Name]
		}
	}
	return defs
}

// validate checks a settings request, returning the values or Errors.
func validate(attrs map[string]json.RawMessage) (map[string]string, error) {
	names := make([]string, 0, len(attrs))
	for k := range attrs {
		names = append(names, k)
	}
	sort.Strings(names)

	var errs Errors
	values := map[string]string{}
	for _, name := range names {
		raw := attrs[name]
		a, ok := definition(name)
		if !ok {
			errs = append(errs, &Error{MessageKey: MsgPropertyUnknown, Attribute: name, Args: []string{name},
				Message: fmt.Sprintf("The property %s is not in the list of valid properties for the resource.", name)})
			continue
		}
		if a.ReadOnly {
			errs = append(errs, &Error{MessageKey: MsgPropertyNotWritable, Attribute: name, Args: []string{name},
				Message: fmt.Sprintf("The property %s is a read only property and cannot be assigned a value.", name)})
			continue
		}
		var v string
		if err := json.Unmarshal(raw, &v); err != nil || string(raw) == "null" {
			errs = append(errs, &Error{MessageKey: MsgPropertyValueTypeError, Attribute: name, Args: []string{string(raw), name},
				Message: fmt.Sprintf("The value %s for the property %s is of a different type than the property can accept.", raw, name)})
			continue
		}
		if verr := checkValue(name, v); verr != nil {
			errs = append(errs, verr)
			continue
		}
		values[name] = v
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return values, nil
}

// checkValue checks a string value of a writable attribute.
func checkValue(name, v string) *Error {
	a, ok := definition(name)
	switch {
	case !ok:
		return &Error{MessageKey: MsgPropertyUnknown, Attribute: name, Args: []string{name},
			Message: fmt.Sprintf("The property %s is not in the list of valid properties for the resource.", name)}
	case a.ReadOnly:
		return &Error{MessageKey: MsgPropertyNotWritable, Attribute: name, Args: []string{name},
			Message: fmt.Sprintf("The property %s is a read only property and cannot be assigned a value.", name)}
	case a.Type == TypeEnumeration:
		for _, allowed := range a.Values {
			if v == allowed {
				return nil
			}
		}
		return &Error{MessageKey: MsgPropertyValueNotInList, Attribute: name, Args: []string{v, name},
			Message: fmt.Sprintf("The value %s for the property %s is not in the list of acceptable values (%v).", v, name, a.Values)}
	case a.Type == TypeString && a.MaxLength > 0 && len(v) > a.MaxLength:
		return &Error{MessageKey: MsgStringValueTooLong, Attribute: name, Args: []string{v, fmt.Sprint(a.MaxLength)},
			Message: fmt.Sprintf("The string %s exceeds the length limit %d.", v, a.MaxLength)}
	}
	return nil
}

func copyMap(m map[string]string) map[string]string {
	c := make(map[string]string, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}
