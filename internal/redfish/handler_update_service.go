package redfish

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/tjst-t/qemu-bmc/internal/firmware"
	"github.com/tjst-t/qemu-bmc/internal/machine"
)

const (
	updateServicePath     = "/redfish/v1/UpdateService"
	firmwareInventoryPath = updateServicePath + "/FirmwareInventory"
	simpleUpdatePath      = updateServicePath + "/Actions/UpdateService.SimpleUpdate"
	multipartPushPath     = updateServicePath + "/upload"

	// DefaultFirmwareUpdateDuration is how long a simulated firmware
	// update takes to go from 0 to 100 percent.
	DefaultFirmwareUpdateDuration = 10 * time.Second

	// updateRegistry is the DMTF Update message registry of the task
	// messages and image errors.
	updateRegistry = "Update.1.0."

	// maxUpdateParameters bounds the UpdateParameters part of a multipart
	// push.
	maxUpdateParameters = 64 << 10
)

// FirmwareStore keeps the firmware versions (implemented by
// *firmware.Store).
type FirmwareStore interface {
	Version(target string) string
	Pending(target string) string
	Install(target, version string) (pending bool, err error)
	OnChange(fn func(target string))
}

// FirmwareFaults injects firmware update faults (implemented by
// *fault.Store).
type FirmwareFaults interface {
	RejectFirmwareImage(target string) bool
	FirmwareUpdateFailure(target string) (atPercent int, ok bool)
}

// firmwareInventory describes the firmware components.
var firmwareInventory = map[string]struct {
	name, description, related string
}{
	firmware.TargetBMC:  {"BMC Firmware", "qemu-bmc BMC firmware (simulated)", managerPath},
	firmware.TargetBIOS: {"BIOS Firmware", "System BIOS firmware (simulated)", systemPath},
}

// SetFirmware makes the firmware versions available: UpdateService with
// its FirmwareInventory, Managers/1 FirmwareVersion and Systems/1
// BiosVersion. Version changes are ResourceChanged events.
func (s *Server) SetFirmware(fw FirmwareStore) {
	s.fw = fw
	fw.OnChange(func(target string) {
		s.publishResourceEvent("ResourceChanged", firmwareInventoryPath+"/"+target)
	})
}

// SetFirmwareFaults makes firmware updates go through the
// firmware-image-rejected and firmware-update-fail faults.
func (s *Server) SetFirmwareFaults(f FirmwareFaults) {
	s.fwFaults = f
}

// SetFirmwareDownload sets how SimpleUpdate images are downloaded.
func (s *Server) SetFirmwareDownload(opts firmware.DownloadOptions) {
	s.fwDownload = opts
}

// SetFirmwareUpdateDuration sets how long a simulated update runs.
func (s *Server) SetFirmwareUpdateDuration(d time.Duration) {
	s.fwDuration = d
}

// firmwareVersion returns the version of target, "" without firmware.
func (s *Server) firmwareVersion(target string) string {
	if s.fw == nil {
		return ""
	}
	return s.fw.Version(target)
}

func (s *Server) firmwareNotFound(w http.ResponseWriter) bool {
	if s.fw == nil {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "the update service is not available")
		return true
	}
	return false
}

// UpdateServiceResource is the Redfish UpdateService.
type UpdateServiceResource struct {
	ODataType            string               `json:"@odata.type"`
	ODataID              string               `json:"@odata.id"`
	ID                   string               `json:"Id"`
	Name                 string               `json:"Name"`
	Description          string               `json:"Description,omitempty"`
	ServiceEnabled       bool                 `json:"ServiceEnabled"`
	Status               ResourceStatus       `json:"Status"`
	FirmwareInventory    ODataID              `json:"FirmwareInventory"`
	MultipartHTTPPushURI string               `json:"MultipartHttpPushUri"`
	MaxImageSizeBytes    int64                `json:"MaxImageSizeBytes"`
	Actions              UpdateServiceActions `json:"Actions"`
}

// UpdateServiceActions are the UpdateService actions.
type UpdateServiceActions struct {
	SimpleUpdate SimpleUpdateAction `json:"#UpdateService.SimpleUpdate"`
}

