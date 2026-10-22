package redfish

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

func doReq(srv *Server, method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	var req *http.Request
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func login(t *testing.T, srv *Server) (token, location string) {
	t.Helper()
	w := doReq(srv, "POST", sessionsPath, `{"UserName":"admin","Password":"password"}`, nil)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	token, location = w.Header().Get("X-Auth-Token"), w.Header().Get("Location")
	require.NotEmpty(t, token)
	require.NotEmpty(t, location)
	return token, location
}

func TestServiceRoot_NoAuthAndLinksSessions(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "admin", "password", "")

	w := doReq(srv, "GET", "/redfish/v1", "", nil)
	require.Equal(t, http.StatusOK, w.Code)
	var root map[string]interface{}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &root))
	assert.Equal(t, map[string]interface{}{"@odata.id": "/redfish/v1/SessionService"}, root["SessionService"])
	assert.Equal(t, map[string]interface{}{"Sessions": map[string]interface{}{"@odata.id": sessionsPath}}, root["Links"])

	w = doReq(srv, "GET", "/redfish", "", nil)
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"v1":"/redfish/v1/"}`, w.Body.String())
}

func TestSession_LoginUseLogout(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "admin", "password", "")

	token, location := login(t, srv)
	assert.Equal(t, sessionsPath+"/1", location)
	assert.Len(t, token, 64)

	// The token authenticates without Basic auth
	w := doReq(srv, "GET", "/redfish/v1/Systems/1", "", map[string]string{"X-Auth-Token": token})
	assert.Equal(t, http.StatusOK, w.Code)
	w = doReq(srv, "POST", "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", `{"ResetType":"ForceOff"}`,
		map[string]string{"X-Auth-Token": token})
	assert.Equal(t, http.StatusNoContent, w.Code)

	// The session resource, without the password
	w = doReq(srv, "GET", location, "", map[string]string{"X-Auth-Token": token})
	require.Equal(t, http.StatusOK, w.Code)
	assert.JSONEq(t, `{"@odata.type":"#Session.v1_0_0.Session","@odata.id":"`+location+`","Id":"1","Name":"User Session","UserName":"admin"}`, w.Body.String())

	w = doReq(srv, "GET", sessionsPath, "", map[string]string{"X-Auth-Token": token})
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"Members@odata.count":1`)

	// Logout invalidates the token
	w = doReq(srv, "DELETE", location, "", map[string]string{"X-Auth-Token": token})
	assert.Equal(t, http.StatusNoContent, w.Code)
	w = doReq(srv, "GET", "/redfish/v1/Systems/1", "", map[string]string{"X-Auth-Token": token})
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	w = doReq(srv, "DELETE", location, "", map[string]string{"Authorization": basic("admin", "password")})
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func basic(user, pass string) string {
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth(user, pass)
	return req.Header.Get("Authorization")
}

func TestSession_LoginErrors(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "admin", "password", "")

	w := doReq(srv, "POST", sessionsPath, `{"UserName":"admin","Password":"wrong"}`, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Empty(t, w.Header().Get("X-Auth-Token"))
	w = doReq(srv, "POST", sessionsPath, `{"UserName":"admin"}`, nil)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	w = doReq(srv, "POST", sessionsPath, `not json`, nil)
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Other session endpoints need authentication
	w = doReq(srv, "GET", sessionsPath, "", nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	w = doReq(srv, "GET", "/redfish/v1/Systems/1", "", map[string]string{"X-Auth-Token": "bogus"})
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "NoValidSession")
}

func TestSession_TrailingSlash(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "admin", "password", "")
	w := doReq(srv, "POST", sessionsPath+"/", `{"UserName":"admin","Password":"password"}`, nil)
	require.Equal(t, http.StatusCreated, w.Code)
	token := w.Header().Get("X-Auth-Token")
	w = doReq(srv, "DELETE", w.Header().Get("Location")+"/", "", map[string]string{"X-Auth-Token": token})
	assert.Equal(t, http.StatusNoContent, w.Code)
}

func TestSession_ExpiresWhenIdle(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "admin", "password", "")
	now := time.Unix(1000, 0)
	srv.sessions.now = func() time.Time { return now }
	token, _ := login(t, srv)

	// Using it keeps it alive
	now = now.Add(DefaultSessionTimeout - time.Second)
	w := doReq(srv, "GET", "/redfish/v1/Systems/1", "", map[string]string{"X-Auth-Token": token})
	assert.Equal(t, http.StatusOK, w.Code)
	now = now.Add(DefaultSessionTimeout - time.Second)
	w = doReq(srv, "GET", "/redfish/v1/Systems/1", "", map[string]string{"X-Auth-Token": token})
	assert.Equal(t, http.StatusOK, w.Code)

	now = now.Add(DefaultSessionTimeout + time.Second)
	w = doReq(srv, "GET", "/redfish/v1/Systems/1", "", map[string]string{"X-Auth-Token": token})
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestSessionService_GetAndPatchTimeout(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "admin", "password", "")
	auth := map[string]string{"Authorization": basic("admin", "password")}

	w := doReq(srv, "GET", "/redfish/v1/SessionService", "", auth)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"SessionTimeout":1800`)
	assert.Contains(t, w.Body.String(), `"ServiceEnabled":true`)

	w = doReq(srv, "PATCH", "/redfish/v1/SessionService", `{"SessionTimeout":600}`, auth)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"SessionTimeout":600`)
	assert.Equal(t, 600*time.Second, srv.sessions.getTimeout())

	w = doReq(srv, "PATCH", "/redfish/v1/SessionService", `{"SessionTimeout":10}`, auth)
	assert.Equal(t, http.StatusBadRequest, w.Code)
}

func TestSession_Limit(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "admin", "password", "")
	for i := 0; i < maxSessions; i++ {
		login(t, srv)
	}
	w := doReq(srv, "POST", sessionsPath, `{"UserName":"admin","Password":"password"}`, nil)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

func TestSession_AuthDisabled(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	w := doReq(srv, "POST", sessionsPath, `{"UserName":"any","Password":"any"}`, nil)
	assert.Equal(t, http.StatusCreated, w.Code)
	w = doReq(srv, "GET", "/redfish/v1/Systems/1", "", nil)
	assert.Equal(t, http.StatusOK, w.Code)
}
