package machine

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

// isoServer serves "/<name>.iso" with body "iso:<name>". Requests for a
// path in gates block until the channel is closed.
func isoServer(t *testing.T, gates map[string]chan struct{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(isoHandler(gates))
	t.Cleanup(srv.Close)
	return srv
}

func isoHandler(gates map[string]chan struct{}) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if gate, ok := gates[r.URL.Path]; ok {
			select {
			case <-gate:
			case <-r.Context().Done():
				return
			}
		}
		if filepath.Ext(r.URL.Path) != ".iso" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("iso:" + r.URL.Path[1:]))
	}
}

func newDownloadMachine(t *testing.T, qmpMock *mockQMPClient, pm *mockProcessManager) (*Machine, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "vmedia")
	var m *Machine
	if pm != nil {
		m = NewWithProcess(qmpMock, pm)
	} else {
		m = New(qmpMock)
	}
	m.SetMediaOptions(MediaOptions{Dir: dir, InsecureTLS: true, Timeout: 5 * time.Second})
	return m, dir
}

// downloadedPath waits for the download to finish and returns the local path
// of the inserted image ("" if it was ejected).
func downloadedPath(t *testing.T, m *Machine) string {
	t.Helper()
	require.Eventually(t, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return m.download == nil
	}, 5*time.Second, 5*time.Millisecond)
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.insertedPath()
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestInsertMedia_URL_Downloads(t *testing.T) {
	var gotUser, gotPass string
	var gotAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, gotAuth = r.BasicAuth()
		w.Write([]byte("iso-data"))
	}))
	defer srv.Close()
	m, dir := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))

	url := srv.URL + "/images/boot.iso"
	require.NoError(t, m.InsertMedia(url, "user", "secret"))
	assert.Equal(t, Media{Image: url, Inserted: true}, m.GetMedia())

	path := downloadedPath(t, m)
	require.NotEmpty(t, path)
	assert.Equal(t, dir, filepath.Dir(path))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "iso-data", string(data))
	assert.Equal(t, Media{Image: url, Inserted: true}, m.GetMedia())
	assert.True(t, gotAuth)
	assert.Equal(t, "user", gotUser)
	assert.Equal(t, "secret", gotPass)
	assert.Len(t, dirEntries(t, dir), 1, "no temporary files left")
}

func TestInsertMedia_URL_NoAuthWithoutCredentials(t *testing.T) {
	gotAuth := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, gotAuth = r.BasicAuth()
		w.Write([]byte("iso"))
	}))
	defer srv.Close()
	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))

	require.NoError(t, m.InsertMedia(srv.URL+"/boot.iso", "", ""))
	require.NotEmpty(t, downloadedPath(t, m))
	assert.False(t, gotAuth)
}

func TestInsertMedia_URL_NotFound_Ejects(t *testing.T) {
	srv := isoServer(t, nil)
	m, dir := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))

	require.NoError(t, m.InsertMedia(srv.URL+"/missing.img", "", ""), "the download happens after the response")
	assert.Empty(t, downloadedPath(t, m))
	assert.Equal(t, Media{}, m.GetMedia())
	assert.Empty(t, dirEntries(t, dir))
}

func TestInsertMedia_URL_HTTPSSelfSigned(t *testing.T) {
	srv := httptest.NewTLSServer(isoHandler(nil))
	defer srv.Close()

	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))
	require.NoError(t, m.InsertMedia(srv.URL+"/boot.iso", "", ""))
	assert.NotEmpty(t, downloadedPath(t, m), "certificate not verified with InsecureTLS")

	m, _ = newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))
	m.SetMediaOptions(MediaOptions{InsecureTLS: false})
	require.NoError(t, m.InsertMedia(srv.URL+"/boot.iso", "", ""))
	assert.Empty(t, downloadedPath(t, m), "self-signed certificate rejected")
	assert.False(t, m.GetMedia().Inserted)
}

func TestInsertMedia_URL_ReplacedWhileDownloading(t *testing.T) {
	gate := make(chan struct{})
	srv := isoServer(t, map[string]chan struct{}{"/first.iso": gate})
	m, dir := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))

	require.NoError(t, m.InsertMedia(srv.URL+"/first.iso", "", ""))
	require.NoError(t, m.InsertMedia(srv.URL+"/second.iso", "", ""))
	close(gate)

	path := downloadedPath(t, m)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "iso:second.iso", string(data))
	assert.Equal(t, Media{Image: srv.URL + "/second.iso", Inserted: true}, m.GetMedia())
	// The abandoned download leaves nothing behind
	assert.Eventually(t, func() bool { return len(dirEntries(t, dir)) == 1 }, 2*time.Second, 5*time.Millisecond)
}

