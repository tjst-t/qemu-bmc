package redfish

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/bios"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

func newBiosServer(t *testing.T) (*Server, *bios.Store) {
	t.Helper()
	store, err := bios.NewStore(filepath.Join(t.TempDir(), "bios.json"), "")
	require.NoError(t, err)
	store.SetReadOnlyValue("BootMode", func() string { return "Uefi" })
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	srv.SetBios(store)
	return srv, store
}

func decode(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(body, &m), string(body))
	return m
}

func TestBios_NotAvailable(t *testing.T) {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	system := decode(t, serve(srv, "GET", "/redfish/v1/Systems/1", "").Body.Bytes())
	assert.NotContains(t, system, "Bios")
	assert.Equal(t, http.StatusNotFound, serve(srv, "GET", "/redfish/v1/Systems/1/Bios", "").Code)
	assert.Equal(t, http.StatusNotFound, serve(srv, "PATCH", "/redfish/v1/Systems/1/Bios/Settings", `{"Attributes":{}}`).Code)
	assert.Equal(t, http.StatusNotFound, serve(srv, "GET", "/redfish/v1/Registries/BiosAttributeRegistry", "").Code)
	reg := decode(t, serve(srv, "GET", "/redfish/v1/Registries", "").Body.Bytes())
	assert.EqualValues(t, 0, reg["Members@odata.count"])
}

func TestGetBios(t *testing.T) {
	srv, _ := newBiosServer(t)

	system := decode(t, serve(srv, "GET", "/redfish/v1/Systems/1", "").Body.Bytes())
	assert.Equal(t, map[string]any{"@odata.id": "/redfish/v1/Systems/1/Bios"}, system["Bios"])

	for _, path := range []string{"/redfish/v1/Systems/1/Bios", "/redfish/v1/Systems/1/Bios/"} {
		w := serve(srv, "GET", path, "")
		require.Equal(t, http.StatusOK, w.Code)
		body := decode(t, w.Body.Bytes())
		assert.Equal(t, "#Bios.v1_2_0.Bios", body["@odata.type"])
		assert.Equal(t, "/redfish/v1/Systems/1/Bios", body["@odata.id"])
		assert.Equal(t, "Bios", body["Id"])
		assert.Equal(t, w.Header().Get("ETag"), body["@odata.etag"])
		assert.Equal(t, "BiosAttributeRegistry.1.0.0", body["AttributeRegistry"])
		assert.Equal(t, false, body["ResetBiosToDefaultsPending"])
		attrs := body["Attributes"].(map[string]any)
		assert.Equal(t, "Uefi", attrs["BootMode"])
		assert.Equal(t, "Enabled", attrs["ProcVirtualization"])
		assert.Equal(t, map[string]any{
			"@odata.type":         "#Settings.v1_3_0.Settings",
			"SettingsObject":      map[string]any{"@odata.id": "/redfish/v1/Systems/1/Bios/Settings"},
			"SupportedApplyTimes": []any{"OnReset"},
		}, body["@Redfish.Settings"])
		assert.Equal(t, map[string]any{
			"#Bios.ResetBios": map[string]any{"target": "/redfish/v1/Systems/1/Bios/Actions/Bios.ResetBios"},
		}, body["Actions"])
	}
}

func TestPatchBiosSettings(t *testing.T) {
	srv, store := newBiosServer(t)

	w := serve(srv, "GET", "/redfish/v1/Systems/1/Bios/Settings", "")
	require.Equal(t, http.StatusOK, w.Code)
	settings := decode(t, w.Body.Bytes())
	assert.Equal(t, "/redfish/v1/Systems/1/Bios/Settings", settings["@odata.id"])
	assert.Equal(t, map[string]any{}, settings["Attributes"])
	assert.Equal(t, "OnReset", settings["@Redfish.SettingsApplyTime"].(map[string]any)["ApplyTime"])
	etag := w.Header().Get("ETag")

	// sushy set_attributes with apply_time OnReset
	w = serve(srv, "PATCH", "/redfish/v1/Systems/1/Bios/Settings/",
		`{"Attributes":{"ProcVirtualization":"Disabled","AssetTag":"rack-7"},"@Redfish.SettingsApplyTime":{"ApplyTime":"OnReset"}}`,
		"If-Match", etag)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, map[string]any{"ProcVirtualization": "Disabled", "AssetTag": "rack-7"}, decode(t, w.Body.Bytes())["Attributes"])
	assert.NotEqual(t, etag, w.Header().Get("ETag"))

	// Pending until the next boot
	current := decode(t, serve(srv, "GET", "/redfish/v1/Systems/1/Bios", "").Body.Bytes())
	assert.Equal(t, "Enabled", current["Attributes"].(map[string]any)["ProcVirtualization"])
	assert.Equal(t, map[string]string{"ProcVirtualization": "Disabled", "AssetTag": "rack-7"}, store.Pending())

	// The old ETag no longer matches
	assert.Equal(t, http.StatusPreconditionFailed, serve(srv, "PATCH", "/redfish/v1/Systems/1/Bios/Settings",
		`{"Attributes":{"NumLock":"Off"}}`, "If-Match", etag).Code)

	store.Apply()
	current = decode(t, serve(srv, "GET", "/redfish/v1/Systems/1/Bios", "").Body.Bytes())
	assert.Equal(t, "Disabled", current["Attributes"].(map[string]any)["ProcVirtualization"])
	assert.Equal(t, "rack-7", current["Attributes"].(map[string]any)["AssetTag"])
	settings = decode(t, serve(srv, "GET", "/redfish/v1/Systems/1/Bios/Settings", "").Body.Bytes())
	assert.Equal(t, map[string]any{}, settings["Attributes"])
}

