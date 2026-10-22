package redfish

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/machine"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

func TestGetManagerCollection(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Managers", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var col ManagerCollection
	err := json.Unmarshal(w.Body.Bytes(), &col)
	require.NoError(t, err)
	assert.Equal(t, 1, col.MembersCount)
	assert.Equal(t, "/redfish/v1/Managers/1", col.Members[0].ODataID)
}

func TestGetManager(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Managers/1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var mgr Manager
	err := json.Unmarshal(w.Body.Bytes(), &mgr)
	require.NoError(t, err)
	assert.Equal(t, "#Manager.v1_3_0.Manager", mgr.ODataType)
	assert.Equal(t, "BMC", mgr.ManagerType)
}

func TestGetManager_LinksStatusFirmware(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	srv.SetFirmwareVersion("0.9.0")

	mgr := getResource(t, srv, "/redfish/v1/Managers/1")
	assert.Equal(t, "BMC", mgr["ManagerType"])
	assert.Equal(t, "0.9.0", mgr["FirmwareVersion"])
	assert.Equal(t, map[string]interface{}{"State": "Enabled", "Health": "OK"}, mgr["Status"])
	assert.Equal(t, map[string]interface{}{
		"ManagerForServers": []interface{}{map[string]interface{}{"@odata.id": "/redfish/v1/Systems/1"}},
		"ManagerInChassis":  map[string]interface{}{"@odata.id": "/redfish/v1/Chassis/1"},
	}, mgr["Links"])
}

func TestGetManager_FirmwareVersionOmittedWhenUnset(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

	mgr := getResource(t, srv, "/redfish/v1/Managers/1")
	assert.NotContains(t, mgr, "FirmwareVersion")
}

func TestGetVirtualMediaCollection(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	req := httptest.NewRequest("GET", "/redfish/v1/Managers/1/VirtualMedia", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	var col VirtualMediaCollection
	err := json.Unmarshal(w.Body.Bytes(), &col)
	require.NoError(t, err)
	assert.Equal(t, 1, col.MembersCount)
}

func getVirtualMedia(t *testing.T, srv *Server) map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest("GET", "/redfish/v1/Managers/1/VirtualMedia/CD1", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var vm map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &vm))
	return vm
}

