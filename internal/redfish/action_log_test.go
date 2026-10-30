package redfish

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/fault"
	"github.com/tjst-t/qemu-bmc/internal/machine"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

// syncBuffer is a bytes.Buffer safe for the log package and the test.
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

const testRemote = "remote=192.0.2.1 user=-" // httptest.NewRequest's RemoteAddr, no auth

func TestActionLog_Reset_OneLinePerCommand(t *testing.T) {
	actions := captureActions(t)
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	path := "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset"

	for _, rt := range []string{"ForceOff", "On", "GracefulRestart"} {
		require.Equal(t, http.StatusNoContent, serve(srv, "POST", path, `{"ResetType":"`+rt+`"}`).Code)
	}
	assert.Equal(t, []string{
		"action: kind=power via=redfish " + testRemote + " reset_type=ForceOff result=accepted",
		"action: kind=power via=redfish " + testRemote + " reset_type=On result=accepted",
		"action: kind=power via=redfish " + testRemote + " reset_type=GracefulRestart result=accepted",
	}, actions())
}

func TestActionLog_Reset_Rejected(t *testing.T) {
	actions := captureActions(t)
	m := newMockMachine(qmp.StatusRunning)
	srv := NewServer(m, "", "", "")
	path := "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset"

	assert.Equal(t, http.StatusBadRequest, serve(srv, "POST", path, `{"ResetType":"Bogus"}`).Code)
	assert.Equal(t, http.StatusBadRequest, serve(srv, "POST", path, `not json`).Code)
	m.resetErr = machine.ErrPoweredOff
	assert.Equal(t, http.StatusConflict, serve(srv, "POST", path, `{"ResetType":"Nmi"}`).Code)
	assert.Equal(t, []string{
		"action: kind=power via=redfish " + testRemote + " reset_type=Bogus result=rejected status=400 reason=ActionParameterNotSupported",
		"action: kind=power via=redfish " + testRemote + " reset_type=- result=rejected status=400 reason=MalformedJSON",
		"action: kind=power via=redfish " + testRemote + " reset_type=Nmi result=rejected status=409 reason=ActionNotSupported",
	}, actions())
}

func TestActionLog_Reset_PowerActionFaults(t *testing.T) {
	actions := captureActions(t)
	m := newMockMachine(qmp.StatusRunning)
	srv := NewServer(m, "", "", "")
	path := "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset"

	m.resetErr = &fault.PowerActionIgnoredError{FaultID: "3", Action: "ForceOff"}
	assert.Equal(t, http.StatusNoContent, serve(srv, "POST", path, `{"ResetType":"ForceOff"}`).Code)
	m.resetErr = &fault.PowerActionError{FaultID: "4", Action: "On", CompletionCode: 0xFF}
	assert.Equal(t, http.StatusInternalServerError, serve(srv, "POST", path, `{"ResetType":"On"}`).Code)
	assert.Equal(t, []string{
		"action: kind=power via=redfish " + testRemote + " reset_type=ForceOff result=fault fault=3",
		"action: kind=power via=redfish " + testRemote + " reset_type=On result=fault status=500 fault=4",
	}, actions())
}

func TestActionLog_ReadsLogNothing(t *testing.T) {
	actions := captureActions(t)
	srv, _ := newBiosServer(t)
	for _, p := range []string{
		"/redfish/v1", "/redfish/v1/Systems/1", "/redfish/v1/Managers/1",
		"/redfish/v1/Systems/1/VirtualMedia/CD1", "/redfish/v1/Systems/1/Bios",
		"/redfish/v1/Systems/1/Bios/Settings", "/redfish/v1/UpdateService",
	} {
		serve(srv, "GET", p, "")
	}
	assert.Empty(t, actions())
}

