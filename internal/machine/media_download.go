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
		return fmt.Errorf("%w: invalid URL %q", ErrInvalidMedia, image)
	}

	ctx, cancel := context.WithCancel(context.Background())
	dl := &mediaDownload{done: make(chan struct{}), cancel: cancel}

	m.mu.Lock()
	old := m.replaceMediaLocked(Media{Image: image, Inserted: true}, "", "")
	m.download = dl
	gen := m.mediaGen
	opts := m.vmedia
	m.mu.Unlock()
	removeMediaFile(old)

	go m.runDownload(ctx, dl, gen, image, username, password, opts)
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
// result, unless the media was replaced in the meantime.
func (m *Machine) runDownload(ctx context.Context, dl *mediaDownload, gen uint64, image, username, password string, opts MediaOptions) {
	log.Printf("VirtualMedia: downloading %s to %s", image, opts.Dir)
	start := time.Now()
	path, err := downloadImage(ctx, opts, gen, image, username, password)

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
		log.Printf("VirtualMedia: download of %s failed, ejecting: %v", image, err)
		m.media, m.mediaPath = Media{}, ""
	} else {
		log.Printf("VirtualMedia: downloaded %s to %s in %s", image, path, time.Since(start).Round(time.Millisecond))
		m.mediaPath, m.mediaFile = path, path
	}
	m.mu.Unlock()
	dl.cancel()
	// Wake a power on waiting for the download before taking resetMu
	close(dl.done)

	m.applyMediaLocked()
}

// downloadImage fetches image into opts.Dir and returns the file path.
func downloadImage(ctx context.Context, opts MediaOptions, gen uint64, image, username, password string) (string, error) {
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

	f, err := os.CreateTemp(opts.Dir, "cd1-*.part")
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
	path := filepath.Join(opts.Dir, fmt.Sprintf("cd1-%d.iso", gen))
	if err := os.Rename(f.Name(), path); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return path, nil
}

// downloadPending reports whether a VirtualMedia download is in progress.
func (m *Machine) downloadPending() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.download != nil
}

// waitMediaDownload waits, up to the media timeout, for a VirtualMedia
// download in progress so that a power on or reset boots with the image.
// Reset waits here before taking resetMu; the dispatched power actions
// check again (holding resetMu) in case another download was started.
func (m *Machine) waitMediaDownload() {
	m.mu.RLock()
	timeout := m.vmedia.Timeout
	m.mu.RUnlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		m.mu.RLock()
		dl, image := m.download, m.media.Image
		m.mu.RUnlock()
		if dl == nil {
			return
		}
		log.Printf("VirtualMedia: waiting for the download of %s before powering on", image)
		select {
		case <-dl.done:
			// The media may have been replaced by another download
		case <-timer.C:
			log.Printf("VirtualMedia: download of %s not finished after %s, powering on without it", image, timeout)
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
