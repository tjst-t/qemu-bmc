package machine

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

func countBoots(m *Machine) *atomic.Int32 {
	var n atomic.Int32
	m.SetBootHook(func() { n.Add(1) })
	return &n
}

func TestBootHook_Legacy(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusPaused)
	m := New(mock)
	boots := countBoots(m)

	require.NoError(t, m.Reset("On"))
	assert.EqualValues(t, 1, boots.Load())
	require.NoError(t, m.Reset("On")) // already on
	assert.EqualValues(t, 1, boots.Load())
	require.NoError(t, m.Reset("ForceRestart"))
	assert.EqualValues(t, 2, boots.Load())
	require.NoError(t, m.Reset("PowerCycle"))
	assert.EqualValues(t, 3, boots.Load())
	require.NoError(t, m.Reset("Nmi"))
	require.NoError(t, m.Reset("GracefulShutdown"))
	require.NoError(t, m.Reset("ForceOff"))
	assert.EqualValues(t, 3, boots.Load(), "no boot without power on or reset")
}

func TestBootHook_Process(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)
	boots := countBoots(m)

	require.NoError(t, m.Reset("On"))
	assert.EqualValues(t, 1, boots.Load())
	require.NoError(t, m.Reset("ForceRestart")) // system_reset
	assert.EqualValues(t, 2, boots.Load())
	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Pxe", Mode: "UEFI"}))
	require.NoError(t, m.Reset("ForceRestart")) // QEMU restarted
	assert.EqualValues(t, 3, boots.Load())

	// QEMU went down by itself, then is powered on again
	pm.exit()
	require.NoError(t, m.Reset("On"))
	assert.EqualValues(t, 4, boots.Load())

	// Powering off is no boot
	require.NoError(t, m.Reset("GracefulShutdown"))
	assert.EqualValues(t, 4, boots.Load())
}

func TestBootHook_GracefulRestart(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 5*time.Second)
	boots := countBoots(m)

	require.NoError(t, m.Reset("GracefulRestart"))
	assert.EqualValues(t, 0, boots.Load())
	mock.setStatus(qmp.StatusShutdown)
	assert.Eventually(t, func() bool { return boots.Load() == 1 }, 3*time.Second, 10*time.Millisecond)
}