func TestActionLog_BootOverride(t *testing.T) {
	actions := captureActions(t)
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

	require.Equal(t, http.StatusOK, serve(srv, "PATCH", "/redfish/v1/Systems/1",
		`{"Boot":{"BootSourceOverrideEnabled":"Once","BootSourceOverrideTarget":"Cd"}}`).Code)
	require.Equal(t, http.StatusOK, serve(srv, "PATCH", "/redfish/v1/Systems/1",
		`{"Boot":{"BootSourceOverrideEnabled":"Continuous","BootSourceOverrideTarget":"UefiHttp","BootSourceOverrideMode":"UEFI","HttpBootUri":"http://user:secret@192.0.2.5/boot.efi"}}`).Code)
	require.Equal(t, http.StatusBadRequest, serve(srv, "PATCH", "/redfish/v1/Systems/1",
		`{"Boot":{"BootSourceOverrideTarget":"Floppy"}}`).Code)
	assert.Equal(t, []string{
		"action: kind=boot via=redfish " + testRemote + " enabled=Once target=Cd mode=- result=accepted",
		"action: kind=boot via=redfish " + testRemote + " enabled=Continuous target=UefiHttp mode=UEFI http_boot_uri=http://user:xxxxx@192.0.2.5/boot.efi result=accepted",
		"action: kind=boot via=redfish " + testRemote + " enabled=- target=Floppy mode=- result=rejected status=400 reason=PropertyValueError",
	}, actions())
}

func TestActionLog_VirtualMedia(t *testing.T) {
	actions := captureActions(t)
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	vm := "/redfish/v1/Systems/1/VirtualMedia/CD1/Actions/VirtualMedia."

	require.Equal(t, http.StatusNoContent, serve(srv, "POST", vm+"InsertMedia",
		`{"Image":"http://user:secret@192.0.2.5/boot.iso","Inserted":true}`).Code)
	require.Equal(t, http.StatusNoContent, serve(srv, "POST", vm+"EjectMedia", `{}`).Code)
	require.Equal(t, http.StatusBadRequest, serve(srv, "POST", vm+"InsertMedia", `{}`).Code)
	assert.Equal(t, []string{
		"action: kind=vmedia-insert via=redfish " + testRemote + " device=CD1 image=http://user:xxxxx@192.0.2.5/boot.iso result=accepted",
		"action: kind=vmedia-eject via=redfish " + testRemote + " device=CD1 result=accepted",
		"action: kind=vmedia-insert via=redfish " + testRemote + " device=CD1 image=- result=rejected status=400 reason=ActionParameterMissing",
	}, actions())
}

func TestActionLog_Bios(t *testing.T) {
	actions := captureActions(t)
	srv, _ := newBiosServer(t)

	require.Equal(t, http.StatusOK, serve(srv, "PATCH", "/redfish/v1/Systems/1/Bios/Settings",
		`{"Attributes":{"PowerProfile":"Balanced","NumLock":"Off"}}`).Code)
	require.Equal(t, http.StatusNoContent, serve(srv, "POST", "/redfish/v1/Systems/1/Bios/Actions/Bios.ResetBios", "").Code)
	require.Equal(t, http.StatusBadRequest, serve(srv, "PATCH", "/redfish/v1/Systems/1/Bios/Settings",
		`{"Attributes":{"Bogus":"x"}}`).Code)
	lines := actions()
	require.Len(t, lines, 3)
	assert.Equal(t, "action: kind=bios via=redfish "+testRemote+" op=settings attributes=NumLock,PowerProfile result=accepted", lines[0])
	assert.Equal(t, "action: kind=bios via=redfish "+testRemote+" op=reset result=accepted", lines[1])
	assert.True(t, strings.HasPrefix(lines[2], "action: kind=bios via=redfish "+testRemote+" op=settings attributes=Bogus result=rejected status=400 reason="), lines[2])
}

func TestActionLog_SecureBoot(t *testing.T) {
	actions := captureActions(t)
	srv, _ := newSecureBootServer(machine.SecureBoot{})

	require.Equal(t, http.StatusOK, serve(srv, "PATCH", "/redfish/v1/Systems/1/SecureBoot", `{"SecureBootEnable": true}`).Code)
	require.Equal(t, http.StatusNoContent, serve(srv, "POST", "/redfish/v1/Systems/1/SecureBoot/Actions/SecureBoot.ResetKeys",
		`{"ResetKeysType": "ResetAllKeysToDefault"}`).Code)
	assert.Equal(t, []string{
		"action: kind=secureboot via=redfish " + testRemote + " op=settings secure_boot_enable=true result=accepted",
		"action: kind=secureboot via=redfish " + testRemote + " op=reset-keys reset_keys_type=ResetAllKeysToDefault result=accepted",
	}, actions())
}

