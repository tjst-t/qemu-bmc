package ipmi

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/bmc"
	"github.com/tjst-t/qemu-bmc/internal/fault"
	"github.com/tjst-t/qemu-bmc/internal/machine"
)

func newFaultServer(t *testing.T, faults ...fault.Fault) (*Server, *ipmiMockMachine, *fault.Store) {
	t.Helper()
	store := fault.NewStore()
	for _, f := range faults {
		_, err := store.Add(f)
		require.NoError(t, err)
	}
	mock := newIPMIMockMachine(machine.PowerOn)
	server := NewServer(mock, bmc.NewState("admin", "password"), "admin", "password")
	server.SetFaults(store)
	return server, mock, store
}

func ipmi15Request(netFn, cmd uint8, data []byte) []byte {
	return SerializeRMCPMessage(RMCPClassIPMI, buildTestSessionWrapper(buildTestIPMIRequest(netFn, cmd, data)))
}

// completionCode returns the completion code of an IPMI 1.5 response
// without authentication.
func completionCode(t *testing.T, resp []byte) CompletionCode {
	t.Helper()
	_, msg, err := ParseIPMI15Message(resp[4:])
	require.NoError(t, err)
	// Response: NetFn, ..., Cmd, then the completion code as first data byte
	require.NotEmpty(t, msg.Data)
	return CompletionCode(msg.Data[0])
}

func byteP(b uint8) *fault.Byte { v := fault.Byte(b); return &v }

func TestFault_IPMIDrop(t *testing.T) {
	server, _, store := newFaultServer(t, fault.Fault{Kind: fault.IPMIDrop})

	_, err := server.HandleMessage(ipmi15Request(NetFnChassis, CmdGetChassisStatus, nil))
	assert.ErrorIs(t, err, errNoResponse)

	// Session setup is not affected without includeSession
	resp, err := server.HandleMessage(ipmi15Request(NetFnApp, CmdGetChannelAuthCapabilities, []byte{0x0e, 0x04}))
	require.NoError(t, err)
	assert.NotNil(t, resp)

	store.Clear()
	resp, err = server.HandleMessage(ipmi15Request(NetFnChassis, CmdGetChassisStatus, nil))
	require.NoError(t, err)
	assert.Equal(t, CompletionCodeOK, completionCode(t, resp))
}

func TestFault_IPMIDrop_IncludeSession(t *testing.T) {
	server, _, _ := newFaultServer(t, fault.Fault{Kind: fault.IPMIDrop, IncludeSession: true})

	_, err := server.HandleMessage(ipmi15Request(NetFnApp, CmdGetChannelAuthCapabilities, []byte{0x0e, 0x04}))
	assert.ErrorIs(t, err, errNoResponse)

	// RMCP+ Open Session
	openSession := SerializeRMCPMessage(RMCPClassIPMI, wrapRMCPPlusPayload(PayloadTypeOpenSessionRequest, 0, 0, buildOpenSessionRequest(1, 0xA0A2A3A4)))
	_, err = server.HandleMessage(openSession)
	assert.ErrorIs(t, err, errNoResponse)
	// (without the fault it is answered)
	resp, err := NewServer(newIPMIMockMachine(machine.PowerOn), bmc.NewState("admin", "password"), "admin", "password").HandleMessage(openSession)
	require.NoError(t, err)
	assert.NotNil(t, resp)

	// ASF presence ping
	ping := []byte{RMCPVersion1, 0, 0xFF, RMCPClassASF, 0, 0, 0x11, 0xBE, 0x80, 0x01, 0, 0}
	_, err = server.HandleMessage(ping)
	assert.ErrorIs(t, err, errNoResponse)
}

func TestFault_IPMIError(t *testing.T) {
	server, mock, _ := newFaultServer(t, fault.Fault{
		Kind: fault.IPMIError, NetFn: byteP(NetFnChassis), Cmd: byteP(CmdChassisControl), CompletionCode: byteP(0xC0),
	})

	resp, err := server.HandleMessage(ipmi15Request(NetFnChassis, CmdChassisControl, []byte{ChassisControlPowerDown}))
	require.NoError(t, err)
	assert.Equal(t, CompletionCodeNodeBusy, completionCode(t, resp))
	assert.Empty(t, mock.calls, "the command is not run")

	resp, err = server.HandleMessage(ipmi15Request(NetFnChassis, CmdGetChassisStatus, nil))
	require.NoError(t, err)
	assert.Equal(t, CompletionCodeOK, completionCode(t, resp))
}

func TestFault_IPMIDelay_Serve(t *testing.T) {
	count := 1
	server, _, _ := newFaultServer(t, fault.Fault{Kind: fault.IPMIDelay, DelayMs: 300, Count: &count})
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	go server.Serve(conn)
	defer server.Close()

	client, err := net.Dial("udp", conn.LocalAddr().String())
	require.NoError(t, err)
	defer client.Close()

	buf := make([]byte, 1024)
	start := time.Now()
	_, err = client.Write(ipmi15Request(NetFnChassis, CmdGetChassisStatus, nil))
	require.NoError(t, err)
	// The next request is answered at once, before the delayed one
	_, err = client.Write(ipmi15Request(NetFnApp, CmdGetDeviceID, nil))
	require.NoError(t, err)

	client.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := client.Read(buf)
	require.NoError(t, err)
	_, msg, err := ParseIPMI15Message(buf[4:n])
	require.NoError(t, err)
	assert.Equal(t, uint8(CmdGetDeviceID), msg.Command)
	assert.Less(t, time.Since(start), 300*time.Millisecond)

	n, err = client.Read(buf)
	require.NoError(t, err)
	_, msg, err = ParseIPMI15Message(buf[4:n])
	require.NoError(t, err)
	assert.Equal(t, uint8(CmdGetChassisStatus), msg.Command)
	assert.GreaterOrEqual(t, time.Since(start), 300*time.Millisecond)
}

func TestFault_PowerActionFail_CompletionCode(t *testing.T) {
	store := fault.NewStore()
	_, err := store.Add(fault.Fault{Kind: fault.PowerActionFail, Action: "On", CompletionCode: byteP(0xD5)})
	require.NoError(t, err)
	mock := newIPMIMockMachine(machine.PowerOff)
	m := faultResetMachine{mock, store}

	code, _ := handleChassisControl([]byte{ChassisControlPowerUp}, m)
	assert.Equal(t, CompletionCodeNotInPresentState, code)
	assert.Empty(t, mock.calls)

	code, _ = handleChassisControl([]byte{ChassisControlPowerDown}, m)
	assert.Equal(t, CompletionCodeOK, code)
	assert.Equal(t, []string{"ForceOff"}, mock.calls)
}

// faultResetMachine runs Reset through the power faults, as main does.
type faultResetMachine struct {
	*ipmiMockMachine
	faults *fault.Store
}

func (f faultResetMachine) Reset(resetType string) error {
	return f.faults.Reset(resetType, f.ipmiMockMachine.Reset)
}
