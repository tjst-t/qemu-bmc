package redfish

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/bmc"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

func newServerWithBMCUser(t *testing.T) (*Server, *bmc.State) {
	t.Helper()
	state := bmc.NewState("admin", "password")
	require.NoError(t, state.SetUserName(3, "maas"))
	require.NoError(t, state.SetUserPassword(3, "secret"))
	require.NoError(t, state.SetUserAccess(1, 3, bmc.UserAccess{PrivilegeLimit: 4, Enabled: true, IPMIMessaging: true}))
	srv := NewServer(newMockMachine(qmp.StatusRunning), "admin", "password", "")
	srv.SetUserStore(state)
	return srv, state
}

func TestBMCUser_BasicAuth(t *testing.T) {
	srv, state := newServerWithBMCUser(t)

	w := doReq(srv, "GET", "/redfish/v1/Systems/1", "", map[string]string{"Authorization": basic("maas", "secret")})
	assert.Equal(t, http.StatusOK, w.Code)
	w = doReq(srv, "GET", "/redfish/v1/Systems/1", "", map[string]string{"Authorization": basic("admin", "password")})
	assert.Equal(t, http.StatusOK, w.Code)
	w = doReq(srv, "GET", "/redfish/v1/Systems/1", "", map[string]string{"Authorization": basic("maas", "wrong")})
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	require.NoError(t, state.SetUserAccess(1, 3, bmc.UserAccess{PrivilegeLimit: 4, Enabled: false}))
	w = doReq(srv, "GET", "/redfish/v1/Systems/1", "", map[string]string{"Authorization": basic("maas", "secret")})
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestBMCUser_SessionLogin(t *testing.T) {
	srv, state := newServerWithBMCUser(t)

	w := doReq(srv, "POST", sessionsPath, `{"UserName":"maas","Password":"secret"}`, nil)
	require.Equal(t, http.StatusCreated, w.Code, w.Body.String())
	token := w.Header().Get("X-Auth-Token")
	require.NotEmpty(t, token)
	w = doReq(srv, "GET", "/redfish/v1/Systems/1", "", map[string]string{"X-Auth-Token": token})
	assert.Equal(t, http.StatusOK, w.Code)

	w = doReq(srv, "POST", sessionsPath, `{"UserName":"maas","Password":"wrong"}`, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	require.NoError(t, state.SetUserAccess(1, 3, bmc.UserAccess{PrivilegeLimit: 4, Enabled: false}))
	w = doReq(srv, "POST", sessionsPath, `{"UserName":"maas","Password":"secret"}`, nil)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestBMCUser_AuthDisabledUnchanged(t *testing.T) {
	state := bmc.NewState("", "")
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	srv.SetUserStore(state)

	w := doReq(srv, "GET", "/redfish/v1/Systems/1", "", nil)
	assert.Equal(t, http.StatusOK, w.Code)
}
