package ipmi

import (
	"bytes"
	"log"
	"net"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/fault"
	"github.com/tjst-t/qemu-bmc/internal/machine"
)

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureActions captures the log until the end of the test; the returned
// function returns the action lines logged so far (without timestamps).
func captureActions(t *testing.T) func() []string {
	t.Helper()
	var buf syncBuffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(out)
		log.SetFlags(flags)
	})
	return func() []string {
		var lines []string
		for _, l := range strings.Split(buf.String(), "\n") {
			if strings.HasPrefix(l, "action: ") {
				lines = append(lines, l)
			}
		}
		return lines
	}
}

var testConsole = &net.UDPAddr{IP: net.ParseIP("192.0.2.7"), Port: 50000}

func TestActionLog_ChassisControl_OneLinePerCommand(t *testing.T) {
	actions := captureActions(t)
	server, mock, _ := newFaultServer(t)

	for _, c := range []struct {
		control byte
		code    CompletionCode
	}{
		{ChassisControlPowerDown, CompletionCodeOK},
		{ChassisControlPowerUp, CompletionCodeOK},
		{ChassisControlPowerCycle, CompletionCodeOK},
		{ChassisControlHardReset, CompletionCodeOK},
		{ChassisControlPulse, CompletionCodeOK},
		{ChassisControlSoftOff, CompletionCodeOK},
		{ChassisControlPulse, CompletionCodeNotInPresentState}, // off: no NMI
		{0x07, CompletionCodeInvalidField},
	} {
		resp, _, err := server.handleMessage(ipmi15Request(NetFnChassis, CmdChassisControl, []byte{c.control}), testConsole)
		require.NoError(t, err)
		require.Equal(t, c.code, completionCode(t, resp))
	}

	assert.Equal(t, []string{
		"action: kind=power via=ipmi remote=192.0.2.7 user=- reset_type=ForceOff result=accepted",
		"action: kind=power via=ipmi remote=192.0.2.7 user=- reset_type=On result=accepted",
		"action: kind=power via=ipmi remote=192.0.2.7 user=- reset_type=PowerCycle result=accepted",
		"action: kind=power via=ipmi remote=192.0.2.7 user=- reset_type=ForceRestart result=accepted",
		"action: kind=power via=ipmi remote=192.0.2.7 user=- reset_type=Nmi result=accepted",
		"action: kind=power via=ipmi remote=192.0.2.7 user=- reset_type=GracefulShutdown result=accepted",
		"action: kind=power via=ipmi remote=192.0.2.7 user=- reset_type=Nmi result=rejected cc=0xd5",
		"action: kind=power via=ipmi remote=192.0.2.7 user=- reset_type=0x07 result=rejected cc=0xcc",
	}, actions())
	assert.Equal(t, []string{"ForceOff", "On", "PowerCycle", "ForceRestart", "Nmi", "GracefulShutdown", "Nmi"}, mock.calls)
}

func TestActionLog_ReadsLogNothing(t *testing.T) {
	actions := captureActions(t)
	server, _, _ := newFaultServer(t)
	for _, req := range [][]byte{
		ipmi15Request(NetFnChassis, CmdGetChassisStatus, nil),
		ipmi15Request(NetFnChassis, CmdGetBootOptions, []byte{0x05, 0x00, 0x00}),
		ipmi15Request(NetFnApp, CmdGetChannelAuthCapabilities, []byte{0x0e, 0x04}),
		// Set Boot Options, other parameters than the boot flags
		ipmi15Request(NetFnChassis, CmdSetBootOptions, []byte{0x00, 0x01}),
	} {
		_, _, err := server.handleMessage(req, testConsole)
		require.NoError(t, err)
	}
	assert.Empty(t, actions())
}

func TestActionLog_SetBootFlags(t *testing.T) {
	actions := captureActions(t)
	server, _, _ := newFaultServer(t)

	for _, flags := range [][]byte{
		{0x05, 0xA0, 0x04, 0x00, 0x00, 0x00}, // Once, UEFI, PXE
		{0x05, 0xC0, 0x14, 0x00, 0x00, 0x00}, // Continuous, Legacy, CD
	} {
		resp, _, err := server.handleMessage(ipmi15Request(NetFnChassis, CmdSetBootOptions, flags), testConsole)
		require.NoError(t, err)
		require.Equal(t, CompletionCodeOK, completionCode(t, resp))
	}
	assert.Equal(t, []string{
		"action: kind=boot via=ipmi remote=192.0.2.7 user=- enabled=Once target=Pxe mode=UEFI result=accepted",
		"action: kind=boot via=ipmi remote=192.0.2.7 user=- enabled=Continuous target=Cd mode=Legacy result=accepted",
	}, actions())
}

