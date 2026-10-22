package redfish

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"
)

// The guest disks are reported both as Storage/1 with one drive per disk and
// as SimpleStorage/1 (deprecated, but read first by Ironic inspection). Both
// collections are empty when no disk is known (legacy mode).
const (
	storagePath       = systemPath + "/Storage"
	storage1Path      = storagePath + "/1"
	drivesPath        = storage1Path + "/Drives"
	simpleStoragePath = systemPath + "/SimpleStorage"
	simpleStorage1    = simpleStoragePath + "/1"
)

func diskName(idx int) string {
	return fmt.Sprintf("Disk %d", idx)
}

// storageMembers returns the single storage member, or none without disks.
func (s *Server) storageMembers(path string) []ODataID {
	if len(s.systemInfo.Disks) == 0 {
		return []ODataID{}
	}
	return []ODataID{{ODataID: path}}
}

// hasStorage reports whether the {stid} in the request names storage 1 and
// there are disks to report.
func (s *Server) hasStorage(r *http.Request) bool {
	return len(s.systemInfo.Disks) > 0 && mux.Vars(r)["stid"] == "1"
}

func (s *Server) handleStorageCollection(w http.ResponseWriter, r *http.Request) {
	members := s.storageMembers(storage1Path)
	writeJSON(w, http.StatusOK, Collection{
		ODataType:    "#StorageCollection.StorageCollection",
		ODataID:      storagePath,
		Name:         "Storage Collection",
		MembersCount: len(members),
		Members:      members,
	})
}

func (s *Server) handleGetStorage(w http.ResponseWriter, r *http.Request) {
	if !s.hasStorage(r) {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "storage not found")
		return
	}
	drives := make([]ODataID, 0, len(s.systemInfo.Disks))
	for i := range s.systemInfo.Disks {
		drives = append(drives, ODataID{ODataID: fmt.Sprintf("%s/Disk%d", drivesPath, i+1)})
	}
	writeJSON(w, http.StatusOK, Storage{
		ODataType:    "#Storage.v1_7_0.Storage",
		ODataID:      storage1Path,
		ODataContext: "/redfish/v1/$metadata#Storage.Storage",
		ID:           "1",
		Name:         "Storage",
		Drives:       drives,
		DrivesCount:  len(drives),
		Status:       statusOK,
	})
}

// handleGetDrive serves Disk{n}, the n-th (1-based) disk in QEMU
// command-line order.
func (s *Server) handleGetDrive(w http.ResponseWriter, r *http.Request) {
	idx := 0
	if n, ok := strings.CutPrefix(mux.Vars(r)["driveid"], "Disk"); ok {
		idx, _ = strconv.Atoi(n)
	}
	if !s.hasStorage(r) || idx < 1 || idx > len(s.systemInfo.Disks) {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "drive not found")
		return
	}
	disk := s.systemInfo.Disks[idx-1]
	writeJSON(w, http.StatusOK, Drive{
		ODataType:     "#Drive.v1_7_0.Drive",
		ODataID:       fmt.Sprintf("%s/Disk%d", drivesPath, idx),
		ODataContext:  "/redfish/v1/$metadata#Drive.Drive",
		ID:            fmt.Sprintf("Disk%d", idx),
		Name:          diskName(idx),
		CapacityBytes: disk.CapacityBytes,
		MediaType:     "HDD",
		Protocol:      disk.Protocol,
		Status:        statusOK,
	})
}

func (s *Server) handleSimpleStorageCollection(w http.ResponseWriter, r *http.Request) {
	members := s.storageMembers(simpleStorage1)
	writeJSON(w, http.StatusOK, Collection{
		ODataType:    "#SimpleStorageCollection.SimpleStorageCollection",
		ODataID:      simpleStoragePath,
		Name:         "Simple Storage Collection",
		MembersCount: len(members),
		Members:      members,
	})
}

func (s *Server) handleGetSimpleStorage(w http.ResponseWriter, r *http.Request) {
	if !s.hasStorage(r) {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "simple storage not found")
		return
	}
	devices := make([]SimpleStorageDevice, 0, len(s.systemInfo.Disks))
	for i, disk := range s.systemInfo.Disks {
		devices = append(devices, SimpleStorageDevice{
			Name:          diskName(i + 1),
			CapacityBytes: disk.CapacityBytes,
			Status:        statusOK,
		})
	}
	writeJSON(w, http.StatusOK, SimpleStorage{
		ODataType:    "#SimpleStorage.v1_2_0.SimpleStorage",
		ODataID:      simpleStorage1,
		ODataContext: "/redfish/v1/$metadata#SimpleStorage.SimpleStorage",
		ID:           "1",
		Name:         "Simple Storage Controller",
		Devices:      devices,
		Status:       statusOK,
	})
}
