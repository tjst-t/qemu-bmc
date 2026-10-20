package redfish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

func newEthernetTestServer(status qmp.Status, macs []string) *Server {
	srv := NewServer(newMockMachine(status), "", "", "")
	srv.SetSystemInfo(SystemInfo{MACAddresses: macs})
	return srv
}

func getJSON(t *testing.T, srv *Server, path string, v interface{}) int {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), v))
	}
	return w.Code
}

func TestGetSystem_EthernetInterfacesLink(t *testing.T) {
	srv := newEthernetTestServer(qmp.StatusRunning, nil)
	var system ComputerSystem
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1", &system))
	assert.Equal(t, "/redfish/v1/Systems/1/EthernetInterfaces", system.EthernetInterfaces.ODataID)
}

func TestGetEthernetInterfaceCollection(t *testing.T) {
	srv := newEthernetTestServer(qmp.StatusRunning, []string{"52:54:00:11:22:33", "52:54:00:44:55:66"})

	for _, path := range []string{"/redfish/v1/Systems/1/EthernetInterfaces", "/redfish/v1/Systems/1/EthernetInterfaces/"} {
		var col EthernetInterfaceCollection
		require.Equal(t, http.StatusOK, getJSON(t, srv, path, &col), path)
		assert.Equal(t, "#EthernetInterfaceCollection.EthernetInterfaceCollection", col.ODataType)
		assert.Equal(t, "/redfish/v1/Systems/1/EthernetInterfaces", col.ODataID)
		assert.Equal(t, 2, col.MembersCount)
		assert.Equal(t, []ODataID{
			{ODataID: "/redfish/v1/Systems/1/EthernetInterfaces/1"},
			{ODataID: "/redfish/v1/Systems/1/EthernetInterfaces/2"},
		}, col.Members)
	}
}

func TestGetEthernetInterfaceCollection_Empty(t *testing.T) {
	srv := newEthernetTestServer(qmp.StatusRunning, nil)
	var col EthernetInterfaceCollection
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1/EthernetInterfaces", &col))
	assert.Equal(t, 0, col.MembersCount)
	assert.NotNil(t, col.Members)
}

func TestGetEthernetInterface(t *testing.T) {
	tests := []struct {
		status     qmp.Status
		linkStatus string
	}{
		{qmp.StatusRunning, "LinkUp"},
		{qmp.StatusShutdown, "LinkDown"},
	}
	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			srv := newEthernetTestServer(tt.status, []string{"52:54:00:11:22:33", "52:54:00:44:55:66"})
			for _, path := range []string{"/redfish/v1/Systems/1/EthernetInterfaces/2", "/redfish/v1/Systems/1/EthernetInterfaces/2/"} {
				var nic EthernetInterface
				require.Equal(t, http.StatusOK, getJSON(t, srv, path, &nic), path)
				assert.Equal(t, "#EthernetInterface.v1_4_0.EthernetInterface", nic.ODataType)
				assert.Equal(t, "/redfish/v1/Systems/1/EthernetInterfaces/2", nic.ODataID)
				assert.Equal(t, "2", nic.ID)
				assert.Equal(t, "52:54:00:44:55:66", nic.MACAddress)
				assert.Equal(t, "52:54:00:44:55:66", nic.PermanentMACAddress)
				assert.Equal(t, tt.linkStatus, nic.LinkStatus)
				assert.Equal(t, "Enabled", nic.Status.State)
			}
		})
	}
}

func TestGetEthernetInterface_NotFound(t *testing.T) {
	srv := newEthernetTestServer(qmp.StatusRunning, []string{"52:54:00:11:22:33"})
	for _, id := range []string{"0", "2", "abc"} {
		var nic EthernetInterface
		assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/redfish/v1/Systems/1/EthernetInterfaces/"+id, &nic), id)
	}
}
