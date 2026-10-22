package redfish

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tjst-t/qemu-bmc/internal/qmp"
)

func newInfoTestServer(info SystemInfo) *Server {
	srv := NewServer(newMockMachine(qmp.StatusRunning), "", "", "")
	srv.SetSystemInfo(info)
	return srv
}

func TestGetSystem_ProcessorsAndStorageLinks(t *testing.T) {
	srv := newInfoTestServer(SystemInfo{})
	var system ComputerSystem
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1", &system))
	assert.Equal(t, "/redfish/v1/Systems/1/Processors", system.Processors.ODataID)
	assert.Equal(t, "/redfish/v1/Systems/1/Storage", system.Storage.ODataID)
	assert.Equal(t, "/redfish/v1/Systems/1/SimpleStorage", system.SimpleStorage.ODataID)
}

func TestProcessors(t *testing.T) {
	srv := newInfoTestServer(SystemInfo{CPUs: 4})
	for _, p := range []string{"", "/"} {
		var col Collection
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1/Processors"+p, &col))
		assert.Equal(t, "#ProcessorCollection.ProcessorCollection", col.ODataType)
		assert.Equal(t, "/redfish/v1/Systems/1/Processors", col.ODataID)
		assert.Equal(t, 1, col.MembersCount)
		assert.Equal(t, []ODataID{{ODataID: "/redfish/v1/Systems/1/Processors/CPU1"}}, col.Members)

		var cpu Processor
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1/Processors/CPU1"+p, &cpu))
		assert.Equal(t, "#Processor.v1_3_0.Processor", cpu.ODataType)
		assert.Equal(t, "/redfish/v1/Systems/1/Processors/CPU1", cpu.ODataID)
		assert.NotEmpty(t, cpu.ODataContext)
		assert.Equal(t, "CPU1", cpu.ID)
		assert.Equal(t, "CPU", cpu.ProcessorType)
		assert.Equal(t, "x86", cpu.ProcessorArchitecture)
		assert.Equal(t, "x86-64", cpu.InstructionSet)
		assert.Equal(t, 4, cpu.TotalCores)
		assert.Equal(t, 4, cpu.TotalThreads)
		assert.Equal(t, statusOK, cpu.Status)
	}

	var cpu Processor
	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/redfish/v1/Systems/1/Processors/CPU2", &cpu))
}

func TestProcessors_UnknownCount(t *testing.T) {
	srv := newInfoTestServer(SystemInfo{})
	var col Collection
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1/Processors", &col))
	assert.Equal(t, 0, col.MembersCount)
	assert.NotNil(t, col.Members)

	var cpu Processor
	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/redfish/v1/Systems/1/Processors/CPU1", &cpu))
}

var testDisks = []Disk{
	{CapacityBytes: 20 << 30, Protocol: "PCIe"},
	{}, // size and bus unknown
}

func TestStorage(t *testing.T) {
	srv := newInfoTestServer(SystemInfo{Disks: testDisks})
	for _, p := range []string{"", "/"} {
		var col Collection
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1/Storage"+p, &col))
		assert.Equal(t, "#StorageCollection.StorageCollection", col.ODataType)
		assert.Equal(t, "/redfish/v1/Systems/1/Storage", col.ODataID)
		assert.Equal(t, []ODataID{{ODataID: "/redfish/v1/Systems/1/Storage/1"}}, col.Members)
		assert.Equal(t, 1, col.MembersCount)

		var st Storage
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1/Storage/1"+p, &st))
		assert.Equal(t, "#Storage.v1_7_0.Storage", st.ODataType)
		assert.Equal(t, "/redfish/v1/Systems/1/Storage/1", st.ODataID)
		assert.NotEmpty(t, st.ODataContext)
		assert.Equal(t, "1", st.ID)
		assert.Equal(t, []ODataID{
			{ODataID: "/redfish/v1/Systems/1/Storage/1/Drives/Disk1"},
			{ODataID: "/redfish/v1/Systems/1/Storage/1/Drives/Disk2"},
		}, st.Drives)
		assert.Equal(t, 2, st.DrivesCount)
		assert.Equal(t, "/redfish/v1/Systems/1/Storage/1/Volumes", st.Volumes.ODataID)
		assert.Equal(t, statusOK, st.Status)

		var drive Drive
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1/Storage/1/Drives/Disk1"+p, &drive))
		assert.Equal(t, "#Drive.v1_7_0.Drive", drive.ODataType)
		assert.Equal(t, "/redfish/v1/Systems/1/Storage/1/Drives/Disk1", drive.ODataID)
		assert.NotEmpty(t, drive.ODataContext)
		assert.Equal(t, "Disk1", drive.ID)
		assert.Equal(t, "Disk 1", drive.Name)
		assert.Equal(t, int64(20<<30), drive.CapacityBytes)
		assert.Equal(t, "HDD", drive.MediaType)
		assert.Equal(t, "PCIe", drive.Protocol)
		assert.Equal(t, statusOK, drive.Status)
	}

	// Unknown capacity and protocol are omitted
	var raw map[string]interface{}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1/Storage/1/Drives/Disk2", &raw))
	assert.NotContains(t, raw, "CapacityBytes")
	assert.NotContains(t, raw, "Protocol")
	assert.Equal(t, "Disk 2", raw["Name"])

	for _, path := range []string{
		"/redfish/v1/Systems/1/Storage/2",
		"/redfish/v1/Systems/1/Storage/1/Drives/Disk0",
		"/redfish/v1/Systems/1/Storage/1/Drives/Disk3",
		"/redfish/v1/Systems/1/Storage/1/Drives/1",
		"/redfish/v1/Systems/1/Storage/2/Drives/Disk1",
	} {
		assert.Equal(t, http.StatusNotFound, getJSON(t, srv, path, &raw), path)
	}
}

