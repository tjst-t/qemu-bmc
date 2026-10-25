package fault

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func apiRequest(t *testing.T, h http.Handler, method, path, body, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestAPI_AddListDelete(t *testing.T) {
	s := NewStore()
	h := s.APIHandler("")

	rec := apiRequest(t, h, "POST", "/faults", `{"kind":"redfish-error","status":503,"path":"/redfish/v1/Systems"}`, "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var added Fault
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &added))
	assert.Equal(t, "1", added.ID)
	assert.Equal(t, "/faults/1", rec.Header().Get("Location"))

	apiRequest(t, h, "POST", "/faults", `{"kind":"ipmi-drop"}`, "")
	rec = apiRequest(t, h, "GET", "/faults", "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var list []Fault
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 2)
	assert.Equal(t, 503, list[0].Status)

	assert.Equal(t, http.StatusNoContent, apiRequest(t, h, "DELETE", "/faults/1", "", "").Code)
	assert.Equal(t, http.StatusNotFound, apiRequest(t, h, "DELETE", "/faults/1", "", "").Code)
	assert.Len(t, s.List(), 1)
	assert.Equal(t, http.StatusNoContent, apiRequest(t, h, "DELETE", "/faults/", "", "").Code)
	assert.Empty(t, s.List())

	// An empty list is [], not null
	rec = apiRequest(t, h, "GET", "/faults", "", "")
	assert.JSONEq(t, `[]`, rec.Body.String())
}

func TestAPI_BadRequests(t *testing.T) {
	h := NewStore().APIHandler("")
	assert.Equal(t, http.StatusBadRequest, apiRequest(t, h, "POST", "/faults", `{"kind":"nope"}`, "").Code)
	assert.Equal(t, http.StatusBadRequest, apiRequest(t, h, "POST", "/faults", `{"kind":"ipmi-drop","typo":1}`, "").Code)
	assert.Equal(t, http.StatusBadRequest, apiRequest(t, h, "POST", "/faults", `not json`, "").Code)
	assert.Equal(t, http.StatusMethodNotAllowed, apiRequest(t, h, "PUT", "/faults", "", "").Code)
	assert.Equal(t, http.StatusMethodNotAllowed, apiRequest(t, h, "GET", "/faults/1", "", "").Code)
	assert.Equal(t, http.StatusNotFound, apiRequest(t, h, "GET", "/other", "", "").Code)
}

func TestAPI_Token(t *testing.T) {
	h := NewStore().APIHandler("secret")
	assert.Equal(t, http.StatusUnauthorized, apiRequest(t, h, "GET", "/faults", "", "").Code)
	assert.Equal(t, http.StatusUnauthorized, apiRequest(t, h, "GET", "/faults", "", "wrong").Code)
	assert.Equal(t, http.StatusOK, apiRequest(t, h, "GET", "/faults", "", "secret").Code)
}