func TestInsertMedia_URL_ReplaceAndEjectRemoveDownloadedFile(t *testing.T) {
	srv := isoServer(t, nil)
	m, dir := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))

	require.NoError(t, m.InsertMedia(srv.URL+"/first.iso", "", ""))
	first := downloadedPath(t, m)
	require.FileExists(t, first)

	require.NoError(t, m.InsertMedia(srv.URL+"/second.iso", "", ""))
	assert.NoFileExists(t, first)
	second := downloadedPath(t, m)
	require.FileExists(t, second)

	// A local image replaces the download too
	iso := testISO(t)
	require.NoError(t, m.InsertMedia(iso, "", ""))
	assert.NoFileExists(t, second)

	require.NoError(t, m.InsertMedia(srv.URL+"/third.iso", "", ""))
	third := downloadedPath(t, m)
	require.FileExists(t, third)
	require.NoError(t, m.EjectMedia())
	assert.NoFileExists(t, third)
	assert.Empty(t, dirEntries(t, dir))
	assert.FileExists(t, iso, "local images are never removed")
}

// startedMedia waits for n QEMU starts and returns the CD images used.
func startedMedia(t *testing.T, pm *mockProcessManager, n int) []string {
	t.Helper()
	require.Eventually(t, func() bool { return len(pm.StartCalls()) >= n }, 5*time.Second, 5*time.Millisecond)
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return append([]string(nil), pm.startMedia...)
}

func TestPowerOn_WaitsForDownload(t *testing.T) {
	gate := make(chan struct{})
	srv := isoServer(t, map[string]chan struct{}{"/boot.iso": gate})
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m, _ := newDownloadMachine(t, qmpMock, pm)

	require.NoError(t, m.InsertMedia(srv.URL+"/boot.iso", "", ""))
	start := time.Now()
	require.NoError(t, m.Reset("On"))
	assert.Less(t, time.Since(start), time.Second, "the caller is not blocked")

	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, pm.StartCalls(), "QEMU is not started before the download finishes")
	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PoweringOn, state)
	assert.Equal(t, Media{Image: srv.URL + "/boot.iso", Inserted: true}, m.GetMedia())
	// Another power on while waiting changes nothing
	require.NoError(t, m.Reset("On"))

	close(gate)
	media := startedMedia(t, pm, 1)
	path := downloadedPath(t, m)
	require.NotEmpty(t, path)
	assert.Equal(t, []string{path}, media)
	assert.Eventually(t, func() bool {
		state, err := m.GetPowerState()
		return err == nil && state == PowerOn
	}, 2*time.Second, 5*time.Millisecond)
}

func TestPowerOn_WaitingForDownload_ForceOffCancels(t *testing.T) {
	gate := make(chan struct{})
	srv := isoServer(t, map[string]chan struct{}{"/boot.iso": gate})
	pm := newMockProcessManager(false)
	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), pm)

	require.NoError(t, m.InsertMedia(srv.URL+"/boot.iso", "", ""))
	require.NoError(t, m.Reset("On"))
	require.NoError(t, m.Reset("ForceOff"))
	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PowerOff, state)

	close(gate)
	require.NotEmpty(t, downloadedPath(t, m))
	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, pm.StartCalls(), "the pending power on was abandoned")
}

func TestForceRestart_WaitsForDownloadAndAttaches(t *testing.T) {
	gate := make(chan struct{})
	srv := isoServer(t, map[string]chan struct{}{"/boot.iso": gate})
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	qmpMock.blocks = []qmp.BlockInfo{{Device: "vmedia0", QDev: "vmedia-cd0", Removable: true}}
	pm := newMockProcessManager(true)
	m, _ := newDownloadMachine(t, qmpMock, pm)

	require.NoError(t, m.InsertMedia(srv.URL+"/boot.iso", "", ""))
	require.NoError(t, m.Reset("ForceRestart"))
	time.Sleep(100 * time.Millisecond)
	assert.NotContains(t, qmpMock.Calls(), "SystemReset")

	close(gate)
	path := downloadedPath(t, m)
	require.Eventually(t, func() bool { return indexOf(qmpMock.Calls(), "SystemReset") >= 0 }, 5*time.Second, 5*time.Millisecond)
	assert.Contains(t, qmpMock.MediumArgs(), "change vmedia-cd0 "+path+" raw")
	calls := qmpMock.Calls()
	assert.Contains(t, calls[:indexOf(calls, "SystemReset")], "BlockdevChangeMedium", "the medium is attached before the reset")
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}

