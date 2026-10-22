package redfish

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/machine"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

// mockMachine implements MachineInterface for testing
type mockMachine struct {
	powerState    machine.PowerState
	qmpStatus     qmp.Status
	bootOverride  machine.BootOverride
	calls         []string
	lastMediaCred string
	lastMedia     string
	media         machine.Media
	mediaErr      error
	resetErr      error
	statusErr     error
	httpBootURI   string
	legacy        bool // HTTP boot unsupported
	secureBoot    *machine.SecureBoot // nil = Secure Boot unsupported
	secureBootErr error               // returned by SetSecureBootEnable(true)
}

func newMockMachine(status qmp.Status) *mockMachine {
	var ps machine.PowerState
	switch status {
	case qmp.StatusRunning:
		ps = machine.PowerOn
	default:
		ps = machine.PowerOff
	}
	return &mockMachine{
		powerState: ps,
		qmpStatus:  status,
		bootOverride: machine.BootOverride{
			Enabled: "Disabled",
			Target:  "None",
			Mode:    "UEFI",
		},
	}
}

func (m *mockMachine) GetPowerState() (machine.PowerState, error) {
	return m.powerState, nil
}

func (m *mockMachine) GetQMPStatus() (qmp.Status, error) {
	return m.qmpStatus, m.statusErr
}

func (m *mockMachine) Reset(resetType string) error {
	m.calls = append(m.calls, resetType)
	return m.resetErr
}

func (m *mockMachine) GetHTTPBootURI() string {
	return m.httpBootURI
}

func (m *mockMachine) SetHTTPBootURI(uri string) error {
	if err := machine.ValidateHTTPBootURI(uri); err != nil {
		return err
	}
	if uri != "" && m.legacy {
		return machine.ErrHTTPBootUnsupported
	}
	m.calls = append(m.calls, "SetHTTPBootURI:"+uri)
	m.httpBootURI = uri
	return nil
}

func (m *mockMachine) HTTPBootSupported() bool {
	return !m.legacy
}

func (m *mockMachine) GetBootOverride() machine.BootOverride {
	return m.bootOverride
}

func (m *mockMachine) SetBootOverride(override machine.BootOverride) error {
	// Validate target
	validTargets := map[string]bool{
		"None": true, "Pxe": true, "Hdd": true, "Cd": true, "BiosSetup": true, "UefiHttp": true,
	}
	if !validTargets[override.Target] {
		return fmt.Errorf("invalid boot target: %s", override.Target)
	}
	if override.Target == "UefiHttp" && m.legacy {
		return machine.ErrHTTPBootUnsupported
	}

	// Validate enabled
	validEnabled := map[string]bool{
		"Disabled": true, "Once": true, "Continuous": true,
	}
	if !validEnabled[override.Enabled] {
		return fmt.Errorf("invalid boot enabled: %s", override.Enabled)
	}

	m.bootOverride = override
	return nil
}

func (m *mockMachine) GetMedia() machine.Media {
	return m.media
}

func (m *mockMachine) InsertMedia(image, username, password string) error {
	m.lastMedia = image
	m.lastMediaCred = username + ":" + password
	m.calls = append(m.calls, "InsertMedia")
	if m.mediaErr != nil {
		return m.mediaErr
	}
	m.media = machine.Media{Image: image, Inserted: true}
	return nil
}

func (m *mockMachine) EjectMedia() error {
	m.lastMedia = ""
	m.calls = append(m.calls, "EjectMedia")
	if m.mediaErr != nil {
		return m.mediaErr
	}
	m.media = machine.Media{}
	return nil
}

func (m *mockMachine) SecureBootSupported() bool {
	return m.secureBoot != nil
}

func (m *mockMachine) GetSecureBoot() machine.SecureBoot {
	if m.secureBoot == nil {
		return machine.SecureBoot{}
	}
	return *m.secureBoot
}

func (m *mockMachine) SetSecureBootEnable(enable bool) error {
	if m.secureBoot == nil {
		return machine.ErrSecureBootUnsupported
	}
	if enable && m.secureBootErr != nil {
		return m.secureBootErr
	}
	m.calls = append(m.calls, fmt.Sprintf("SetSecureBootEnable:%t", enable))
	m.secureBoot.Enable = enable
	return nil
}

func (m *mockMachine) ResetSecureBootKeys() error {
	if m.secureBoot == nil {
		return machine.ErrSecureBootUnsupported
	}
	m.calls = append(m.calls, "ResetSecureBootKeys")
	return nil
}

func (m *mockMachine) Calls() []string {
	return m.calls
}

func (m *mockMachine) LastInsertedMedia() string {
	return m.lastMedia
}

func TestServiceRoot(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var root ServiceRoot
	err := json.Unmarshal(w.Body.Bytes(), &root)
	require.NoError(t, err)

	assert.Equal(t, "#ServiceRoot.v1_5_0.ServiceRoot", root.ODataType)
	assert.Equal(t, "/redfish/v1", root.ODataID)
	assert.Equal(t, "RootService", root.ID)
	assert.Equal(t, "Root Service", root.Name)
	assert.Equal(t, "1.0.0", root.RedfishVersion)
	assert.Equal(t, "/redfish/v1/Systems", root.Systems.ODataID)
	assert.Equal(t, "/redfish/v1/Managers", root.Managers.ODataID)
	assert.Equal(t, "/redfish/v1/Chassis", root.Chassis.ODataID)
}
