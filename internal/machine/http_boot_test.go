package machine

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

// httpBootPath waits for the HTTP boot download to finish and returns the
// downloaded image ("" if it failed).
func httpBootPath(t *testing.T, m *Machine) string {
	t.Helper()
	require.Eventually(t, func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return m.httpBoot.download == nil
	}, 5*time.Second, 5*time.Millisecond)
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.httpBoot.path
}

// startedHTTPBoot waits for n QEMU starts and returns the boot targets and
// HTTP boot images used.
func startedHTTPBoot(t *testing.T, pm *mockProcessManager, n int) (targets, images []string) {
	t.Helper()
	require.Eventually(t, func() bool { return len(pm.StartCalls()) >= n }, 5*time.Second, 5*time.Millisecond)
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return append([]string(nil), pm.startCalls...), append([]string(nil), pm.startHTTP...)
}

func uefiHTTPOnce() BootOverride {
	return BootOverride{Enabled: "Once", Target: "UefiHttp", Mode: "UEFI"}
}

func TestValidateHTTPBootURI(t *testing.T) {
	for _, ok := range []string{"", "http://host/a.iso", "https://10.0.0.1:8443/x/boot.iso?t=1", "HTTPS://host/a"} {
		assert.NoError(t, ValidateHTTPBootURI(ok), ok)
	}
	for _, bad := range []string{"ftp://host/a.iso", "/vm/a.iso", "file:///a.iso", "http://", "host/a.iso", "http://host:bad/"} {
		assert.Error(t, ValidateHTTPBootURI(bad), bad)
	}
}

func TestSetHTTPBootURI_Invalid(t *testing.T) {
	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))
	assert.Error(t, m.SetHTTPBootURI("tftp://host/a.efi"))
	assert.Equal(t, "", m.GetHTTPBootURI())
}

func TestHTTPBoot_LegacyMode_Unsupported(t *testing.T) {
	m := New(newMockQMPClient(qmp.StatusRunning))
	assert.False(t, m.HTTPBootSupported())
	assert.ErrorIs(t, m.SetBootOverride(uefiHTTPOnce()), ErrHTTPBootUnsupported)
	assert.Equal(t, "None", m.GetBootOverride().Target, "nothing changed")
	assert.ErrorIs(t, m.SetHTTPBootURI("http://host/a.iso"), ErrHTTPBootUnsupported)
	assert.NoError(t, m.SetHTTPBootURI(""), "clearing is accepted")
	assert.Equal(t, "", m.GetHTTPBootURI())
	// Other targets still work
	assert.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Pxe", Mode: "UEFI"}))
}

func TestHTTPBoot_WithURI_BootsHiddenCDOnce(t *testing.T) {
	srv := isoServer(t, nil)
	pm := newMockProcessManager(false)
	m, dir := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), pm)

	uri := srv.URL + "/boot.iso"
	require.NoError(t, m.SetBootOverride(uefiHTTPOnce()))
	require.NoError(t, m.SetHTTPBootURI(uri))
	assert.Equal(t, uri, m.GetHTTPBootURI())
	path := httpBootPath(t, m)
	require.NotEmpty(t, path)
	assert.Equal(t, filepath.Join(dir, "httpboot-1.iso"), path)
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "iso:boot.iso", string(data))

	require.NoError(t, m.Reset("On"))
	targets, images := startedHTTPBoot(t, pm, 1)
	assert.Equal(t, []string{"UefiHttp"}, targets)
	assert.Equal(t, []string{path}, images)
	assert.Equal(t, Media{}, m.GetMedia(), "not VirtualMedia")
	pm.mu.Lock()
	assert.Equal(t, []string{""}, pm.startMedia, "the VirtualMedia drive stays empty")
	pm.mu.Unlock()

	// Once is used up; the URI stays until cleared
	assert.Equal(t, "None", m.GetBootOverride().Target)
	assert.Equal(t, uri, m.GetHTTPBootURI())

	pm.exit() // the guest powered off
	require.NoError(t, m.Reset("On"))
	targets, images = startedHTTPBoot(t, pm, 2)
	assert.Equal(t, "None", targets[1])
	assert.Equal(t, "", images[1], "no hidden CD after Once")
	assert.FileExists(t, path)
}