// SimpleUpdateAction is the SimpleUpdate action with its allowed transfer
// protocols.
type SimpleUpdateAction struct {
	Target            string   `json:"target"`
	TransferProtocols []string `json:"TransferProtocol@Redfish.AllowableValues"`
}

// SoftwareInventory is a FirmwareInventory member.
type SoftwareInventory struct {
	ODataType        string         `json:"@odata.type"`
	ODataID          string         `json:"@odata.id"`
	ID               string         `json:"Id"`
	Name             string         `json:"Name"`
	Description      string         `json:"Description"`
	Version          string         `json:"Version"`
	Updateable       bool           `json:"Updateable"`
	Manufacturer     string         `json:"Manufacturer"`
	SoftwareID       string         `json:"SoftwareId"`
	Status           ResourceStatus `json:"Status"`
	RelatedItem      []ODataID      `json:"RelatedItem"`
	RelatedItemCount int            `json:"RelatedItem@odata.count"`
}

func (s *Server) handleGetUpdateService(w http.ResponseWriter, r *http.Request) {
	if s.firmwareNotFound(w) {
		return
	}
	writeJSON(w, http.StatusOK, UpdateServiceResource{
		ODataType:            "#UpdateService.v1_8_0.UpdateService",
		ODataID:              updateServicePath,
		ID:                   "UpdateService",
		Name:                 "Update Service",
		Description:          "Simulated firmware updates (images: first line 'QBMC-FW <BMC|BIOS> <version>')",
		ServiceEnabled:       true,
		Status:               statusOK,
		FirmwareInventory:    ODataID{ODataID: firmwareInventoryPath},
		MultipartHTTPPushURI: multipartPushPath,
		MaxImageSizeBytes:    firmware.MaxImageBytes,
		Actions: UpdateServiceActions{SimpleUpdate: SimpleUpdateAction{
			Target:            simpleUpdatePath,
			TransferProtocols: []string{"HTTP", "HTTPS"},
		}},
	})
}

func (s *Server) handleFirmwareInventoryCollection(w http.ResponseWriter, r *http.Request) {
	if s.firmwareNotFound(w) {
		return
	}
	members := make([]ODataID, 0, len(firmware.Targets))
	for _, t := range firmware.Targets {
		members = append(members, ODataID{ODataID: firmwareInventoryPath + "/" + t})
	}
	writeJSON(w, http.StatusOK, Collection{
		ODataType:    "#SoftwareInventoryCollection.SoftwareInventoryCollection",
		ODataID:      firmwareInventoryPath,
		Name:         "Firmware Inventory Collection",
		MembersCount: len(members),
		Members:      members,
	})
}

// handleGetFirmwareInventory serves FirmwareInventory/BMC and /BIOS. A
// BIOS version installed but not active until the next boot shows as
// Status.State StandbyOffline ("enabled but awaits an external action to
// activate it") with the pending version in the Description.
func (s *Server) handleGetFirmwareInventory(w http.ResponseWriter, r *http.Request) {
	if s.firmwareNotFound(w) {
		return
	}
	target := mux.Vars(r)["fwid"]
	info, ok := firmwareInventory[target]
	if !ok {
		writeError(w, http.StatusNotFound, "ResourceNotFound", "firmware inventory item not found")
		return
	}
	inv := SoftwareInventory{
		ODataType:        "#SoftwareInventory.v1_2_0.SoftwareInventory",
		ODataID:          firmwareInventoryPath + "/" + target,
		ID:               target,
		Name:             info.name,
		Description:      info.description,
		Version:          s.fw.Version(target),
		Updateable:       true,
		Manufacturer:     "qemu-bmc",
		SoftwareID:       firmware.ImageMagic + "-" + target,
		Status:           statusOK,
		RelatedItem:      []ODataID{{ODataID: info.related}},
		RelatedItemCount: 1,
	}
	if pending := s.fw.Pending(target); pending != "" {
		inv.Status = ResourceStatus{State: "StandbyOffline", Health: "OK"}
		inv.Description = fmt.Sprintf("%s; version %s is installed and becomes active at the next system boot (power on or reset)",
			info.description, pending)
	}
	writeJSON(w, http.StatusOK, inv)
}