func TestPowerOn_DownloadWaitTimesOut(t *testing.T) {
	gate := make(chan struct{})
	srv := isoServer(t, map[string]chan struct{}{"/boot.iso": gate})
	pm := newMockProcessManager(false)
	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), pm)
	m.SetMediaOptions(MediaOptions{Timeout: 100 * time.Millisecond})

	require.NoError(t, m.InsertMedia(srv.URL+"/boot.iso", "", ""))
	require.NoError(t, m.Reset("On"))
	assert.Equal(t, []string{""}, startedMedia(t, pm, 1), "powered on without the image")

	// Finishing later still gets the image attached
	close(gate)
	assert.NotEmpty(t, downloadedPath(t, m))
}

// The power action runs once VMEDIA_TIMEOUT has passed since the request,
// not after a second full timeout (the dispatched action checks the download
// again while holding resetMu).
func TestDownloadWaitTimeout_NotExtended(t *testing.T) {
	const timeout = 300 * time.Millisecond
	cases := []struct {
		name      string
		resetType string
		legacy    bool
		running   bool
		status    qmp.Status
		done      func(qmpMock *mockQMPClient, pm *mockProcessManager) bool
	}{
		{"process On", "On", false, false, qmp.StatusRunning,
			func(_ *mockQMPClient, pm *mockProcessManager) bool { return len(pm.StartCalls()) > 0 }},
		{"process ForceRestart", "ForceRestart", false, true, qmp.StatusRunning,
			func(q *mockQMPClient, _ *mockProcessManager) bool { return indexOf(q.Calls(), "SystemReset") >= 0 }},
		{"legacy On", "On", true, false, qmp.StatusPaused,
			func(q *mockQMPClient, _ *mockProcessManager) bool { return indexOf(q.Calls(), "Cont") >= 0 }},
		{"legacy ForceRestart", "ForceRestart", true, false, qmp.StatusRunning,
			func(q *mockQMPClient, _ *mockProcessManager) bool { return indexOf(q.Calls(), "SystemReset") >= 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := make(chan struct{})
			srv := isoServer(t, map[string]chan struct{}{"/boot.iso": gate})
			// Release the download even if the test fails, so the server
			// can shut down (cleanups run in reverse order)
			release := sync.OnceFunc(func() { close(gate) })
			t.Cleanup(release)
			qmpMock := newMockQMPClient(tc.status)
			var pm *mockProcessManager
			if !tc.legacy {
				pm = newMockProcessManager(tc.running)
			}
			m, _ := newDownloadMachine(t, qmpMock, pm)
			m.SetMediaOptions(MediaOptions{Timeout: timeout})

			require.NoError(t, m.InsertMedia(srv.URL+"/boot.iso", "", ""))
			start := time.Now()
			require.NoError(t, m.Reset(tc.resetType))
			limit := start.Add(timeout * 3 / 2)
			for !tc.done(qmpMock, pm) {
				if time.Now().After(limit) {
					t.Fatalf("%s not carried out within 1.5x VMEDIA_TIMEOUT", tc.resetType)
				}
				time.Sleep(2 * time.Millisecond)
			}
			assert.GreaterOrEqual(t, time.Since(start), timeout, "waited for the download")

			release()
			assert.NotEmpty(t, downloadedPath(t, m))
		})
	}
}

func TestPowerOn_FailedDownloadBootsWithoutMedia(t *testing.T) {
	gate := make(chan struct{})
	srv := isoServer(t, map[string]chan struct{}{"/missing.img": gate})
	pm := newMockProcessManager(false)
	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), pm)

	require.NoError(t, m.InsertMedia(srv.URL+"/missing.img", "", ""))
	require.NoError(t, m.Reset("On"))
	close(gate)
	assert.Equal(t, []string{""}, startedMedia(t, pm, 1))
	assert.False(t, m.GetMedia().Inserted)
}

func TestEjectMedia_CancelsDownloadAndPowerOnWait(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	srv := isoServer(t, map[string]chan struct{}{"/boot.iso": gate})
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	qmpMock.blocks = []qmp.BlockInfo{{Device: "vmedia0", QDev: "vmedia-cd0", Removable: true}}
	pm := newMockProcessManager(false)
	m, dir := newDownloadMachine(t, qmpMock, pm)

	require.NoError(t, m.InsertMedia(srv.URL+"/boot.iso", "", ""))
	require.NoError(t, m.Reset("On"))
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, m.EjectMedia())
	assert.Equal(t, []string{""}, startedMedia(t, pm, 1))
	assert.Equal(t, Media{}, m.GetMedia())
	assert.Eventually(t, func() bool { return len(dirEntries(t, dir)) == 0 }, 2*time.Second, 5*time.Millisecond)
}