func TestHTTPBoot_ForceRestart_AfterOnce_RestartsWithoutHiddenCD(t *testing.T) {
	srv := isoServer(t, nil)
	pm := newMockProcessManager(false)
	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), pm)

	require.NoError(t, m.SetBootOverride(uefiHTTPOnce()))
	require.NoError(t, m.SetHTTPBootURI(srv.URL+"/boot.iso"))
	require.NotEmpty(t, httpBootPath(t, m))
	require.NoError(t, m.Reset("On"))
	startedHTTPBoot(t, pm, 1)

	require.NoError(t, m.Reset("ForceRestart"))
	targets, images := startedHTTPBoot(t, pm, 2)
	assert.Equal(t, []string{"UefiHttp", "None"}, targets, "QEMU restarted to drop the hidden CD")
	assert.Equal(t, "", images[1])
}

func TestHTTPBoot_NoURI_BootsFromNetwork(t *testing.T) {
	pm := newMockProcessManager(false)
	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), pm)

	require.NoError(t, m.SetBootOverride(uefiHTTPOnce()))
	require.NoError(t, m.Reset("On"))
	targets, images := startedHTTPBoot(t, pm, 1)
	assert.Equal(t, []string{"UefiHttp"}, targets)
	assert.Equal(t, []string{""}, images)
}

func TestHTTPBoot_PowerOnWaitsForDownload(t *testing.T) {
	gate := make(chan struct{})
	srv := isoServer(t, map[string]chan struct{}{"/boot.iso": gate})
	pm := newMockProcessManager(false)
	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), pm)

	require.NoError(t, m.SetBootOverride(uefiHTTPOnce()))
	require.NoError(t, m.SetHTTPBootURI(srv.URL+"/boot.iso"))
	start := time.Now()
	require.NoError(t, m.Reset("On"))
	assert.Less(t, time.Since(start), time.Second, "the caller is not blocked")

	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, pm.StartCalls(), "QEMU is not started before the download finishes")
	state, err := m.GetPowerState()
	require.NoError(t, err)
	assert.Equal(t, PoweringOn, state)

	close(gate)
	targets, images := startedHTTPBoot(t, pm, 1)
	path := httpBootPath(t, m)
	require.NotEmpty(t, path)
	assert.Equal(t, []string{"UefiHttp"}, targets)
	assert.Equal(t, []string{path}, images)
}

func TestHTTPBoot_OtherTargetDoesNotWaitForDownload(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	srv := isoServer(t, map[string]chan struct{}{"/boot.iso": gate})
	pm := newMockProcessManager(false)
	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), pm)

	require.NoError(t, m.SetHTTPBootURI(srv.URL+"/boot.iso"))
	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Hdd", Mode: "UEFI"}))
	require.NoError(t, m.Reset("On"))
	assert.Equal(t, []string{"Hdd"}, pm.StartCalls())
}

func TestHTTPBoot_DownloadTimeout_BootsWithoutIt(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	srv := isoServer(t, map[string]chan struct{}{"/boot.iso": gate})
	pm := newMockProcessManager(false)
	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), pm)
	m.SetMediaOptions(MediaOptions{Timeout: 200 * time.Millisecond})

	require.NoError(t, m.SetBootOverride(uefiHTTPOnce()))
	require.NoError(t, m.SetHTTPBootURI(srv.URL+"/boot.iso"))
	require.NoError(t, m.Reset("On"))
	targets, images := startedHTTPBoot(t, pm, 1)
	assert.Equal(t, []string{"None"}, targets)
	assert.Equal(t, []string{""}, images)
}

