// Package sysuuid determines the stable system UUID that qemu-bmc passes to
// QEMU (-uuid) and reports via Redfish (Systems/1 UUID) and IPMI
// (Get System GUID).
package sysuuid

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
)

// UUID is a 128-bit UUID in RFC 4122 (network) byte order.
type UUID [16]byte

// namespace is the RFC 4122 name-based namespace for hostname-derived UUIDs.
var namespace = MustParse("6f1b9d4e-3c2a-4e57-9a0d-8b7c5e2f1a63")

// Parse parses the canonical 8-4-4-4-12 hex form.
func Parse(s string) (UUID, error) {
	var u UUID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return u, fmt.Errorf("invalid UUID %q: expected xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx", s)
	}
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil {
		return u, fmt.Errorf("invalid UUID %q: %w", s, err)
	}
	copy(u[:], b)
	return u, nil
}

// MustParse is like Parse but panics on error.
func MustParse(s string) UUID {
	u, err := Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}

// String returns the lowercase canonical form.
func (u UUID) String() string {
	h := hex.EncodeToString(u[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// IPMIGUID returns the 16-byte GUID as encoded in the IPMI Get System GUID
// response: the whole UUID least-significant byte first (IPMI v2.0 spec,
// section 20.8), which is the reverse of RFC 4122 byte order.
func (u UUID) IPMIGUID() []byte {
	b := make([]byte, 16)
	for i := range u {
		b[15-i] = u[i]
	}
	return b
}

// Derive returns a name-based (version 5) UUID for the hostname.
func Derive(hostname string) UUID {
	h := sha1.New()
	h.Write(namespace[:])
	h.Write([]byte(hostname))
	var u UUID
	copy(u[:], h.Sum(nil))
	u[6] = (u[6] & 0x0f) | 0x50
	u[8] = (u[8] & 0x3f) | 0x80
	return u
}

// Source describes where a resolved UUID came from.
type Source string

const (
	SourceExplicit Source = "explicit"
	SourceFile     Source = "file"
	SourceHostname Source = "hostname"
)

// Options are the inputs to Resolve.
type Options struct {
	Explicit string // SYSTEM_UUID or -uuid; used verbatim when set
	File     string // persisted UUID file; read if present, written after derivation
	Hostname string // used to derive a UUID when nothing else is available
}

// Resolve picks the system UUID: an explicit value first, then the UUID
// persisted in File, then a UUID derived from Hostname. A derived UUID is
// written to File (when set) so later starts keep it even if the hostname
// changes.
func Resolve(opts Options) (UUID, Source, error) {
	if opts.Explicit != "" {
		u, err := Parse(opts.Explicit)
		return u, SourceExplicit, err
	}

	if opts.File != "" {
		data, err := os.ReadFile(opts.File)
		switch {
		case err == nil:
			u, err := Parse(strings.TrimSpace(string(data)))
			if err != nil {
				return u, SourceFile, fmt.Errorf("reading %s: %w", opts.File, err)
			}
			return u, SourceFile, nil
		case !errors.Is(err, os.ErrNotExist):
			return UUID{}, SourceFile, fmt.Errorf("reading %s: %w", opts.File, err)
		}
	}

	u := Derive(opts.Hostname)
	if opts.File != "" {
		if err := os.WriteFile(opts.File, []byte(u.String()+"\n"), 0o644); err != nil {
			log.Printf("WARN: could not persist system UUID to %s: %v", opts.File, err)
		}
	}
	return u, SourceHostname, nil
}

// SerialNumber returns the default system serial number for the UUID:
// "QBMC-" followed by the first 10 hex digits of the UUID in upper case.
func (u UUID) SerialNumber() string {
	return "QBMC-" + strings.ToUpper(hex.EncodeToString(u[:5]))
}