// SimpleUpdateRequest is the body of the SimpleUpdate action.
type SimpleUpdateRequest struct {
	ImageURI         string            `json:"ImageURI"`
	TransferProtocol string            `json:"TransferProtocol"`
	Targets          []json.RawMessage `json:"Targets"`
	Username         string            `json:"Username"`
	Password         string            `json:"Password"`
}

// handleSimpleUpdate downloads the image (VirtualMedia download settings),
// checks it and starts the update task: 202 with the task monitor as
// Location. A broken image is rejected with 400 before any task exists.
func (s *Server) handleSimpleUpdate(w http.ResponseWriter, r *http.Request) {
	if s.firmwareNotFound(w) {
		return
	}
	var req SimpleUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "invalid request body")
		return
	}
	if req.ImageURI == "" {
		writeError(w, http.StatusBadRequest, "ActionParameterMissing", "ImageURI is required")
		return
	}
	uri, err := imageURL(req.ImageURI, req.TransferProtocol)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ActionParameterNotSupported", err.Error())
		return
	}
	targets, err := parseUpdateTargets(req.Targets)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ActionParameterValueNotInList", err.Error())
		return
	}

	shown := machine.RedactImage(uri)
	log.Printf("UpdateService: SimpleUpdate: downloading %s", shown)
	img, err := firmware.Download(r.Context(), s.fwDownload, uri, req.Username, req.Password)
	if err != nil {
		log.Printf("UpdateService: SimpleUpdate %s: %v", shown, err)
		if errors.Is(err, firmware.ErrInvalidImage) {
			writeImageRejected(w, shown, targets, err)
			return
		}
		writeUpdateError(w, http.StatusBadRequest, updateMessage("TransferFailed", "Critical", shown, updateServicePath),
			fmt.Sprintf("downloading %s failed: %v", shown, err))
		return
	}
	s.acceptImage(w, img, shown, targets)
}

// imageURL returns the http(s) URL of a SimpleUpdate image. ImageURI
// without a scheme takes it from TransferProtocol; TransferProtocol is
// otherwise only checked to be HTTP or HTTPS (sushy sends HTTP by default,
// also for https URLs).
func imageURL(imageURI, protocol string) (string, error) {
	switch strings.ToUpper(protocol) {
	case "", "HTTP", "HTTPS":
	default:
		return "", fmt.Errorf("TransferProtocol %s is not supported (HTTP or HTTPS)", protocol)
	}
	if !strings.Contains(imageURI, "://") {
		if protocol == "" {
			return "", fmt.Errorf("ImageURI %q has no scheme and no TransferProtocol is given", imageURI)
		}
		imageURI = strings.ToLower(protocol) + "://" + strings.TrimPrefix(imageURI, "//")
	}
	u, err := url.Parse(imageURI)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("ImageURI %q must be an http(s) URL", machine.RedactImage(imageURI))
	}
	return imageURI, nil
}

// parseUpdateTargets reads the Targets of an update: FirmwareInventory/BMC
// or /BIOS URIs (also accepted: Managers/1 for the BMC, Systems/1 for the
// BIOS). Empty means the image decides.
func parseUpdateTargets(raws []json.RawMessage) (map[string]bool, error) {
	uris, err := parseODataRefs(raws)
	if err != nil {
		return nil, fmt.Errorf("Targets: %v", err)
	}
	targets := map[string]bool{}
	for _, uri := range uris {
		switch uri {
		case firmwareInventoryPath + "/" + firmware.TargetBMC, managerPath:
			targets[firmware.TargetBMC] = true
		case firmwareInventoryPath + "/" + firmware.TargetBIOS, systemPath:
			targets[firmware.TargetBIOS] = true
		default:
			return nil, fmt.Errorf("Targets: %s cannot be updated (%s/BMC or %s/BIOS)", uri, firmwareInventoryPath, firmwareInventoryPath)
		}
	}
	return targets, nil
}

// UpdateParameters is the UpdateParameters part of a multipart push.
type UpdateParameters struct {
	Targets            []json.RawMessage `json:"Targets"`
	OperationApplyTime string            `json:"@Redfish.OperationApplyTime"`
}

