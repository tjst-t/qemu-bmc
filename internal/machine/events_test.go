package machine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

// nextEvent returns the next event of ch, failing the test after a second.
func nextEvent(t *testing.T, ch <-chan Event) Event {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(time.Second):
		t.Fatal("no event")
		return Event{}
	}
}

// powerEvents collects the power states of ch until none arrives for 100 ms.
func powerEvents(ch <-chan Event) []PowerState {
	var states []PowerState
	for {
		select {
		case e := <-ch:
			if e.Type == EventPowerState {
				states = append(states, e.PowerState)
			}
		case <-time.After(100 * time.Millisecond):
			return states
		}
	}
}

func TestEvents_PowerOffOn_Legacy(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)
	ch, cancel := m.SubscribeEvents()
	defer cancel()

	require.NoError(t, m.Reset("ForceOff"))
	assert.Equal(t, Event{Type: EventPowerState, PowerState: PowerOff}, nextEvent(t, ch))
	require.NoError(t, m.Reset("On"))
	assert.Equal(t, Event{Type: EventPowerState, PowerState: PowerOn}, nextEvent(t, ch))
	// No change, no event
	require.NoError(t, m.Reset("On"))
	assert.Empty(t, powerEvents(ch))
}

func TestEvents_PowerCycle_ProcessMode_OffThenOn(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)
	require.NoError(t, m.Reset("On"))

	ch, cancel := m.SubscribeEvents()
	defer cancel()
	go func() {
		assert.Eventually(t, func() bool {
			calls := qmpMock.Calls()
			return len(calls) > 0 && calls[len(calls)-1] == "Quit"
		}, 2*time.Second, time.Millisecond)
		qmpMock.setStatus(qmp.StatusRunning) // the new QEMU runs
		pm.exit()
	}()
	require.NoError(t, m.Reset("PowerCycle"))
	assert.Equal(t, []PowerState{PowerOff, PowerOn}, powerEvents(ch))
}

func TestEvents_QEMUExits_ReportedByPolling(t *testing.T) {
	qmpMock := newMockQMPClient(qmp.StatusRunning)
	pm := newMockProcessManager(false)
	m := NewWithProcess(qmpMock, pm)
	m.powerPoll = 10 * time.Millisecond
	require.NoError(t, m.Reset("On"))
	ch, cancel := m.SubscribeEvents()
	defer cancel()

	pm.exit() // QEMU crashed or the guest powered off
	assert.Equal(t, Event{Type: EventPowerState, PowerState: PowerOff}, nextEvent(t, ch))
}

func TestEvents_GracefulRestart(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 5*time.Second)
	ch, cancel := m.SubscribeEvents()
	defer cancel()

	require.NoError(t, m.Reset("GracefulRestart"))
	assert.Equal(t, Event{Type: EventPowerState, PowerState: PoweringOff}, nextEvent(t, ch))
	mock.setStatus(qmp.StatusShutdown)
	assert.Equal(t, []PowerState{PoweringOn, PowerOn}, powerEvents(ch))
}

func TestEvents_GracefulShutdownEndedByForceOff_NoOn(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := newFastMachine(New(mock), 5*time.Second)
	ch, cancel := m.SubscribeEvents()
	defer cancel()

	require.NoError(t, m.Reset("GracefulShutdown"))
	require.NoError(t, m.Reset("ForceOff"))
	assert.Equal(t, []PowerState{PoweringOff, PowerOff}, powerEvents(ch))
}

func TestEvents_ResourceChanged(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	mock.blocks = []qmp.BlockInfo{{Device: "ide2-cd0", QDev: "cd0", Removable: true}}
	m := New(mock)
	ch, cancel := m.SubscribeEvents()
	defer cancel()

	require.NoError(t, m.SetBootOverride(BootOverride{Enabled: "Once", Target: "Pxe", Mode: "UEFI"}))
	assert.Equal(t, Event{Type: EventResourceChanged, Resource: ResourceBoot}, nextEvent(t, ch))
	require.NoError(t, m.InsertMedia("/tmp/x.iso", "", ""))
	assert.Equal(t, Event{Type: EventResourceChanged, Resource: ResourceVirtualMedia}, nextEvent(t, ch))
	require.NoError(t, m.EjectMedia())
	assert.Equal(t, Event{Type: EventResourceChanged, Resource: ResourceVirtualMedia}, nextEvent(t, ch))
	// Using the Once override changes the boot settings too
	m.ConsumeBootOnce()
	assert.Equal(t, Event{Type: EventResourceChanged, Resource: ResourceBoot}, nextEvent(t, ch))
}

func TestEvents_Unsubscribe_StopsEvents(t *testing.T) {
	mock := newMockQMPClient(qmp.StatusRunning)
	m := New(mock)
	ch, cancel := m.SubscribeEvents()
	ch2, cancel2 := m.SubscribeEvents()
	defer cancel2()
	cancel()
	cancel() // idempotent

	require.NoError(t, m.Reset("ForceOff"))
	assert.Equal(t, PowerOff, nextEvent(t, ch2).PowerState)
	select {
	case e := <-ch:
		t.Fatalf("event after unsubscribing: %+v", e)
	default:
	}
	assert.True(t, m.subscribed())
	cancel2()
	assert.False(t, m.subscribed())
}
