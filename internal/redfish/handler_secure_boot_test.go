package redfish

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/machine"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

func newSecureBootServer(sb machine.SecureBoot) (*Server, *mockMachine) {
	mock := newMockMachine(qmp.StatusRunning)
	mock.secureBoot = &sb
	return NewServer(mock, "", "", ""), mock
}

func serve(srv *Server, method, path, body string, header ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	return w
}

func TestGetSecureBoot(t *testing.T) {
	srv, _ := newSecureBootServer(machine.SecureBoot{Enable: true, CurrentBoot: false})

	for _, path := range []string{"/redfish/v1/Systems/1/SecureBoot", "/redfish/v1/Systems/1/SecureBoot/"} {
		w := serve(srv, "GET", path, "")
		require.Equal(t, http.StatusOK, w.Code, path)

		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, "#SecureBoot.v1_1_0.SecureBoot", body["@odata.type"])
		assert.Equal(t, "/redfish/v1/Systems/1/SecureBoot", body["@odata.id"])
		assert.Equal(t, "SecureBoot", body["Id"])
		assert.Equal(t, true, body["SecureBootEnable"])
		assert.Equal(t, "Disabled", body["SecureBootCurrentBoot"])
		assert.Equal(t, "UserMode", body["SecureBootMode"])
		assert.Equal(t, w.Header().Get("ETag"), body["@odata.etag"])
		assert.Equal(t, map[string]any{
			"#SecureBoot.ResetKeys": map[string]any{
				"target":                                "/redfish/v1/Systems/1/SecureBoot/Actions/SecureBoot.ResetKeys",
				"ResetKeysType@Redfish.AllowableValues": []any{"ResetAllKeysToDefault"},
			},
		}, body["Actions"])
	}

	srv, _ = newSecureBootServer(machine.SecureBoot{Enable: true, CurrentBoot: true})
	var res SecureBootResource
	require.NoError(t, json.Unmarshal(serve(srv, "GET", "/redfish/v1/Systems/1/SecureBoot", "").Body.Bytes(), &res))
	assert.Equal(t, "Enabled", res.SecureBootCurrentBoot)
}

func TestSecureBoot_SystemLink(t *testing.T) {
	srv, _ := newSecureBootServer(machine.SecureBoot{})
	var body map[string]any
	require.NoError(t, json.Unmarshal(serve(srv, "GET", "/redfish/v1/Systems/1", "").Body.Bytes(), &body))
	assert.Equal(t, map[string]any{"@odata.id": "/redfish/v1/Systems/1/SecureBoot"}, body["SecureBoot"])
}

func TestSecureBoot_Unsupported_NotFound(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")

	var body map[string]any
	require.NoError(t, json.Unmarshal(serve(srv, "GET", "/redfish/v1/Systems/1", "").Body.Bytes(), &body))
	assert.NotContains(t, body, "SecureBoot")

	assert.Equal(t, http.StatusNotFound, serve(srv, "GET", "/redfish/v1/Systems/1/SecureBoot", "").Code)
	assert.Equal(t, http.StatusNotFound, serve(srv, "GET", "/redfish/v1/Systems/1/SecureBoot/", "").Code)
	assert.Equal(t, http.StatusNotFound, serve(srv, "PATCH", "/redfish/v1/Systems/1/SecureBoot", `{"SecureBootEnable": true}`).Code)
	assert.Equal(t, http.StatusNotFound, serve(srv, "POST", "/redfish/v1/Systems/1/SecureBoot/Actions/SecureBoot.ResetKeys", `{"ResetKeysType": "ResetAllKeysToDefault"}`).Code)
}