func postMediaAction(srv *Server, action, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST",
		"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia."+action,
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func TestGetVirtualMedia_NotInserted(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

	vm := getVirtualMedia(t, srv)
	assert.Equal(t, "#VirtualMedia.v1_2_0.VirtualMedia", vm["@odata.type"])
	assert.Equal(t, "/redfish/v1/Managers/1/VirtualMedia/CD1", vm["@odata.id"])
	assert.NotEmpty(t, vm["@odata.context"])
	assert.Equal(t, false, vm["Inserted"])
	assert.Equal(t, "", vm["Image"])
	assert.NotContains(t, vm, "ImageName")
	assert.Equal(t, "NotConnected", vm["ConnectedVia"])
	assert.Equal(t, true, vm["WriteProtected"])
	assert.Equal(t, []interface{}{"CD", "DVD"}, vm["MediaTypes"])
}

func TestGetVirtualMedia_ReadsMachineState(t *testing.T) {
	mock := newMockMachine(qmp.StatusShutdown)
	mock.media = machine.Media{Image: "file:///iso/debian.iso", Inserted: true}
	srv := NewServer(mock, "", "", "")

	vm := getVirtualMedia(t, srv)
	assert.Equal(t, true, vm["Inserted"])
	assert.Equal(t, "file:///iso/debian.iso", vm["Image"])
	assert.Equal(t, "debian.iso", vm["ImageName"])
	assert.Equal(t, "URI", vm["ConnectedVia"])
	assert.Equal(t, true, vm["WriteProtected"])
}

func TestInsertVirtualMedia(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	w := postMediaAction(srv, "InsertMedia", `{"Image": "/iso/boot.iso", "Inserted": true}`)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Empty(t, w.Body.Bytes())
	assert.Equal(t, "/iso/boot.iso", mock.LastInsertedMedia())
}

func TestInsertVirtualMedia_URLWithCredentials(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	w := postMediaAction(srv, "InsertMedia",
		`{"Image": "https://example.com/boot.iso", "Inserted": true, "UserName": "user", "Password": "secret", "TransferProtocolType": "HTTPS"}`)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Equal(t, "https://example.com/boot.iso", mock.LastInsertedMedia())
	assert.Equal(t, "user:secret", mock.lastMediaCred)
}

func TestInsertVirtualMedia_TrailingSlash(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

	w := postMediaAction(srv, "InsertMedia/", `{"Image": "/iso/boot.iso"}`)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestInsertVirtualMedia_EmptyImage(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	srv := NewServer(mock, "", "", "")

	w := postMediaAction(srv, "InsertMedia", `{"Image": "", "Inserted": true}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.NotContains(t, mock.Calls(), "InsertMedia")
}

func TestInsertVirtualMedia_MalformedJSON(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

	w := postMediaAction(srv, "InsertMedia", `{`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestInsertVirtualMedia_InvalidMedia(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	mock.mediaErr = fmt.Errorf("%w: stat /iso/missing.iso: no such file or directory", machine.ErrInvalidMedia)
	srv := NewServer(mock, "", "", "")

	w := postMediaAction(srv, "InsertMedia", `{"Image": "/iso/missing.iso"}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "missing.iso")
	assert.False(t, getVirtualMedia(t, srv)["Inserted"].(bool))
}

func TestInsertVirtualMedia_QMPError(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	mock.mediaErr = errors.New("no removable CD drive found")
	srv := NewServer(mock, "", "", "")

	w := postMediaAction(srv, "InsertMedia", `{"Image": "/iso/boot.iso"}`)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestEjectVirtualMedia(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	mock.media = machine.Media{Image: "/iso/boot.iso", Inserted: true}
	srv := NewServer(mock, "", "", "")

	w := postMediaAction(srv, "EjectMedia", `{}`)
	assert.Equal(t, http.StatusNoContent, w.Code)
	assert.Contains(t, mock.Calls(), "EjectMedia")
	assert.False(t, getVirtualMedia(t, srv)["Inserted"].(bool))
}

func TestEjectVirtualMedia_NoBody(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

	req := httptest.NewRequest("POST",
		"/redfish/v1/Managers/1/VirtualMedia/CD1/Actions/VirtualMedia.EjectMedia", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestEjectVirtualMedia_Error(t *testing.T) {
	mock := newMockMachine(qmp.StatusRunning)
	mock.mediaErr = errors.New("QMP error")
	srv := NewServer(mock, "", "", "")

	w := postMediaAction(srv, "EjectMedia", `{}`)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestVirtualMedia_InsertThenGet(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

	w := postMediaAction(srv, "InsertMedia", `{"Image": "/iso/boot.iso", "Inserted": true}`)
	require.Equal(t, http.StatusNoContent, w.Code)

	vm := getVirtualMedia(t, srv)
	assert.Equal(t, true, vm["Inserted"])
	assert.Equal(t, "/iso/boot.iso", vm["Image"])
	assert.Equal(t, "boot.iso", vm["ImageName"])
}

const (
	systemsCD1  = "/redfish/v1/Systems/1/VirtualMedia/CD1"
	managersCD1 = "/redfish/v1/Managers/1/VirtualMedia/CD1"
)

func postMediaActionAt(srv *Server, cd, action, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", cd+"/Actions/VirtualMedia."+action, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func TestGetSystem_VirtualMediaLink(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

	sys := getResource(t, srv, "/redfish/v1/Systems/1")
	assert.Equal(t, map[string]interface{}{"@odata.id": "/redfish/v1/Systems/1/VirtualMedia"}, sys["VirtualMedia"])
}

func TestSystemsVirtualMediaCollection(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

	for _, p := range []string{"/redfish/v1/Systems/1/VirtualMedia", "/redfish/v1/Systems/1/VirtualMedia/"} {
		col := getResource(t, srv, p)
		assert.Equal(t, "#VirtualMediaCollection.VirtualMediaCollection", col["@odata.type"])
		assert.Equal(t, "/redfish/v1/Systems/1/VirtualMedia", col["@odata.id"])
		assert.Equal(t, float64(1), col["Members@odata.count"])
		assert.Equal(t, []interface{}{map[string]interface{}{"@odata.id": systemsCD1}}, col["Members"])
	}
}

func TestSystemsVirtualMedia_URIsFollowRequest(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

	for _, cd := range []string{systemsCD1, managersCD1} {
		for _, p := range []string{cd, cd + "/"} {
			vm := getResource(t, srv, p)
			assert.Equal(t, cd, vm["@odata.id"])
			assert.Equal(t, map[string]interface{}{
				"#VirtualMedia.InsertMedia": map[string]interface{}{"target": cd + "/Actions/VirtualMedia.InsertMedia"},
				"#VirtualMedia.EjectMedia":  map[string]interface{}{"target": cd + "/Actions/VirtualMedia.EjectMedia"},
			}, vm["Actions"])
		}
	}
}

func TestVirtualMedia_SharedBetweenSystemsAndManagers(t *testing.T) {
	cases := []struct{ name, insertAt, readAt string }{
		{"systems to managers", systemsCD1, managersCD1},
		{"managers to systems", managersCD1, systemsCD1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

			w := postMediaActionAt(srv, tc.insertAt, "InsertMedia", `{"Image": "/iso/boot.iso"}`)
			require.Equal(t, http.StatusNoContent, w.Code)

			vm := getResource(t, srv, tc.readAt)
			assert.Equal(t, true, vm["Inserted"])
			assert.Equal(t, "/iso/boot.iso", vm["Image"])

			w = postMediaActionAt(srv, tc.readAt, "EjectMedia", `{}`)
			require.Equal(t, http.StatusNoContent, w.Code)
			vm = getResource(t, srv, tc.insertAt)
			assert.Equal(t, false, vm["Inserted"])
		})
	}
}

func TestSystemsVirtualMedia_TrailingSlashActions(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

	req := httptest.NewRequest("POST", systemsCD1+"/Actions/VirtualMedia.InsertMedia/", strings.NewReader(`{"Image": "/iso/a.iso"}`))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNoContent, w.Code)

	w = postMediaActionAt(srv, systemsCD1, "InsertMedia", `{}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}
