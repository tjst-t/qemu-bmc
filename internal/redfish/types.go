package redfish

// ODataID represents an OData reference
type ODataID struct {
	ODataID string `json:"@odata.id"`
}

// ServiceRoot is the Redfish service root
type ServiceRoot struct {
	ODataType      string  `json:"@odata.type"`
	ODataID        string  `json:"@odata.id"`
	ODataContext   string  `json:"@odata.context,omitempty"`
	ID             string  `json:"Id"`
	Name           string  `json:"Name"`
	RedfishVersion string  `json:"RedfishVersion"`
	Systems        ODataID `json:"Systems"`
	Managers       ODataID `json:"Managers"`
	Chassis        ODataID `json:"Chassis"`
}

// SystemCollection is a collection of computer systems
type SystemCollection struct {
	ODataType    string    `json:"@odata.type"`
	ODataID      string    `json:"@odata.id"`
	Name         string    `json:"Name"`
	MembersCount int       `json:"Members@odata.count"`
	Members      []ODataID `json:"Members"`
}

// ComputerSystem represents a computer system
type ComputerSystem struct {
	ODataType    string                `json:"@odata.type"`
	ODataID      string                `json:"@odata.id"`
	ODataContext string                `json:"@odata.context,omitempty"`
	ODataEtag    string                `json:"@odata.etag,omitempty"`
	ID           string                `json:"Id"`
	Name         string                `json:"Name"`
	PowerState   string                `json:"PowerState"`
	Boot         BootSource            `json:"Boot"`
	Actions      ComputerSystemActions `json:"Actions"`
}

// BootSource represents boot source override
type BootSource struct {
	BootSourceOverrideEnabled string   `json:"BootSourceOverrideEnabled"`
	BootSourceOverrideTarget  string   `json:"BootSourceOverrideTarget"`
	BootSourceOverrideMode    string   `json:"BootSourceOverrideMode"`
	AllowableValues           []string `json:"BootSourceOverrideTarget@Redfish.AllowableValues"`
}

// ComputerSystemActions contains available actions
type ComputerSystemActions struct {
	Reset ResetAction `json:"#ComputerSystem.Reset"`
}

// ResetAction describes the reset action
type ResetAction struct {
	Target          string   `json:"target"`
	AllowableValues []string `json:"ResetType@Redfish.AllowableValues"`
}

// ResetRequest is the request body for reset action
type ResetRequest struct {
	ResetType string `json:"ResetType"`
}

// PatchSystemRequest is the request body for patching a system
type PatchSystemRequest struct {
	Boot *PatchBootSource `json:"Boot,omitempty"`
}

// PatchBootSource is the boot source in a patch request
type PatchBootSource struct {
	BootSourceOverrideEnabled string `json:"BootSourceOverrideEnabled,omitempty"`
	BootSourceOverrideTarget  string `json:"BootSourceOverrideTarget,omitempty"`
	BootSourceOverrideMode    string `json:"BootSourceOverrideMode,omitempty"`
}

// RedfishError is a Redfish error response
type RedfishError struct {
	Error RedfishErrorBody `json:"error"`
}

// RedfishErrorBody is the body of a Redfish error
type RedfishErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
