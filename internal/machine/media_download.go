package machine

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultMediaTimeout bounds how long a power on waits for a VirtualMedia
// download to finish.
const DefaultMediaTimeout = 10 * time.Minute

// MediaOptions configures how http(s) VirtualMedia images are downloaded.
type MediaOptions struct {
	// Dir is where downloaded images are stored ("" = <tmp>/vmedia).
	Dir string
	// InsecureTLS skips verification of https server certificates.
	InsecureTLS bool
	// Timeout bounds how long a power on or reset waits for a download.
	Timeout time.Duration
}

func defaultMediaOptions() MediaOptions {
	return MediaOptions{
		Dir:         filepath.Join(os.TempDir(), "vmedia"),
		InsecureTLS: true,
		Timeout:     DefaultMediaTimeout,
	}
}

// mediaDownload is a download in progress; done is closed when it ends.
type mediaDownload struct {
	done   chan struct{}
	cancel context.CancelFunc
}

// SetMediaOptions configures VirtualMedia downloads. An empty Dir and a
// non-positive Timeout keep the current values.
func (m *Machine) SetMediaOptions(opts MediaOptions) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if opts.Dir == "" {
		opts.Dir = m.vmedia.Dir
	}
	if opts.Timeout <= 0 {
		opts.Timeout = m.vmedia.Timeout
	}
	m.vmedia = opts
}

// Download file name prefixes: <prefix>-<gen>.iso, partial <prefix>-*.part.
const (
	mediaFilePrefix    = "cd1"
	httpBootFilePrefix = "httpboot"
)

// CleanStaleDownloads removes images downloaded by an earlier qemu-bmc run
// (VirtualMedia cd1-*.iso, HTTP boot httpboot-*.iso and partial *.part
// files) from the download directory. Only the
// current run tracks its download, so leftovers would otherwise pile up in
// a persistent VMEDIA_DIR. Call it at startup, before any InsertMedia.
func (m *Machine) CleanStaleDownloads() {
	m.mu.RLock()
	dir := m.vmedia.Dir
	m.mu.RUnlock()
	var patterns []string
	for _, prefix := range []string{mediaFilePrefix, httpBootFilePrefix} {
		patterns = append(patterns, prefix+"-*.iso", prefix+"-*.part")
	}
	for _, pattern := range patterns {
		files, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, f := range files {
			removeMediaFile(f)
		}
	}
}

// RedactImage hides the password of an http(s) URL that carries credentials
// ("https://user:pass@host/x" becomes "https://user:xxxxx@host/x"), for logs and
// the reported Image. Other images are returned unchanged.
func RedactImage(image string) string {
	if !isHTTPImage(image) {
		return image
	}
	u, err := url.Parse(image)
	if err != nil || u.User == nil {
		return image
	}
	if _, ok := u.User.Password(); !ok {
		return image
	}
	return u.Redacted()
}

func isHTTPImage(image string) bool {
	lower := strings.ToLower(image)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// insertURL records an http(s) image as inserted and downloads it in the
// background. The image is attached once the download completes (at power
// on, or over QMP if QEMU is running); if it fails, the media is ejected.
func (m *Machine) insertURL(image, username, password string) error {
	u, err := url.Parse(image)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: invalid URL %q", ErrInvalidMedia, RedactImage(image))
	}

	ctx, cancel := context.WithCancel(context.Background())
	dl := &mediaDownload{done: make(chan struct{}), cancel: cancel}

	// Report the URL without its password (sent as Basic auth by the client)
	shown := RedactImage(image)

	m.mu.Lock()
	old := m.replaceMediaLocked(Media{Image: shown, Inserted: true}, "", "")
	m.download = dl
	gen := m.mediaGen
	opts := m.vmedia
	m.mu.Unlock()
	removeMediaFile(old)
	m.publishChange(ResourceVirtualMedia)

	go m.runDownload(ctx, dl, gen, image, shown, username, password, opts)
	// Take the previous image out of a running QEMU right away. Done in the
	// background: a power on waiting for a download holds resetMu.
	go m.applyMediaLocked()
	return nil
}

// replaceMediaLocked sets the media state, abandoning a download in progress.
// It returns the previously downloaded file, which the caller removes.
// Callers must hold m.mu.
func (m *Machine) replaceMediaLocked(media Media, path, file string) (oldFile string) {
	if m.download != nil {
		m.download.cancel()
		m.download = nil
	}
	m.mediaGen++
	oldFile = m.mediaFile
	m.media, m.mediaPath, m.mediaFile = media, path, file
	return oldFile
}

func removeMediaFile(path string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("VirtualMedia: removing %s: %v", path, err)
	}
}

