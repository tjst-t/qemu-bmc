package bios

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func raw(t *testing.T, attrs map[string]any) map[string]json.RawMessage {
	t.Helper()
	out := map[string]json.RawMessage{}
	for k, v := range attrs {
		b, err := json.Marshal(v)
		require.NoError(t, err)
		out[k] = b
	}
	return out
}

func TestNewStore_Defaults(t *testing.T) {
	s, err := NewStore("", "")
	require.NoError(t, err)
	attrs := s.Attributes()
	assert.Equal(t, "Enabled", attrs["ProcVirtualization"])
	assert.Equal(t, "Enabled", attrs["ProcHyperThreading"])
	assert.Equal(t, "Disabled", attrs["SriovGlobalEnable"])
	assert.Equal(t, "Enabled", attrs["PxeDev1EnDis"])
	assert.Equal(t, "MaxPerformance", attrs["PowerProfile"])
	assert.Equal(t, "", attrs["AssetTag"])
	assert.Equal(t, "Bios", attrs["BootMode"])
	assert.Equal(t, "Disabled", attrs["SecureBoot"])
	assert.Len(t, attrs, len(Definitions))
	assert.Empty(t, s.Pending())
	assert.False(t, s.ResetPending())
}

func TestNewStore_Overrides(t *testing.T) {
	s, err := NewStore("", `{"ProcHyperThreading":"Disabled","AssetTag":"rack-1"}`)
	require.NoError(t, err)
	assert.Equal(t, "Disabled", s.Attributes()["ProcHyperThreading"])
	assert.Equal(t, "rack-1", s.Attributes()["AssetTag"])
	for _, a := range s.Registry() {
		if a.Name == "ProcHyperThreading" {
			assert.Equal(t, "Disabled", a.Default)
		}
	}

	for _, bad := range []string{`[]`, `{"Nope":"x"}`, `{"BootMode":"Uefi"}`, `{"NumLock":"Maybe"}`, `{"NumLock":1}`} {
		_, err := NewStore("", bad)
		assert.Error(t, err, bad)
	}
}

func TestReadOnlyValues(t *testing.T) {
	s, _ := NewStore("", "")
	mode := "Uefi"
	s.SetReadOnlyValue("BootMode", func() string { return mode })
	s.SetReadOnlyValue("SecureBoot", func() string { return "Enabled" })
	assert.Equal(t, "Uefi", s.Attributes()["BootMode"])
	assert.Equal(t, "Enabled", s.Attributes()["SecureBoot"])
	for _, a := range s.Registry() {
		if a.Name == "BootMode" {
			assert.True(t, a.ReadOnly)
			assert.Equal(t, "Uefi", a.Default)
		}
	}
}

func TestSetPending_Validation(t *testing.T) {
	s, _ := NewStore("", "")
	cases := []struct {
		attrs map[string]any
		key   string
	}{
		{map[string]any{"Unknown": "x"}, MsgPropertyUnknown},
		{map[string]any{"ProcVirtualization": true}, MsgPropertyValueTypeError},
		{map[string]any{"ProcVirtualization": nil}, MsgPropertyValueTypeError},
		{map[string]any{"ProcVirtualization": "Off"}, MsgPropertyValueNotInList},
		{map[string]any{"BootMode": "Uefi"}, MsgPropertyNotWritable},
		{map[string]any{"SecureBoot": "Enabled"}, MsgPropertyNotWritable},
		{map[string]any{"AssetTag": string(make([]byte, 65))}, MsgStringValueTooLong},
	}
	for _, c := range cases {
		err := s.SetPending(raw(t, c.attrs))
		var errs Errors
		require.ErrorAs(t, err, &errs, c.attrs)
		require.Len(t, errs, 1)
		assert.Equal(t, c.key, errs[0].MessageKey, c.attrs)
		assert.NotEmpty(t, errs[0].Message)
		assert.NotEmpty(t, errs[0].Args)
	}

	// One invalid attribute: nothing is changed
	err := s.SetPending(raw(t, map[string]any{"NumLock": "Off", "PowerProfile": "Turbo", "Foo": 1}))
	var errs Errors
	require.ErrorAs(t, err, &errs)
	assert.Len(t, errs, 2)
	assert.Empty(t, s.Pending())

	require.NoError(t, s.SetPending(raw(t, map[string]any{"AssetTag": string(make([]byte, 64))})))
}