func TestPatchSecureBoot(t *testing.T) {
	srv, mock := newSecureBootServer(machine.SecureBoot{})

	w := serve(srv, "PATCH", "/redfish/v1/Systems/1/SecureBoot", `{"SecureBootEnable": true}`)
	require.Equal(t, http.StatusOK, w.Code)
	var res SecureBootResource
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &res))
	assert.True(t, res.SecureBootEnable)
	assert.Equal(t, "Disabled", res.SecureBootCurrentBoot) // from the next boot
	assert.Equal(t, []string{"SetSecureBootEnable:true"}, mock.Calls())

	w = serve(srv, "PATCH", "/redfish/v1/Systems/1/SecureBoot/", `{"SecureBootEnable": false}`)
	require.Equal(t, http.StatusOK, w.Code)
	assert.False(t, mock.secureBoot.Enable)
}

func TestPatchSecureBoot_Invalid(t *testing.T) {
	for name, body := range map[string]string{
		"string":    `{"SecureBootEnable": "true"}`,
		"null":      `{"SecureBootEnable": null}`,
		"number":    `{"SecureBootEnable": 1}`,
		"missing":   `{"SecureBootMode": "SetupMode"}`,
		"malformed": `{`,
	} {
		t.Run(name, func(t *testing.T) {
			srv, mock := newSecureBootServer(machine.SecureBoot{})
			w := serve(srv, "PATCH", "/redfish/v1/Systems/1/SecureBoot", body)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Empty(t, mock.Calls())
		})
	}
}

func TestPatchSecureBoot_FirmwareMissing(t *testing.T) {
	srv, mock := newSecureBootServer(machine.SecureBoot{})
	mock.secureBootErr = errors.New("Secure Boot OVMF not available: OVMF_CODE.secboot.fd: no such file")
	w := serve(srv, "PATCH", "/redfish/v1/Systems/1/SecureBoot", `{"SecureBootEnable": true}`)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "OVMF_CODE.secboot.fd")
	assert.False(t, mock.secureBoot.Enable)
}

func TestPatchSecureBoot_IfMatch(t *testing.T) {
	srv, mock := newSecureBootServer(machine.SecureBoot{})
	etag := serve(srv, "GET", "/redfish/v1/Systems/1/SecureBoot", "").Header().Get("ETag")
	require.NotEmpty(t, etag)

	w := serve(srv, "PATCH", "/redfish/v1/Systems/1/SecureBoot", `{"SecureBootEnable": true}`, "If-Match", `"stale"`)
	assert.Equal(t, http.StatusPreconditionFailed, w.Code)
	assert.False(t, mock.secureBoot.Enable)

	w = serve(srv, "PATCH", "/redfish/v1/Systems/1/SecureBoot", `{"SecureBootEnable": true}`, "If-Match", etag)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, mock.secureBoot.Enable)
	assert.NotEqual(t, etag, w.Header().Get("ETag"))
}

func TestResetKeys(t *testing.T) {
	srv, mock := newSecureBootServer(machine.SecureBoot{})
	for _, path := range []string{
		"/redfish/v1/Systems/1/SecureBoot/Actions/SecureBoot.ResetKeys",
		"/redfish/v1/Systems/1/SecureBoot/Actions/SecureBoot.ResetKeys/",
	} {
		w := serve(srv, "POST", path, `{"ResetKeysType": "ResetAllKeysToDefault"}`)
		assert.Equal(t, http.StatusNoContent, w.Code, path)
	}
	assert.Equal(t, []string{"ResetSecureBootKeys", "ResetSecureBootKeys"}, mock.Calls())
}

func TestResetKeys_Invalid(t *testing.T) {
	for name, body := range map[string]string{
		"delete all": `{"ResetKeysType": "DeleteAllKeys"}`,
		"delete PK":  `{"ResetKeysType": "DeletePK"}`,
		"missing":    `{}`,
		"malformed":  `{`,
	} {
		t.Run(name, func(t *testing.T) {
			srv, mock := newSecureBootServer(machine.SecureBoot{})
			w := serve(srv, "POST", "/redfish/v1/Systems/1/SecureBoot/Actions/SecureBoot.ResetKeys", body)
			assert.Equal(t, http.StatusBadRequest, w.Code)
			assert.Empty(t, mock.Calls())
		})
	}
}
