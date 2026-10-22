package redfish

import (
	"net/http"

	"github.com/gorilla/mux"
)

const (
	processorsPath = systemPath + "/Processors"
	cpuPath        = processorsPath + "/CPU1"
)

// handleProcessorCollection serves a single CPU1 holding all vCPUs, or no
// members when the CPU count is unknown.
func (s *Server) handleProcessorCollection(w http.ResponseWriter, r *http.Request) {
	members := []ODataID{}
	if s.systemInfo.CPUs > 0 {
		members = append(members, ODataID{ODataID: cpuPath})
	}
	writeJSON(w, http.StatusOK, Collection{
		ODataType:    "#ProcessorCollection.ProcessorCollection",
		ODataID:      processorsPath,
		Name:         "Processor Collection",
		MembersCount: len(members),
		Members:      members,
	})
}

func (s *Server) handleGetProcessor(w http.ResponseWriter, r *http.Request) {
	if s.systemInfo.CPUs <= 0 || mux.Vars(r)["procid"] != "CPU1" {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "processor not found")
		return
	}
	writeJSON(w, http.StatusOK, Processor{
		ODataType:             "#Processor.v1_3_0.Processor",
		ODataID:               cpuPath,
		ODataContext:          "/redfish/v1/$metadata#Processor.Processor",
		ID:                    "CPU1",
		Name:                  "QEMU Virtual CPU",
		ProcessorType:         "CPU",
		ProcessorArchitecture: "x86",
		InstructionSet:        "x86-64",
		TotalCores:            s.systemInfo.CPUs,
		TotalThreads:          s.systemInfo.CPUs,
		Status:                statusOK,
	})
}