func TestPatchBiosSettings_Invalid(t *testing.T) {
	srv, store := newBiosServer(t)

	cases := []struct {
		body, messageID, related string
	}{
		{`{"Attributes":{"Bogus":"x"}}`, "Base.1.8.PropertyUnknown", "#/Attributes/Bogus"},
		{`{"Attributes":{"ProcVirtualization":1}}`, "Base.1.8.PropertyValueTypeError", "#/Attributes/ProcVirtualization"},
		{`{"Attributes":{"ProcVirtualization":"Maybe"}}`, "Base.1.8.PropertyValueNotInList", "#/Attributes/ProcVirtualization"},
		{`{"Attributes":{"BootMode":"Bios"}}`, "Base.1.8.PropertyNotWritable", "#/Attributes/BootMode"},
		{`{"Attributes":{"AssetTag":"` + strings.Repeat("a", 65) + `"}}`, "Base.1.8.StringValueTooLong", "#/Attributes/AssetTag"},
		{`{"Attributes":"x"}`, "Base.1.8.PropertyValueTypeError", "#/Attributes"},
		{`{"Attributes":{},"Other":1}`, "Base.1.8.PropertyUnknown", "#/Other"},
		{`{"Attributes":{},"@Redfish.SettingsApplyTime":{"ApplyTime":"Immediate"}}`, "Base.1.8.PropertyValueNotInList", "#/@Redfish.SettingsApplyTime/ApplyTime"},
	}
	for _, c := range cases {
		w := serve(srv, "PATCH", "/redfish/v1/Systems/1/Bios/Settings", c.body)
		require.Equal(t, http.StatusBadRequest, w.Code, c.body)
		var e RedfishError
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &e))
		assert.Equal(t, "Base.1.8.GeneralError", e.Error.Code)
		require.Len(t, e.Error.ExtendedInfo, 1, c.body)
		msg := e.Error.ExtendedInfo[0]
		assert.Equal(t, c.messageID, msg.MessageID, c.body)
		assert.Equal(t, []string{c.related}, msg.RelatedProperties, c.body)
		assert.Equal(t, "#Message.v1_1_1.Message", msg.ODataType)
		assert.NotEmpty(t, msg.Message)
		assert.NotEmpty(t, msg.Resolution)
		assert.Equal(t, msg.Message, e.Error.Message)
	}
	assert.Empty(t, store.Pending())

	// Every invalid attribute is reported
	w := serve(srv, "PATCH", "/redfish/v1/Systems/1/Bios/Settings", `{"Attributes":{"Bogus":"x","NumLock":"Maybe","PowerProfile":"Balanced"}}`)
	require.Equal(t, http.StatusBadRequest, w.Code)
	var e RedfishError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &e))
	assert.Len(t, e.Error.ExtendedInfo, 2)
	assert.Empty(t, store.Pending())

	assert.Equal(t, http.StatusBadRequest, serve(srv, "PATCH", "/redfish/v1/Systems/1/Bios/Settings", `{}`).Code)
	assert.Equal(t, http.StatusBadRequest, serve(srv, "PATCH", "/redfish/v1/Systems/1/Bios/Settings", `nope`).Code)
	assert.Equal(t, http.StatusMethodNotAllowed, serve(srv, "PATCH", "/redfish/v1/Systems/1/Bios", `{"Attributes":{}}`).Code)
}