// handleMultipartPush receives an image as the UpdateFile part of a
// multipart/form-data POST (UpdateParameters: Targets,
// @Redfish.OperationApplyTime Immediate or OnReset; the BIOS always
// activates at the next boot) and starts the update task.
func (s *Server) handleMultipartPush(w http.ResponseWriter, r *http.Request) {
	if s.firmwareNotFound(w) {
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "MalformedJSON", "the request must be multipart/form-data with UpdateParameters and UpdateFile parts")
		return
	}
	var (
		params   UpdateParameters
		img      firmware.Image
		imgErr   error
		haveFile bool
		name     = "UpdateFile"
	)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "MalformedJSON", "invalid multipart body: "+err.Error())
			return
		}
		switch part.FormName() {
		case "UpdateParameters":
			data, err := io.ReadAll(io.LimitReader(part, maxUpdateParameters+1))
			if err == nil && len(data) > maxUpdateParameters {
				err = errors.New("too large")
			}
			if err == nil {
				err = json.Unmarshal(data, &params)
			}
			if err != nil {
				writeError(w, http.StatusBadRequest, "MalformedJSON", "invalid UpdateParameters: "+err.Error())
				return
			}
		case "UpdateFile":
			haveFile = true
			if fn := part.FileName(); fn != "" {
				name = fn
			}
			img, imgErr = firmware.ReadImage(part)
		}
		part.Close()
	}
	if !haveFile {
		writeError(w, http.StatusBadRequest, "ActionParameterMissing", "the UpdateFile part is required")
		return
	}
	switch params.OperationApplyTime {
	case "", "Immediate", "OnReset":
	default:
		writeError(w, http.StatusBadRequest, "ActionParameterNotSupported",
			fmt.Sprintf("@Redfish.OperationApplyTime %s is not supported (Immediate or OnReset)", params.OperationApplyTime))
		return
	}
	targets, err := parseUpdateTargets(params.Targets)
	if err != nil {
		writeError(w, http.StatusBadRequest, "ActionParameterValueNotInList", err.Error())
		return
	}
	if imgErr != nil {
		log.Printf("UpdateService: multipart push %s: %v", name, imgErr)
		if errors.Is(imgErr, firmware.ErrInvalidImage) {
			writeImageRejected(w, name, targets, imgErr)
			return
		}
		writeError(w, http.StatusBadRequest, "MalformedJSON", "reading UpdateFile: "+imgErr.Error())
		return
	}
	s.acceptImage(w, img, name, targets)
}

// acceptImage checks a received image against the Targets and the
// firmware-image-rejected faults, and starts its update task.
func (s *Server) acceptImage(w http.ResponseWriter, img firmware.Image, name string, targets map[string]bool) {
	if len(targets) > 0 && !targets[img.Target] {
		writeImageRejected(w, name, targets, fmt.Errorf("%w: the image is for %s, not the Targets", firmware.ErrInvalidImage, img.Target))
		return
	}
	if s.fwFaults != nil && s.fwFaults.RejectFirmwareImage(img.Target) {
		writeImageRejected(w, name, map[string]bool{img.Target: true}, fmt.Errorf("%w: rejected (injected fault)", firmware.ErrInvalidImage))
		return
	}
	t, err := s.startUpdate(img, name)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "ServiceTemporarilyUnavailable", err.Error())
		return
	}
	log.Printf("UpdateService: task %s: %s %s from %s", t.id, img.Target, img.Version, name)
	writeTaskAccepted(w, t)
}

// writeImageRejected answers 400 for a broken image (Update
// VerificationFailed).
func writeImageRejected(w http.ResponseWriter, name string, targets map[string]bool, reason error) {
	at := updateServicePath
	for _, t := range firmware.Targets {
		if targets[t] {
			at = firmwareInventoryPath + "/" + t
			break
		}
	}
	msg := updateMessage("VerificationFailed", "Critical", name, at)
	msg.Resolution = "Provide a qemu-bmc firmware image: its first line is 'QBMC-FW <BMC|BIOS> <version>', matching the Targets."
	writeUpdateError(w, http.StatusBadRequest, msg, msg.Message+" "+reason.Error())
}

