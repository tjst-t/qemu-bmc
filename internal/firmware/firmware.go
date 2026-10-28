// Package firmware keeps the simulated firmware versions of the BMC and the
// BIOS reported by Redfish (UpdateService FirmwareInventory, Managers/1
// FirmwareVersion, Systems/1 BiosVersion), checks firmware images and saves
// the versions installed by updates to a file.
//
// Nothing is really flashed: an image is a small text header naming its
// target and version (see ReadImage). A BMC update changes the version
// right away; a BIOS update is pending until the machine boots (Apply).
package firmware

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Update targets
const (
	TargetBMC  = "BMC"
	TargetBIOS = "BIOS"
)

// Targets are the firmware components, in FirmwareInventory order.
var Targets = []string{TargetBMC, TargetBIOS}

// DefaultBIOSVersion is the BIOS version when BIOS_FIRMWARE_VERSION is
// not set.
const DefaultBIOSVersion = "QBMC-BIOS-1.0.0"

const (
	// ImageMagic starts the first line of an image:
	// "QBMC-FW <target> <version>\n"
	ImageMagic = "QBMC-FW"
	// MaxImageBytes bounds the size of an image
	MaxImageBytes = 256 << 20
	// maxHeaderBytes bounds the first line
	maxHeaderBytes = 256
	// maxVersionLength bounds the version string
	maxVersionLength = 64
)

// ErrInvalidImage is returned for an image that is not a qemu-bmc firmware
// image.
var ErrInvalidImage = errors.New("invalid firmware image")

// Image is what a firmware image installs.
type Image struct {
	Target  string // TargetBMC or TargetBIOS
	Version string
	Size    int64 // bytes read
}

// IsTarget reports whether t is a firmware target.
func IsTarget(t string) bool {
	for _, target := range Targets {
		if t == target {
			return true
		}
	}
	return false
}

// ReadImage reads a whole firmware image from r and checks its first line,
// "QBMC-FW <target> <version>" (target BMC or BIOS, version up to 64
// printable characters without spaces), ended by a newline or the end of
// the image. Any bytes may follow. An image that does not have this form,
// or is larger than MaxImageBytes, is ErrInvalidImage.
func ReadImage(r io.Reader) (Image, error) {
	br := bufio.NewReaderSize(io.LimitReader(r, MaxImageBytes+1), maxHeaderBytes+1)
	line, err := br.ReadSlice('\n')
	switch {
	case err == bufio.ErrBufferFull:
		return Image{}, fmt.Errorf("%w: first line longer than %d bytes", ErrInvalidImage, maxHeaderBytes)
	case err != nil && err != io.EOF:
		return Image{}, err
	}
	img, herr := parseHeader(string(bytes.TrimRight(line, "\r\n")))
	img.Size = int64(len(line))
	n, err := io.Copy(io.Discard, br)
	img.Size += n
	if err != nil {
		return Image{}, err
	}
	if img.Size > MaxImageBytes {
		return Image{}, fmt.Errorf("%w: larger than %d bytes", ErrInvalidImage, MaxImageBytes)
	}
	if herr != nil {
		return Image{}, herr
	}
	return img, nil
}

func parseHeader(line string) (Image, error) {
	fields := strings.Split(line, " ")
	if len(fields) == 0 || fields[0] != ImageMagic {
		return Image{}, fmt.Errorf("%w: the first line must be %q", ErrInvalidImage, ImageMagic+" <BMC|BIOS> <version>")
	}
	if len(fields) != 3 {
		return Image{}, fmt.Errorf("%w: the first line must be %q, got %q", ErrInvalidImage, ImageMagic+" <BMC|BIOS> <version>", line)
	}
	target, version := fields[1], fields[2]
	if !IsTarget(target) {
		return Image{}, fmt.Errorf("%w: unknown target %q (BMC or BIOS)", ErrInvalidImage, target)
	}
	if version == "" || len(version) > maxVersionLength {
		return Image{}, fmt.Errorf("%w: the version must be 1-%d characters", ErrInvalidImage, maxVersionLength)
	}
	for _, c := range version {
		if c <= ' ' || c > '~' {
			return Image{}, fmt.Errorf("%w: the version must be printable ASCII without spaces", ErrInvalidImage)
		}
	}
	return Image{Target: target, Version: version}, nil
}

// DownloadOptions configures how SimpleUpdate images are fetched (the
// VirtualMedia settings: VMEDIA_INSECURE_TLS, VMEDIA_TIMEOUT).
type DownloadOptions struct {
	InsecureTLS bool
	Timeout     time.Duration // 0 = none
}

