package machine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"time"
)

// ErrHTTPBootUnsupported is returned for the UefiHttp boot target and a
// Boot.HttpBootUri in legacy mode.
var ErrHTTPBootUnsupported = errors.New("UEFI HTTP boot needs process management mode: qemu-bmc cannot change the arguments of an externally started QEMU")

// httpBootState is the Boot.HttpBootUri state. The image is downloaded when
// the URI is set and booted from a hidden CD drive by the UefiHttp target.
type httpBootState struct {
	uri      string // as set ("" = none)
	path     string // downloaded image ("" while downloading or if it failed)
	gen      uint64 // counts URI changes
	download *mediaDownload
}

// ValidateHTTPBootURI checks a Boot.HttpBootUri value: an http(s) URL, or ""
// for none.
func ValidateHTTPBootURI(uri string) error {
	if uri == "" {
		return nil
	}
	u, err := url.Parse(uri)
	if err != nil || !isHTTPImage(uri) || u.Host == "" {
		return fmt.Errorf("invalid HttpBootUri %q: must be an http(s) URL", RedactImage(uri))
	}
	return nil
}

// HTTPBootSupported reports whether the UefiHttp boot target and
// Boot.HttpBootUri can be used (process management mode only).
func (m *Machine) HTTPBootSupported() bool {
	return m.processManager != nil
}

// GetHTTPBootURI returns Boot.HttpBootUri ("" if unset) without its
// password.
func (m *Machine) GetHTTPBootURI() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return RedactImage(m.httpBoot.uri)
}

// SetHTTPBootURI sets Boot.HttpBootUri ("" clears it). The image is
// downloaded in the background like a VirtualMedia image, replacing the
// previous one, and the UefiHttp boot target boots it. Setting a URI again
// downloads it again. The URI is kept until cleared, also once a Once
// override has been used.
func (m *Machine) SetHTTPBootURI(uri string) error {
	if err := ValidateHTTPBootURI(uri); err != nil {
		return err
	}
	if uri != "" && !m.HTTPBootSupported() {
		return ErrHTTPBootUnsupported
	}

	m.mu.Lock()
	if m.httpBoot.download != nil {
		m.httpBoot.download.cancel()
	}
	old := m.httpBoot.path
	changed := uri != m.httpBoot.uri
	m.httpBoot = httpBootState{uri: uri, gen: m.httpBoot.gen + 1}
	if changed {
		m.publishChange(ResourceBoot)
	}
	if uri == "" {
		m.mu.Unlock()
		removeMediaFile(old)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	dl := &mediaDownload{done: make(chan struct{}), cancel: cancel}
	m.httpBoot.download = dl
	gen := m.httpBoot.gen
	opts := m.vmedia
	m.mu.Unlock()
	removeMediaFile(old)

	go m.runHTTPBootDownload(ctx, dl, gen, uri, opts)
	return nil
}

// runHTTPBootDownload downloads the HTTP boot image for URI generation gen
// and records it, unless the URI was changed in the meantime.
func (m *Machine) runHTTPBootDownload(ctx context.Context, dl *mediaDownload, gen uint64, uri string, opts MediaOptions) {
	shown := RedactImage(uri)
	log.Printf("HTTP boot: downloading %s to %s", shown, opts.Dir)
	start := time.Now()
	// Credentials in the URL are sent as Basic auth by net/http
	path, err := downloadImage(ctx, opts, httpBootFilePrefix, gen, uri, "", "")

	m.mu.Lock()
	if m.httpBoot.gen != gen {
		m.mu.Unlock()
		removeMediaFile(path)
		close(dl.done)
		return
	}
	m.httpBoot.download = nil
	if err != nil {
		log.Printf("HTTP boot: download of %s failed, UefiHttp boots without it: %v", shown, err)
	} else {
		log.Printf("HTTP boot: downloaded %s to %s in %s", shown, path, time.Since(start).Round(time.Millisecond))
		m.httpBoot.path = path
	}
	m.mu.Unlock()
	dl.cancel()
	close(dl.done)
}

// nextBoot returns the boot target for the next boot and, for UefiHttp, the
// HTTP boot image ("" = boot from the network). UefiHttp with a URI whose
// image is not available (download failed or not finished) boots as without
// an override. Callers must hold m.mu.
func (m *Machine) nextBoot() (target, httpBootImage string) {
	target = m.nextBootTarget()
	if target != "UefiHttp" || m.httpBoot.uri == "" {
		return target, ""
	}
	if m.httpBoot.path == "" {
		return "None", ""
	}
	return target, m.httpBoot.path
}