func TestVolumes(t *testing.T) {
	srv := newInfoTestServer(SystemInfo{Disks: testDisks})
	for _, p := range []string{"", "/"} {
		var col Collection
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1/Storage/1/Volumes"+p, &col))
		assert.Equal(t, "#VolumeCollection.VolumeCollection", col.ODataType)
		assert.Equal(t, "/redfish/v1/Systems/1/Storage/1/Volumes", col.ODataID)
		assert.Equal(t, []ODataID{
			{ODataID: "/redfish/v1/Systems/1/Storage/1/Volumes/1"},
			{ODataID: "/redfish/v1/Systems/1/Storage/1/Volumes/2"},
		}, col.Members)
		assert.Equal(t, 2, col.MembersCount)

		var vol Volume
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1/Storage/1/Volumes/1"+p, &vol))
		assert.Equal(t, "#Volume.v1_4_0.Volume", vol.ODataType)
		assert.Equal(t, "/redfish/v1/Systems/1/Storage/1/Volumes/1", vol.ODataID)
		assert.NotEmpty(t, vol.ODataContext)
		assert.Equal(t, "1", vol.ID)
		assert.Equal(t, "Volume 1", vol.Name)
		assert.Equal(t, int64(20<<30), vol.CapacityBytes)
		assert.Equal(t, statusOK, vol.Status)
		assert.Equal(t, []ODataID{{ODataID: "/redfish/v1/Systems/1/Storage/1/Drives/Disk1"}}, vol.Links.Drives)
		assert.Equal(t, 1, vol.Links.DrivesCount)
	}

	// Volume 2 links Disk2; unknown capacity is omitted, RAIDType is never set
	var raw map[string]interface{}
	require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1/Storage/1/Volumes/2", &raw))
	assert.NotContains(t, raw, "CapacityBytes")
	assert.NotContains(t, raw, "RAIDType")
	assert.Equal(t, map[string]interface{}{
		"Drives":             []interface{}{map[string]interface{}{"@odata.id": "/redfish/v1/Systems/1/Storage/1/Drives/Disk2"}},
		"Drives@odata.count": float64(1),
	}, raw["Links"])

	for _, path := range []string{
		"/redfish/v1/Systems/1/Storage/1/Volumes/0",
		"/redfish/v1/Systems/1/Storage/1/Volumes/3",
		"/redfish/v1/Systems/1/Storage/1/Volumes/01",
		"/redfish/v1/Systems/1/Storage/1/Volumes/Disk1",
		"/redfish/v1/Systems/1/Storage/2/Volumes",
		"/redfish/v1/Systems/1/Storage/2/Volumes/1",
	} {
		assert.Equal(t, http.StatusNotFound, getJSON(t, srv, path, &raw), path)
	}
}

func TestSimpleStorage(t *testing.T) {
	srv := newInfoTestServer(SystemInfo{Disks: testDisks})
	for _, p := range []string{"", "/"} {
		var col Collection
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1/SimpleStorage"+p, &col))
		assert.Equal(t, "#SimpleStorageCollection.SimpleStorageCollection", col.ODataType)
		assert.Equal(t, "/redfish/v1/Systems/1/SimpleStorage", col.ODataID)
		assert.Equal(t, []ODataID{{ODataID: "/redfish/v1/Systems/1/SimpleStorage/1"}}, col.Members)
		assert.Equal(t, 1, col.MembersCount)

		var ss SimpleStorage
		require.Equal(t, http.StatusOK, getJSON(t, srv, "/redfish/v1/Systems/1/SimpleStorage/1"+p, &ss))
		assert.Equal(t, "#SimpleStorage.v1_2_0.SimpleStorage", ss.ODataType)
		assert.Equal(t, "/redfish/v1/Systems/1/SimpleStorage/1", ss.ODataID)
		assert.NotEmpty(t, ss.ODataContext)
		assert.Equal(t, "1", ss.ID)
		assert.Equal(t, []SimpleStorageDevice{
			{Name: "Disk 1", CapacityBytes: 20 << 30, Status: statusOK},
			{Name: "Disk 2", Status: statusOK},
		}, ss.Devices)
		assert.Equal(t, statusOK, ss.Status)
	}

	var raw map[string]interface{}
	assert.Equal(t, http.StatusNotFound, getJSON(t, srv, "/redfish/v1/Systems/1/SimpleStorage/2", &raw))
}

func TestStorage_NoDisks(t *testing.T) {
	srv := newInfoTestServer(SystemInfo{})
	for _, path := range []string{
		"/redfish/v1/Systems/1/Storage",
		"/redfish/v1/Systems/1/Storage/1/Volumes",
		"/redfish/v1/Systems/1/SimpleStorage",
	} {
		var col Collection
		require.Equal(t, http.StatusOK, getJSON(t, srv, path, &col), path)
		assert.Equal(t, 0, col.MembersCount, path)
		assert.NotNil(t, col.Members, path)
	}
	var raw map[string]interface{}
	for _, path := range []string{
		"/redfish/v1/Systems/1/Storage/1",
		"/redfish/v1/Systems/1/Storage/1/Drives/Disk1",
		"/redfish/v1/Systems/1/Storage/1/Volumes/1",
		"/redfish/v1/Systems/1/SimpleStorage/1",
	} {
		assert.Equal(t, http.StatusNotFound, getJSON(t, srv, path, &raw), path)
	}
}