func TestHTTPBoot_DownloadFailed_BootsWithoutIt(t *testing.T) {
	srv := isoServer(t, nil)
	pm := newMockProcessManager(false)
	m, dir := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), pm)

	uri := srv.URL + "/missing.img"
	require.NoError(t, m.SetBootOverride(uefiHTTPOnce()))
	require.NoError(t, m.SetHTTPBootURI(uri))
	assert.Empty(t, httpBootPath(t, m))
	assert.Equal(t, uri, m.GetHTTPBootURI(), "the URI is still reported")

	require.NoError(t, m.Reset("On"))
	targets, images := startedHTTPBoot(t, pm, 1)
	assert.Equal(t, []string{"None"}, targets)
	assert.Equal(t, []string{""}, images)
	assert.Empty(t, dirEntries(t, dir))
}

func TestHTTPBoot_ChangeAndClearURI_RemovesPreviousFile(t *testing.T) {
	srv := isoServer(t, nil)
	m, dir := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))

	require.NoError(t, m.SetHTTPBootURI(srv.URL+"/a.iso"))
	first := httpBootPath(t, m)
	require.NotEmpty(t, first)

	require.NoError(t, m.SetHTTPBootURI(srv.URL+"/b.iso"))
	second := httpBootPath(t, m)
	require.NotEmpty(t, second)
	assert.NoFileExists(t, first)
	data, err := os.ReadFile(second)
	require.NoError(t, err)
	assert.Equal(t, "iso:b.iso", string(data))

	require.NoError(t, m.SetHTTPBootURI(""))
	assert.Equal(t, "", m.GetHTTPBootURI())
	assert.NoFileExists(t, second)
	assert.Empty(t, dirEntries(t, dir))
}

func TestHTTPBoot_ClearWhileDownloading_PowerOnGoesAhead(t *testing.T) {
	gate := make(chan struct{})
	defer close(gate)
	srv := isoServer(t, map[string]chan struct{}{"/boot.iso": gate})
	pm := newMockProcessManager(false)
	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), pm)

	require.NoError(t, m.SetBootOverride(uefiHTTPOnce()))
	require.NoError(t, m.SetHTTPBootURI(srv.URL+"/boot.iso"))
	m.mu.RLock()
	dl := m.httpBoot.download
	m.mu.RUnlock()
	require.NotNil(t, dl)
	require.NoError(t, m.SetHTTPBootURI(""))
	require.NoError(t, m.Reset("On"))
	targets, images := startedHTTPBoot(t, pm, 1)
	assert.Equal(t, []string{"UefiHttp"}, targets, "boots from the network")
	assert.Equal(t, []string{""}, images)

	// Let the abandoned download finish (it may still create the download
	// directory) before TempDir is removed
	select {
	case <-dl.done:
	case <-time.After(5 * time.Second):
		t.Fatal("abandoned download did not finish")
	}
}

func TestHTTPBoot_CredentialsInURI_Redacted(t *testing.T) {
	srv := isoServer(t, nil)
	m, _ := newDownloadMachine(t, newMockQMPClient(qmp.StatusRunning), newMockProcessManager(false))
	uri := "http://user:secret@" + srv.Listener.Addr().String() + "/a.iso"
	require.NoError(t, m.SetHTTPBootURI(uri))
	assert.Equal(t, "http://user:xxxxx@"+srv.Listener.Addr().String()+"/a.iso", m.GetHTTPBootURI())
	require.NotEmpty(t, httpBootPath(t, m))
}

func TestFindCDDrive_SkipsHTTPBootDrive(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	qmpMock.blocks = []qmp.BlockInfo{
		{Device: "httpboot0", QDev: "/machine/peripheral/httpboot-cd0", Removable: true,
			Inserted: &qmp.BlockInserted{File: "/vm/vmedia/httpboot-1.iso"}},
		{Device: "vmedia0", QDev: "vmedia-cd0", Removable: true},
	}
	m := NewWithProcess(qmpMock, newMockProcessManager(true))
	id, file, err := m.findCDDrive()
	require.NoError(t, err)
	assert.Equal(t, "vmedia-cd0", id)
	assert.Equal(t, "", file)
}