// runDownload downloads image for media generation gen and records the
// result, unless the media was replaced in the meantime. shown is image
// without its password, for logs.
func (m *Machine) runDownload(ctx context.Context, dl *mediaDownload, gen uint64, image, shown, username, password string, opts MediaOptions) {
	log.Printf("VirtualMedia: downloading %s to %s", shown, opts.Dir)
	start := time.Now()
	path, err := downloadImage(ctx, opts, mediaFilePrefix, gen, image, username, password)

	m.mu.Lock()
	if m.mediaGen != gen {
		// Replaced or ejected while downloading
		m.mu.Unlock()
		removeMediaFile(path)
		close(dl.done)
		return
	}
	m.download = nil
	if err != nil {
		log.Printf("VirtualMedia: download of %s failed, ejecting: %v", shown, err)
		m.media, m.mediaPath = Media{}, ""
		m.publishChange(ResourceVirtualMedia)
	} else {
		log.Printf("VirtualMedia: downloaded %s to %s in %s", shown, path, time.Since(start).Round(time.Millisecond))
		m.mediaPath, m.mediaFile = path, path
	}
	m.mu.Unlock()
	dl.cancel()
	// Wake a power on waiting for the download before taking resetMu
	close(dl.done)

	m.applyMediaLocked()
}

// downloadImage fetches image into opts.Dir as <prefix>-<gen>.iso and
// returns the file path.
func downloadImage(ctx context.Context, opts MediaOptions, prefix string, gen uint64, image, username, password string) (string, error) {
	if err := os.MkdirAll(opts.Dir, 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, image, nil)
	if err != nil {
		return "", err
	}
	if username != "" || password != "" {
		req.SetBasicAuth(username, password)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if opts.InsecureTLS {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	defer transport.CloseIdleConnections()
	resp, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %s", resp.Status)
	}

	f, err := os.CreateTemp(opts.Dir, prefix+"-*.part")
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	path := filepath.Join(opts.Dir, fmt.Sprintf("%s-%d.iso", prefix, gen))
	if err := os.Rename(f.Name(), path); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return path, nil
}

// downloadPending reports whether a download the next boot needs is in
// progress.
func (m *Machine) downloadPending() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	dl, _ := m.pendingDownload()
	return dl != nil
}

// pendingDownload returns a download in progress that the next boot needs
// (the VirtualMedia image, or the HTTP boot image for the UefiHttp target)
// and its image for logs, or nil. Callers must hold m.mu.
func (m *Machine) pendingDownload() (*mediaDownload, string) {
	if m.download != nil {
		return m.download, m.media.Image
	}
	if m.httpBoot.download != nil && m.nextBootTarget() == "UefiHttp" {
		return m.httpBoot.download, RedactImage(m.httpBoot.uri)
	}
	return nil, ""
}

// mediaDeadline returns how long a power action requested now waits for a
// VirtualMedia download: now plus the media timeout.
func (m *Machine) mediaDeadline() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return time.Now().Add(m.vmedia.Timeout)
}

// waitMediaDownload waits, until deadline, for a VirtualMedia or HTTP boot
// download in progress so that a power on or reset boots with the image. Reset waits
// here before taking resetMu; the dispatched power actions check again
// (holding resetMu) in case another download was started, with the same
// deadline so that a download that already timed out is not waited for
// again.
func (m *Machine) waitMediaDownload(deadline time.Time) {
	var timer *time.Timer
	for {
		m.mu.RLock()
		dl, image := m.pendingDownload()
		m.mu.RUnlock()
		if dl == nil {
			return
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			// Already given up on (and logged) by an earlier wait, or
			// the deadline passed while waiting for resetMu
			return
		}
		if timer == nil {
			timer = time.NewTimer(remaining)
			defer timer.Stop()
		}
		log.Printf("Waiting for the download of %s before powering on", image)
		select {
		case <-dl.done:
			// The media may have been replaced by another download, or
			// the HTTP boot image may still be downloading
		case <-timer.C:
			log.Printf("Download of %s not finished in time, powering on without it", image)
			return
		}
	}
}

// applyMediaLocked takes resetMu and makes a running QEMU hold the current
// media.
func (m *Machine) applyMediaLocked() {
	m.resetMu.Lock()
	defer m.resetMu.Unlock()
	m.applyMedia()
}

// applyMedia makes a running QEMU hold the current media. Callers hold
// resetMu.
func (m *Machine) applyMedia() {
	if !m.qemuRunning() {
		return
	}
	m.mu.RLock()
	path := m.insertedPath()
	m.mu.RUnlock()
	m.syncMedia(path)
}