// writeUpdateError writes a Redfish error with one Update registry message.
func writeUpdateError(w http.ResponseWriter, status int, msg Message, text string) {
	writeJSON(w, status, RedfishError{Error: RedfishErrorBody{
		Code:         baseRegistry + "GeneralError",
		Message:      text,
		ExtendedInfo: []Message{msg},
	}})
}

// updateMessage returns an Update registry message.
func updateMessage(key, severity string, args ...string) Message {
	text := map[string]string{
		"TargetDetermined":      "The target device '%1' will be updated with image '%2'.",
		"InstallingOnComponent": "Image '%1' is being installed on '%2'.",
		"TransferFailed":        "Transfer of image '%1' to '%2' failed.",
		"VerificationFailed":    "Verification of image '%1' at '%2' failed.",
		"ApplyFailed":           "Installation of image '%1' to '%2' failed.",
		"AwaitToActivate":       "Awaiting for an action to proceed with activating image '%1' on '%2'.",
		"UpdateSuccessful":      "Device '%1' successfully updated with image '%2'.",
	}[key]
	for i, a := range args {
		text = strings.ReplaceAll(text, fmt.Sprintf("%%%d", i+1), a)
	}
	return Message{
		ODataType:       "#Message.v1_1_1.Message",
		MessageID:       updateRegistry + key,
		Message:         text,
		MessageArgs:     args,
		MessageSeverity: severity,
	}
}

// startUpdate creates the update task of an accepted image and runs it in
// the background.
func (s *Server) startUpdate(img firmware.Image, name string) (task, error) {
	inv := firmwareInventoryPath + "/" + img.Target
	t, err := s.tasks.create(fmt.Sprintf("Firmware update of %s to %s", img.Target, img.Version),
		updateMessage("TargetDetermined", "OK", inv, name))
	if err != nil {
		return task{}, err
	}
	failAt, fail := 0, false
	if s.fwFaults != nil {
		failAt, fail = s.fwFaults.FirmwareUpdateFailure(img.Target)
	}
	go s.runUpdate(t.id, img, name, failAt, fail)
	return t, nil
}

// runUpdate simulates the update: PercentComplete goes from 0 to 100 over
// the update duration, then the version is installed (the BIOS one
// pending until the next boot). With fail, the task ends in Exception at
// failAt percent and nothing is installed.
func (s *Server) runUpdate(id string, img firmware.Image, name string, failAt int, fail bool) {
	inv := firmwareInventoryPath + "/" + img.Target
	s.updateTask(id, func(t *task) {
		t.state = taskRunning
		t.messages = append(t.messages, updateMessage("InstallingOnComponent", "OK", name, inv))
	})
	step := s.fwDuration / 100
	for p := 0; ; p++ {
		if fail && p >= failAt {
			log.Printf("UpdateService: task %s: %s update failed at %d%% (injected fault)", id, img.Target, p)
			s.failUpdate(id, p, updateMessage("ApplyFailed", "Critical", name, inv))
			return
		}
		if p == 100 {
			break
		}
		if step > 0 {
			time.Sleep(step)
		}
		s.updateTask(id, func(t *task) { t.percent = p + 1 })
	}

	pending, err := s.fw.Install(img.Target, img.Version)
	if err != nil {
		log.Printf("UpdateService: task %s: %v", id, err)
		s.failUpdate(id, 100, updateMessage("ApplyFailed", "Critical", name, inv))
		return
	}
	done := updateMessage("UpdateSuccessful", "OK", inv, name)
	if pending {
		done = updateMessage("AwaitToActivate", "OK", name, inv)
		done.Resolution = fmt.Sprintf("Version %s becomes active at the next system boot: power on, ForceRestart, PowerCycle or GracefulRestart.", img.Version)
	}
	log.Printf("UpdateService: task %s: %s %s installed (pending until the next boot: %t)", id, img.Target, img.Version, pending)
	s.updateTask(id, func(t *task) {
		t.state = taskCompleted
		t.percent = 100
		t.messages = append(t.messages, done)
	})
}

func (s *Server) failUpdate(id string, percent int, msg Message) {
	s.updateTask(id, func(t *task) {
		t.state = taskException
		t.status = "Critical"
		t.percent = percent
		t.messages = append(t.messages, msg)
	})
}