func TestInsertMedia_URL_Running_AttachedWhenDownloaded(t *testing.T) {
	gate := make(chan struct{})
	srv := isoServer(t, map[string]chan struct{}{"/boot.iso": gate})
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	qmpMock.blocks = []qmp.BlockInfo{{Device: "vmedia0", QDev: "vmedia-cd0", Removable: true,
		Inserted: &qmp.BlockInserted{File: "/iso/old.iso"}}}
	m, _ := newDownloadMachine(t, qmpMock, newMockProcessManager(true))
	m.SetInitialMedia("/iso/old.iso")

	require.NoError(t, m.InsertMedia(srv.URL+"/boot.iso", "", ""))
	// The previous image is taken out while downloading
	assert.Eventually(t, func() bool {
		return assert.ObjectsAreEqual([]string{"open vmedia-cd0 force=true", "remove vmedia-cd0", "close vmedia-cd0"}, qmpMock.MediumArgs())
	}, 2*time.Second, 5*time.Millisecond)
	qmpMock.mu.Lock()
	qmpMock.blocks[0].Inserted = nil
	qmpMock.mu.Unlock()

	close(gate)
	path := downloadedPath(t, m)
	assert.Eventually(t, func() bool {
		args := qmpMock.MediumArgs()
		return len(args) == 4 && args[3] == "change vmedia-cd0 "+path+" raw"
	}, 2*time.Second, 5*time.Millisecond)
}

func TestInsertMedia_URL_Legacy(t *testing.T) {
	srv := isoServer(t, nil)
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	qmpMock.blocks = q35Blocks
	m, _ := newDownloadMachine(t, qmpMock, nil)

	require.NoError(t, m.InsertMedia(srv.URL+"/boot.iso", "", ""))
	path := downloadedPath(t, m)
	require.NotEmpty(t, path)
	assert.Eventually(t, func() bool {
		for _, a := range qmpMock.MediumArgs() {
			if a == "change /machine/unattached/device[23] "+path+" raw" {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond)
}

func TestInsertMedia_InvalidURL(t *testing.T) {
	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))
	assert.ErrorIs(t, m.InsertMedia("http:///boot.iso", "", ""), ErrInvalidMedia)
	assert.False(t, m.GetMedia().Inserted)
}

func TestCleanStaleDownloads(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"cd1-3.iso", "cd1-123.part", "keep.iso"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644))
	}
	m := NewWithProcess(newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))
	m.SetMediaOptions(MediaOptions{Dir: dir})
	m.CleanStaleDownloads()

	left, _ := filepath.Glob(filepath.Join(dir, "*"))
	assert.Equal(t, []string{filepath.Join(dir, "keep.iso")}, left)
}

func TestRedactImage(t *testing.T) {
	assert.Equal(t, "https://user:xxxxx@host/a.iso", RedactImage("https://user:secret@host/a.iso"))
	assert.Equal(t, "http://user@host/a.iso", RedactImage("http://user@host/a.iso"))
	assert.Equal(t, "https://host/a%20b.iso?x=1", RedactImage("https://host/a%20b.iso?x=1"))
	assert.Equal(t, "/iso/a.iso", RedactImage("/iso/a.iso"))
}

func TestInsertMedia_URL_CredentialsInURL_NotReportedOrLogged(t *testing.T) {
	var gotUser, gotPass string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		if gotPass != "s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte("iso"))
	}))
	defer srv.Close()
	var logs bytes.Buffer
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))

	host := strings.TrimPrefix(srv.URL, "http://")
	require.NoError(t, m.InsertMedia("http://vm:s3cret@"+host+"/boot.iso", "", ""))
	want := Media{Image: "http://vm:xxxxx@" + host + "/boot.iso", Inserted: true}
	assert.Equal(t, want, m.GetMedia())
	require.NotEmpty(t, downloadedPath(t, m))
	assert.Equal(t, "vm", gotUser)
	assert.Equal(t, want, m.GetMedia())

	// A failed download (wrong password) is logged without the password too
	require.NoError(t, m.InsertMedia("http://vm:wr0ng@"+host+"/boot.iso", "", ""))
	assert.Empty(t, downloadedPath(t, m))
	// Connection errors include the URL
	srv.Close()
	require.NoError(t, m.InsertMedia("http://vm:wr0ng@"+host+"/boot.iso", "", ""))
	assert.Empty(t, downloadedPath(t, m))

	assert.NotContains(t, logs.String(), "s3cret")
	assert.NotContains(t, logs.String(), "wr0ng")
	assert.Contains(t, logs.String(), "vm:xxxxx@")
}