func TestActionLog_SimpleUpdate(t *testing.T) {
	actions := captureActions(t)
	srv, _ := newUpdateServer(t)
	img := imageServer(t)

	w := serve(srv, "POST", "/redfish/v1/UpdateService/Actions/UpdateService.SimpleUpdate",
		`{"ImageURI":"`+img.URL+`/QBMC-FW_BMC_2.0.0","TransferProtocol":"HTTP"}`)
	require.Equal(t, http.StatusAccepted, w.Code)
	w = serve(srv, "POST", "/redfish/v1/UpdateService/Actions/UpdateService.SimpleUpdate", `{}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	lines := actions()
	require.Len(t, lines, 2)
	assert.Regexp(t, `^action: kind=update via=redfish `+testRemote+` op=SimpleUpdate image=http://127\.0\.0\.1:\d+/QBMC-FW_BMC_2\.0\.0 target=BMC version=2\.0\.0 task=\d+ result=accepted$`, lines[0])
	assert.Equal(t, "action: kind=update via=redfish "+testRemote+" op=SimpleUpdate image=- result=rejected status=400 reason=ActionParameterMissing", lines[1])
}

func TestActionLog_MultipartPush(t *testing.T) {
	actions := captureActions(t)
	srv, _ := newUpdateServer(t)
	require.Equal(t, http.StatusAccepted, push(srv, "", "QBMC-FW BIOS 2.0.0\npayload", t).Code)
	lines := actions()
	require.Len(t, lines, 1)
	assert.Regexp(t, `^action: kind=update via=redfish `+testRemote+` op=MultipartPush image=fw\.img target=BIOS version=2\.0\.0 task=\d+ result=accepted$`, lines[0])
}

func TestActionLog_User(t *testing.T) {
	actions := captureActions(t)
	srv := NewServer(newMockMachine(qmp.StatusRunning), "admin", "password", "")
	path := "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset"

	req := httptest.NewRequest("POST", path, strings.NewReader(`{"ResetType":"On"}`))
	req.SetBasicAuth("admin", "password")
	srv.ServeHTTP(httptest.NewRecorder(), req)

	login := doReq(srv, "POST", sessionsPath, `{"UserName":"admin","Password":"password"}`, nil)
	require.Equal(t, http.StatusCreated, login.Code)
	w := doReq(srv, "POST", path, `{"ResetType":"ForceOff"}`, map[string]string{"X-Auth-Token": login.Header().Get("X-Auth-Token")})
	require.Equal(t, http.StatusNoContent, w.Code)

	// Not authenticated: never reaches the command, not logged
	assert.Equal(t, http.StatusUnauthorized, serve(srv, "POST", path, `{"ResetType":"On"}`).Code)

	assert.Equal(t, []string{
		"action: kind=power via=redfish remote=192.0.2.1 user=admin reset_type=On result=accepted",
		"action: kind=power via=redfish remote=192.0.2.1 user=admin reset_type=ForceOff result=accepted",
	}, actions())
}

func TestActionLog_RedfishFaults(t *testing.T) {
	actions := captureActions(t)
	m := newMockMachine(qmp.StatusRunning)
	srv := NewServer(m, "", "", "")
	store := fault.NewStore()
	store.SetOnRedfishFault(func(r *http.Request, id string, kind fault.Kind) {
		srv.LogFaultedAction(r, id, string(kind))
	})
	h := store.Middleware(srv)
	f, err := store.Add(fault.Fault{Kind: fault.RedfishError, Status: 503, Path: "/redfish/v1/Systems"})
	require.NoError(t, err)

	do := func(method, path, body string) int {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w.Code
	}
	assert.Equal(t, http.StatusServiceUnavailable, do("POST", "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"ForceOff"}`))
	assert.Equal(t, http.StatusServiceUnavailable, do("GET", "/redfish/v1/Systems/1", ""))
	assert.Equal(t, http.StatusServiceUnavailable, do("POST", "/redfish/v1/Systems/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia", `{}`))
	store.Clear()
	assert.Equal(t, http.StatusNoContent, do("POST", "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"On"}`))

	assert.Equal(t, []string{
		"action: kind=power via=redfish " + testRemote + " reset_type=ForceOff result=fault fault=" + f.ID + " reason=redfish-error",
		"action: kind=vmedia-eject via=redfish " + testRemote + " device=CD1 result=fault fault=" + f.ID + " reason=redfish-error",
		"action: kind=power via=redfish " + testRemote + " reset_type=On result=accepted",
	}, actions())
	assert.Equal(t, []string{"On"}, m.calls)
}