func TestResetBios(t *testing.T) {
	srv, store := newBiosServer(t)
	require.NoError(t, store.SetPending(map[string]json.RawMessage{"NumLock": json.RawMessage(`"Off"`)}))
	store.Apply()
	require.NoError(t, store.SetPending(map[string]json.RawMessage{"PowerProfile": json.RawMessage(`"Balanced"`)}))

	// sushy posts without a body first
	w := serve(srv, "POST", "/redfish/v1/Systems/1/Bios/Actions/Bios.ResetBios", "")
	require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
	assert.Equal(t, http.StatusNoContent, serve(srv, "POST", "/redfish/v1/Systems/1/Bios/Actions/Bios.ResetBios/", "{}").Code)

	current := decode(t, serve(srv, "GET", "/redfish/v1/Systems/1/Bios", "").Body.Bytes())
	assert.Equal(t, true, current["ResetBiosToDefaultsPending"])
	assert.Equal(t, "Off", current["Attributes"].(map[string]any)["NumLock"], "not before the next boot")
	assert.Empty(t, store.Pending())

	store.Apply()
	current = decode(t, serve(srv, "GET", "/redfish/v1/Systems/1/Bios", "").Body.Bytes())
	assert.Equal(t, false, current["ResetBiosToDefaultsPending"])
	assert.Equal(t, "On", current["Attributes"].(map[string]any)["NumLock"])
	assert.Equal(t, "MaxPerformance", current["Attributes"].(map[string]any)["PowerProfile"])
}

func TestRegistries(t *testing.T) {
	srv, _ := newBiosServer(t)
	srv.SetSystemInfo(SystemInfo{Model: "qemu-bmc"})
	srv.SetFirmwareVersion("1.2.3")

	root := decode(t, serve(srv, "GET", "/redfish/v1", "").Body.Bytes())
	assert.Equal(t, map[string]any{"@odata.id": "/redfish/v1/Registries"}, root["Registries"])

	coll := decode(t, serve(srv, "GET", "/redfish/v1/Registries/", "").Body.Bytes())
	assert.Equal(t, "#MessageRegistryFileCollection.MessageRegistryFileCollection", coll["@odata.type"])
	assert.Equal(t, []any{map[string]any{"@odata.id": "/redfish/v1/Registries/BiosAttributeRegistry"}}, coll["Members"])

	w := serve(srv, "GET", "/redfish/v1/Registries/BiosAttributeRegistry", "")
	require.Equal(t, http.StatusOK, w.Code)
	file := decode(t, w.Body.Bytes())
	assert.Equal(t, "#MessageRegistryFile.v1_1_0.MessageRegistryFile", file["@odata.type"])
	assert.Equal(t, "BiosAttributeRegistry", file["Id"])
	assert.Equal(t, []any{"en"}, file["Languages"])
	loc := file["Location"].([]any)[0].(map[string]any)
	assert.Equal(t, "en", loc["Language"])
	uri := loc["Uri"].(string)
	assert.Equal(t, "/redfish/v1/Registries/BiosAttributeRegistry/BiosAttributeRegistry.1.0.0.json", uri)
	assert.Equal(t, http.StatusNotFound, serve(srv, "GET", "/redfish/v1/Registries/Other", "").Code)

	w = serve(srv, "GET", uri, "")
	require.Equal(t, http.StatusOK, w.Code)
	var reg AttributeRegistry
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &reg))
	assert.Equal(t, "#AttributeRegistry.v1_3_6.AttributeRegistry", reg.ODataType)
	// sushy matches the registry Id against Bios AttributeRegistry
	assert.Equal(t, "BiosAttributeRegistry.1.0.0", reg.ID)
	assert.Equal(t, "en", reg.Language)
	assert.Equal(t, "qemu-bmc", reg.OwningEntity)
	assert.Equal(t, "1.0.0", reg.RegistryVersion)
	assert.Equal(t, []SupportedSystem{{ProductName: "qemu-bmc", SystemID: "qemubmc", FirmwareVersion: "1.2.3"}}, reg.SupportedSystems)

	byName := map[string]RegistryAttribute{}
	for _, a := range reg.RegistryEntries.Attributes {
		byName[a.AttributeName] = a
	}
	assert.Len(t, byName, len(bios.Definitions))
	pv := byName["ProcVirtualization"]
	assert.Equal(t, "Enumeration", pv.Type)
	assert.Equal(t, "Enabled", pv.DefaultValue)
	assert.False(t, pv.ReadOnly)
	assert.True(t, pv.ResetRequired)
	assert.Equal(t, []RegistryAttrValue{{"Enabled", "Enabled"}, {"Disabled", "Disabled"}}, pv.Value)
	bm := byName["BootMode"]
	assert.True(t, bm.ReadOnly)
	assert.Equal(t, "Uefi", bm.DefaultValue)
	tag := byName["AssetTag"]
	assert.Equal(t, "String", tag.Type)
	require.NotNil(t, tag.MaxLength)
	assert.Equal(t, 64, *tag.MaxLength)
	assert.Nil(t, tag.Value)
}