func TestActionLog_Faults(t *testing.T) {
	actions := captureActions(t)
	server, mock, store := newFaultServer(t)
	powerDown := ipmi15Request(NetFnChassis, CmdChassisControl, []byte{ChassisControlPowerDown})

	// IPMI-level faults: the command never runs
	drop, err := store.Add(fault.Fault{Kind: fault.IPMIDrop, NetFn: byteP(NetFnChassis), Cmd: byteP(CmdChassisControl), Count: intP(1)})
	require.NoError(t, err)
	_, _, err = server.handleMessage(powerDown, testConsole)
	require.ErrorIs(t, err, errNoResponse)

	ipmiErr, err := store.Add(fault.Fault{Kind: fault.IPMIError, CompletionCode: byteP(0xC3), Count: intP(1)})
	require.NoError(t, err)
	resp, _, err := server.handleMessage(powerDown, testConsole)
	require.NoError(t, err)
	assert.Equal(t, CompletionCode(0xC3), completionCode(t, resp))
	assert.Empty(t, mock.calls)

	assert.Equal(t, []string{
		"action: kind=power via=ipmi remote=192.0.2.7 user=- reset_type=ForceOff result=fault fault=" + drop.ID,
		"action: kind=power via=ipmi remote=192.0.2.7 user=- reset_type=ForceOff result=fault cc=0xc3 fault=" + ipmiErr.ID,
	}, actions())
}

// powerFaultMachine runs the power actions through the power-action-* faults
type powerFaultMachine struct {
	*ipmiMockMachine
	faults *fault.Store
}

func (m powerFaultMachine) Reset(resetType string) error {
	return m.faults.Reset(resetType, m.ipmiMockMachine.Reset)
}

func TestActionLog_PowerActionFaults(t *testing.T) {
	actions := captureActions(t)
	store := fault.NewStore()
	mock := newIPMIMockMachine(machine.PowerOn)
	server, _, _ := newFaultServer(t)
	server.machine = powerFaultMachine{mock, store}

	ignored, err := store.Add(fault.Fault{Kind: fault.PowerActionIgnored, Count: intP(1)})
	require.NoError(t, err)
	resp, _, err := server.handleMessage(ipmi15Request(NetFnChassis, CmdChassisControl, []byte{ChassisControlPowerDown}), testConsole)
	require.NoError(t, err)
	assert.Equal(t, CompletionCodeOK, completionCode(t, resp))

	failed, err := store.Add(fault.Fault{Kind: fault.PowerActionFail, CompletionCode: byteP(0xD5), Count: intP(1)})
	require.NoError(t, err)
	resp, _, err = server.handleMessage(ipmi15Request(NetFnChassis, CmdChassisControl, []byte{ChassisControlHardReset}), testConsole)
	require.NoError(t, err)
	assert.Equal(t, CompletionCode(0xD5), completionCode(t, resp))
	assert.Empty(t, mock.calls)

	assert.Equal(t, []string{
		"action: kind=power via=ipmi remote=192.0.2.7 user=- reset_type=ForceOff result=fault fault=" + ignored.ID,
		"action: kind=power via=ipmi remote=192.0.2.7 user=- reset_type=ForceRestart result=fault cc=0xd5 fault=" + failed.ID,
	}, actions())
}

func TestActionLog_VMServer(t *testing.T) {
	actions := captureActions(t)
	clientConn, _, waitFn := vmTestHelper(t, machine.PowerOn)
	vmDoHandshake(t, clientConn)

	_, err := clientConn.Write(vmBuildIPMIRequestFrame(0x03, NetFnChassis, 0x00, CmdChassisControl, []byte{ChassisControlHardReset}))
	require.NoError(t, err)
	vmReadResponse(t, clientConn)
	_, err = clientConn.Write(vmBuildIPMIRequestFrame(0x04, NetFnChassis, 0x00, CmdGetChassisStatus, nil))
	require.NoError(t, err)
	vmReadResponse(t, clientConn)
	clientConn.Close()
	require.NoError(t, waitFn())

	lines := actions()
	require.Len(t, lines, 1)
	assert.True(t, strings.HasPrefix(lines[0], "action: kind=power via=ipmi-vm remote="), lines[0])
	assert.True(t, strings.HasSuffix(lines[0], " user=- reset_type=ForceRestart result=accepted"), lines[0])
}

func TestSessionUser(t *testing.T) {
	assert.Equal(t, "", sessionUser(nil))
	assert.Equal(t, "admin", sessionUser(&Session{UserName: []byte("admin")}))
}
func intP(n int) *int { return &n }