// Download fetches an http(s) image and checks it (ReadImage). username and
// password, if set, are sent as Basic auth.
func Download(ctx context.Context, opts DownloadOptions, uri, username, password string) (Image, error) {
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return Image{}, err
	}
	if username != "" || password != "" {
		req.SetBasicAuth(username, password)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if opts.InsecureTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // VMEDIA_INSECURE_TLS
	}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return Image{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Image{}, fmt.Errorf("HTTP %s", resp.Status)
	}
	return ReadImage(resp.Body)
}

// saved is what is written to the file: the versions installed by updates
// (they win over the configured ones) and the BIOS version pending until
// the next boot.
type saved struct {
	Versions map[string]string `json:"Versions,omitempty"`
	Pending  map[string]string `json:"Pending,omitempty"`
}

// Store holds the firmware versions. It is safe for concurrent use.
type Store struct {
	mu       sync.Mutex
	path     string            // "" = not saved
	defaults map[string]string // configured versions
	state    saved
	hooks    []func(target string)
}

// NewStore creates a store saved to path ("" = memory only) with the
// configured versions of the BMC and the BIOS. Versions saved in path win.
func NewStore(path, bmcVersion, biosVersion string) *Store {
	s := &Store{
		path:     path,
		defaults: map[string]string{TargetBMC: bmcVersion, TargetBIOS: biosVersion},
		state:    saved{Versions: map[string]string{}, Pending: map[string]string{}},
	}
	if path != "" {
		s.load()
	}
	return s
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return
	}
	var st saved
	if err == nil {
		err = json.Unmarshal(data, &st)
	}
	if err != nil {
		log.Printf("WARN: firmware: ignoring %s: %v", s.path, err)
		return
	}
	keep := func(from, to map[string]string) {
		for target, v := range from {
			if _, err := parseHeader(ImageMagic + " " + target + " " + v); err != nil {
				log.Printf("WARN: firmware: %s: dropping %s=%q: %v", s.path, target, v, err)
				continue
			}
			to[target] = v
		}
	}
	keep(st.Versions, s.state.Versions)
	keep(st.Pending, s.state.Pending)
	log.Printf("firmware: versions loaded from %s", s.path)
}

// save writes the state to the file. Callers hold mu.
func (s *Store) save() error {
	if s.path == "" {
		return nil
	}
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("saving firmware versions: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("saving firmware versions: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("saving firmware versions: %w", err)
	}
	return nil
}

// OnChange adds a function called (without locks held) after the version
// or the pending version of a target changed.
func (s *Store) OnChange(fn func(target string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = append(s.hooks, fn)
}

func (s *Store) changed(targets ...string) {
	s.mu.Lock()
	hooks := append([]func(string){}, s.hooks...)
	s.mu.Unlock()
	for _, t := range targets {
		for _, fn := range hooks {
			fn(t)
		}
	}
}

// Version returns the running version of target.
func (s *Store) Version(target string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.state.Versions[target]; ok {
		return v
	}
	return s.defaults[target]
}

// Pending returns the version of target installed but not active until
// the next boot, "" if none.
func (s *Store) Pending(target string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state.Pending[target]
}

// ActivatesOnReset reports whether an update of target only takes effect
// when the machine boots.
func ActivatesOnReset(target string) bool {
	return target == TargetBIOS
}

// Install installs version on target: the BMC runs it right away, the BIOS
// at the next boot (it replaces a version pending so far). It reports
// whether the version is pending.
func (s *Store) Install(target, version string) (pending bool, err error) {
	if !IsTarget(target) {
		return false, fmt.Errorf("unknown firmware target %q", target)
	}
	s.mu.Lock()
	if ActivatesOnReset(target) {
		s.state.Pending[target] = version
		log.Printf("firmware: %s %s pending until the next boot", target, version)
	} else {
		s.state.Versions[target] = version
		log.Printf("firmware: %s updated to %s", target, version)
	}
	err = s.save()
	s.mu.Unlock()
	s.changed(target)
	return ActivatesOnReset(target), err
}

// Apply activates the pending versions; it is called when the machine
// boots. It reports whether anything was pending.
func (s *Store) Apply() bool {
	s.mu.Lock()
	if len(s.state.Pending) == 0 {
		s.mu.Unlock()
		return false
	}
	var targets []string
	for _, t := range Targets {
		if v, ok := s.state.Pending[t]; ok {
			s.state.Versions[t] = v
			targets = append(targets, t)
			log.Printf("firmware: %s updated to %s", t, v)
		}
	}
	s.state.Pending = map[string]string{}
	if err := s.save(); err != nil {
		log.Printf("WARN: firmware: %v", err)
	}
	s.mu.Unlock()
	s.changed(targets...)
	return true
}