func TestPendingApply(t *testing.T) {
	s, _ := NewStore("", "")
	require.NoError(t, s.SetPending(raw(t, map[string]any{"ProcVirtualization": "Disabled"})))
	require.NoError(t, s.SetPending(raw(t, map[string]any{"NumLock": "Off"})))
	assert.Equal(t, map[string]string{"ProcVirtualization": "Disabled", "NumLock": "Off"}, s.Pending())
	assert.Equal(t, "Enabled", s.Attributes()["ProcVirtualization"], "not before the next boot")

	assert.True(t, s.Apply())
	assert.Equal(t, "Disabled", s.Attributes()["ProcVirtualization"])
	assert.Equal(t, "Off", s.Attributes()["NumLock"])
	assert.Empty(t, s.Pending())
	assert.False(t, s.Apply(), "nothing pending")
}

func TestResetToDefaults(t *testing.T) {
	s, _ := NewStore("", `{"PowerProfile":"Balanced"}`)
	require.NoError(t, s.SetPending(raw(t, map[string]any{"ProcVirtualization": "Disabled", "PowerProfile": "PowerSaving"})))
	s.Apply()

	require.NoError(t, s.SetPending(raw(t, map[string]any{"NumLock": "Off"})))
	require.NoError(t, s.ResetToDefaults())
	assert.True(t, s.ResetPending())
	assert.Empty(t, s.Pending(), "reset drops earlier pending settings")
	// Settings after the reset apply on top of the defaults
	require.NoError(t, s.SetPending(raw(t, map[string]any{"BootRetry": "Disabled"})))
	assert.Equal(t, "Disabled", s.Attributes()["ProcVirtualization"], "not before the next boot")

	assert.True(t, s.Apply())
	attrs := s.Attributes()
	assert.Equal(t, "Enabled", attrs["ProcVirtualization"])
	assert.Equal(t, "Balanced", attrs["PowerProfile"], "BIOS_ATTRIBUTES default")
	assert.Equal(t, "On", attrs["NumLock"])
	assert.Equal(t, "Disabled", attrs["BootRetry"])
	assert.False(t, s.ResetPending())
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "bios.json")
	s, err := NewStore(path, "")
	require.NoError(t, err)
	require.NoError(t, s.SetPending(raw(t, map[string]any{"ProcVirtualization": "Disabled"})))
	s.Apply()
	require.NoError(t, s.SetPending(raw(t, map[string]any{"AssetTag": "a1"})))

	// A new store (container restart) has the applied and pending values
	s2, err := NewStore(path, "")
	require.NoError(t, err)
	assert.Equal(t, "Disabled", s2.Attributes()["ProcVirtualization"])
	assert.Equal(t, map[string]string{"AssetTag": "a1"}, s2.Pending())

	require.NoError(t, s2.ResetToDefaults())
	s3, _ := NewStore(path, "")
	assert.True(t, s3.ResetPending())
	s3.Apply()
	s4, _ := NewStore(path, "")
	assert.Equal(t, "Enabled", s4.Attributes()["ProcVirtualization"])
	assert.False(t, s4.ResetPending())
}

func TestPersistence_InvalidFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bios.json")

	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o644))
	s, err := NewStore(path, "")
	require.NoError(t, err)
	assert.Equal(t, "Enabled", s.Attributes()["ProcVirtualization"])

	// Values no longer allowed are dropped, the rest kept
	require.NoError(t, os.WriteFile(path, []byte(`{"Attributes":{"ProcVirtualization":"Disabled","NumLock":"Maybe","Gone":"x","BootMode":"Uefi"},"Pending":{"PowerProfile":"Balanced"}}`), 0o644))
	s, err = NewStore(path, "")
	require.NoError(t, err)
	attrs := s.Attributes()
	assert.Equal(t, "Disabled", attrs["ProcVirtualization"])
	assert.Equal(t, "On", attrs["NumLock"])
	assert.NotContains(t, attrs, "Gone")
	assert.Equal(t, "Bios", attrs["BootMode"])
	assert.Equal(t, map[string]string{"PowerProfile": "Balanced"}, s.Pending())
}
