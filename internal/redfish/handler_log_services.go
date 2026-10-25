package redfish

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
)

const (
	logServicesPath        = systemPath + "/LogServices"
	serialLogServicePath   = logServicesPath + "/SerialConsole"
	serialLogEntriesPath   = serialLogServicePath + "/Entries"
	serialLogEntryPath     = serialLogEntriesPath + "/1"
	serialLogAttachmentURI = serialLogEntryPath + "/attachment"
)

// SerialLog is the recent VM serial console output kept by qemu-bmc
// (implemented by *serial.Mux).
type SerialLog interface {
	Recent() []byte
}

// SetSerialLog makes the recent serial console output readable as the
// SerialConsole log service of Systems/1 (process management mode only;
// without it the LogServices collection is empty).
func (s *Server) SetSerialLog(l SerialLog) {
	s.serialLog = l
}

// LogService is a Redfish log service
type LogService struct {
	ODataType           string         `json:"@odata.type"`
	ODataID             string         `json:"@odata.id"`
	ODataContext        string         `json:"@odata.context,omitempty"`
	ID                  string         `json:"Id"`
	Name                string         `json:"Name"`
	Description         string         `json:"Description"`
	LogEntryType        string         `json:"LogEntryType"`
	MaxNumberOfRecords  int            `json:"MaxNumberOfRecords"`
	OverWritePolicy     string         `json:"OverWritePolicy"`
	ServiceEnabled      bool           `json:"ServiceEnabled"`
	DateTime            string         `json:"DateTime"`
	DateTimeLocalOffset string         `json:"DateTimeLocalOffset"`
	Entries             ODataID        `json:"Entries"`
	Status              ResourceStatus `json:"Status"`
}

// LogEntry is an entry of a log service
type LogEntry struct {
	ODataType               string `json:"@odata.type"`
	ODataID                 string `json:"@odata.id"`
	ODataContext            string `json:"@odata.context,omitempty"`
	ID                      string `json:"Id"`
	Name                    string `json:"Name"`
	EntryType               string `json:"EntryType"`
	OemRecordFormat         string `json:"OemRecordFormat"`
	Severity                string `json:"Severity"`
	Created                 string `json:"Created"`
	Message                 string `json:"Message"`
	AdditionalDataURI       string `json:"AdditionalDataURI"`
	AdditionalDataSizeBytes int    `json:"AdditionalDataSizeBytes"`
}

func (s *Server) handleLogServiceCollection(w http.ResponseWriter, r *http.Request) {
	members := []ODataID{}
	if s.serialLog != nil {
		members = append(members, ODataID{ODataID: serialLogServicePath})
	}
	writeJSON(w, http.StatusOK, Collection{
		ODataType:    "#LogServiceCollection.LogServiceCollection",
		ODataID:      logServicesPath,
		Name:         "Log Service Collection",
		MembersCount: len(members),
		Members:      members,
	})
}

// serialLogFound writes 404 unless the serial console log exists.
func (s *Server) serialLogFound(w http.ResponseWriter, r *http.Request) bool {
	if s.serialLog == nil || mux.Vars(r)["logid"] != "SerialConsole" {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "log service not found")
		return false
	}
	if entry, ok := mux.Vars(r)["entryid"]; ok && entry != "1" {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "log entry not found")
		return false
	}
	return true
}

// handleGetSerialLogService serves the SerialConsole log service. The log
// has a single entry whose AdditionalDataURI is the buffered output: a
// LogEntry per line or chunk would be far too large.
func (s *Server) handleGetSerialLogService(w http.ResponseWriter, r *http.Request) {
	if !s.serialLogFound(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, LogService{
		ODataType:           "#LogService.v1_1_0.LogService",
		ODataID:             serialLogServicePath,
		ODataContext:        "/redfish/v1/$metadata#LogService.LogService",
		ID:                  "SerialConsole",
		Name:                "Serial Console Log",
		Description:         "Recent output of the VM serial console; entry 1 links to it as text",
		LogEntryType:        "OEM",
		MaxNumberOfRecords:  1,
		OverWritePolicy:     "WrapsWhenFull",
		ServiceEnabled:      true,
		DateTime:            time.Now().UTC().Format(time.RFC3339),
		DateTimeLocalOffset: "+00:00",
		Entries:             ODataID{ODataID: serialLogEntriesPath},
		Status:              statusOK,
	})
}

// LogEntryCollection is a collection of log entries. The entries are
// contained resources, so they are expanded in Members.
type LogEntryCollection struct {
	ODataType    string     `json:"@odata.type"`
	ODataID      string     `json:"@odata.id"`
	Name         string     `json:"Name"`
	MembersCount int        `json:"Members@odata.count"`
	Members      []LogEntry `json:"Members"`
}

func (s *Server) handleSerialLogEntryCollection(w http.ResponseWriter, r *http.Request) {
	if !s.serialLogFound(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, LogEntryCollection{
		ODataType:    "#LogEntryCollection.LogEntryCollection",
		ODataID:      serialLogEntriesPath,
		Name:         "Serial Console Log Entries",
		MembersCount: 1,
		Members:      []LogEntry{s.serialLogEntry()},
	})
}

func (s *Server) handleGetSerialLogEntry(w http.ResponseWriter, r *http.Request) {
	if !s.serialLogFound(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, s.serialLogEntry())
}

// serialLogEntry is the single entry of the SerialConsole log: the
// buffered output is its additional data.
func (s *Server) serialLogEntry() LogEntry {
	size := len(s.serialLog.Recent())
	return LogEntry{
		ODataType:               "#LogEntry.v1_7_0.LogEntry",
		ODataID:                 serialLogEntryPath,
		ODataContext:            "/redfish/v1/$metadata#LogEntry.LogEntry",
		ID:                      "1",
		Name:                    "Serial Console Output",
		EntryType:               "Oem",
		OemRecordFormat:         "SerialConsoleText",
		Severity:                "OK",
		Created:                 time.Now().UTC().Format(time.RFC3339),
		Message:                 "Last " + strconv.Itoa(size) + " bytes of the VM serial console output",
		AdditionalDataURI:       serialLogAttachmentURI,
		AdditionalDataSizeBytes: size,
	}
}

// handleGetSerialLogAttachment returns the buffered serial console output
// as is (terminal escapes included).
func (s *Server) handleGetSerialLogAttachment(w http.ResponseWriter, r *http.Request) {
	if !s.serialLogFound(w, r) {
		return
	}
	out := s.serialLog.Recent()
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))
	w.WriteHeader(http.StatusOK)
	w.Write(out)
}
