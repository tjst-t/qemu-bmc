package fault

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFirmwareFault_Validate(t *testing.T) {
	s := NewStore()
	f := mustAdd(t, s, Fault{Kind: FirmwareUpdateFail})
	assert.Equal(t, "firmware", f.Target)
	assert.Equal(t, DefaultAtPercent, *f.AtPercent)
	f = mustAdd(t, s, Fault{Kind: FirmwareImageRejected, Target: "bios"})
	assert.Equal(t, "BIOS", f.Target)
	f = mustAdd(t, s, Fault{Kind: FirmwareUpdateFail, Target: "firmware", AtPercent: intPtr(0)})
	assert.Equal(t, 0, *f.AtPercent)

	for _, bad := range []Fault{
		{Kind: FirmwareImageRejected, Target: "NIC"},
		{Kind: FirmwareImageRejected, Target: "redfish"},
		{Kind: FirmwareUpdateFail, AtPercent: intPtr(101)},
		{Kind: FirmwareUpdateFail, AtPercent: intPtr(-1)},
		{Kind: RedfishDrop, Target: "BMC"},
	} {
		_, err := s.Add(bad)
		assert.Error(t, err, "%+v", bad)
	}

	faults, err := Parse(`[{"kind":"firmware-update-fail","target":"BMC","atPercent":30}]`)
	require.NoError(t, err)
	assert.Equal(t, 30, *faults[0].AtPercent)
}

func TestRejectFirmwareImage(t *testing.T) {
	s := NewStore()
	mustAdd(t, s, Fault{Kind: FirmwareImageRejected, Target: "BIOS", Count: intPtr(1)})
	assert.False(t, s.RejectFirmwareImage("BMC"))
	assert.True(t, s.RejectFirmwareImage("BIOS"))
	assert.False(t, s.RejectFirmwareImage("BIOS"), "count used up")

	mustAdd(t, s, Fault{Kind: FirmwareImageRejected})
	assert.True(t, s.RejectFirmwareImage("BMC"))
	assert.True(t, s.RejectFirmwareImage("BIOS"))

	var nilStore *Store
	assert.False(t, nilStore.RejectFirmwareImage("BMC"))
}

func TestFirmwareUpdateFailure(t *testing.T) {
	s := NewStore()
	now := time.Now()
	s.now = func() time.Time { return now }
	mustAdd(t, s, Fault{Kind: FirmwareUpdateFail, Target: "BMC", AtPercent: intPtr(20), DurationSeconds: floatPtr(10)})

	_, ok := s.FirmwareUpdateFailure("BIOS")
	assert.False(t, ok)
	at, ok := s.FirmwareUpdateFailure("BMC")
	assert.True(t, ok)
	assert.Equal(t, 20, at)

	now = now.Add(11 * time.Second)
	_, ok = s.FirmwareUpdateFailure("BMC")
	assert.False(t, ok, "expired")

	var nilStore *Store
	_, ok = nilStore.FirmwareUpdateFailure("BMC")
	assert.False(t, ok)
}
