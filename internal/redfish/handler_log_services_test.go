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

type fakeSerialLog string

func (f fakeSerialLog) Recent() []byte { return []byte(f) }

func getLogJSON(t *testing.T, srv *Server, path string) (int, map[string]interface{}) {
	t.Helper()
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	var body map[string]interface{}
	if w.Code == http.StatusOK {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	}
	return w.Code, body
}

func TestSystem_LinksLogServices(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	_, sys := getLogJSON(t, srv, "/redfish/v1/Systems/1")
	assert.Equal(t, map[string]interface{}{"@odata.id": "/redfish/v1/Systems/1/LogServices"}, sys["LogServices"])
}

func TestLogServices_WithoutSerialLog(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	code, coll := getLogJSON(t, srv, "/redfish/v1/Systems/1/LogServices")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "#LogServiceCollection.LogServiceCollection", coll["@odata.type"])
	assert.Equal(t, float64(0), coll["Members@odata.count"])
	assert.Equal(t, []interface{}{}, coll["Members"])

	for _, p := range []string{"", "/Entries", "/Entries/1", "/Entries/1/attachment"} {
		code, _ := getLogJSON(t, srv, "/redfish/v1/Systems/1/LogServices/SerialConsole"+p)
		assert.Equal(t, http.StatusNotFound, code, p)
	}
}

func TestLogServices_SerialConsole(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	srv.SetSerialLog(fakeSerialLog("debian login: \x1b[0m"))

	code, coll := getLogJSON(t, srv, "/redfish/v1/Systems/1/LogServices/")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, []interface{}{map[string]interface{}{"@odata.id": "/redfish/v1/Systems/1/LogServices/SerialConsole"}}, coll["Members"])

	code, ls := getLogJSON(t, srv, "/redfish/v1/Systems/1/LogServices/SerialConsole")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "#LogService.v1_1_0.LogService", ls["@odata.type"])
	assert.Equal(t, "/redfish/v1/Systems/1/LogServices/SerialConsole", ls["@odata.id"])
	assert.NotEmpty(t, ls["@odata.context"])
	assert.Equal(t, "SerialConsole", ls["Id"])
	assert.Equal(t, "OEM", ls["LogEntryType"])
	assert.Equal(t, "WrapsWhenFull", ls["OverWritePolicy"])
	assert.Equal(t, true, ls["ServiceEnabled"])
	assert.Equal(t, map[string]interface{}{"@odata.id": "/redfish/v1/Systems/1/LogServices/SerialConsole/Entries"}, ls["Entries"])

	code, entries := getLogJSON(t, srv, "/redfish/v1/Systems/1/LogServices/SerialConsole/Entries")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "#LogEntryCollection.LogEntryCollection", entries["@odata.type"])
	// Entries are contained resources: expanded in Members
	require.Len(t, entries["Members"], 1)
	member := entries["Members"].([]interface{})[0].(map[string]interface{})
	assert.Equal(t, "/redfish/v1/Systems/1/LogServices/SerialConsole/Entries/1", member["@odata.id"])
	assert.Equal(t, "#LogEntry.v1_7_0.LogEntry", member["@odata.type"])
	assert.Equal(t, "Oem", member["EntryType"])

	code, entry := getLogJSON(t, srv, "/redfish/v1/Systems/1/LogServices/SerialConsole/Entries/1")
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "#LogEntry.v1_7_0.LogEntry", entry["@odata.type"])
	assert.Equal(t, "Oem", entry["EntryType"])
	assert.NotEmpty(t, entry["OemRecordFormat"])
	assert.Equal(t, "/redfish/v1/Systems/1/LogServices/SerialConsole/Entries/1/attachment", entry["AdditionalDataURI"])
	assert.Equal(t, float64(len("debian login: \x1b[0m")), entry["AdditionalDataSizeBytes"])

	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/redfish/v1/Systems/1/LogServices/SerialConsole/Entries/1/attachment", nil))
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "text/plain", w.Header().Get("Content-Type"))
	assert.Equal(t, "debian login: \x1b[0m", w.Body.String())

	for _, p := range []string{"/redfish/v1/Systems/1/LogServices/Other", "/redfish/v1/Systems/1/LogServices/SerialConsole/Entries/2"} {
		code, _ := getLogJSON(t, srv, p)
		assert.Equal(t, http.StatusNotFound, code, p)
	}
}

func TestLogServices_RequiresAuth(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "admin", "password", "")
	srv.SetSerialLog(fakeSerialLog("secret"))
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/redfish/v1/Systems/1/LogServices/SerialConsole/Entries/1/attachment", nil))
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
