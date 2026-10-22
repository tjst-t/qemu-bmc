package redfish

import "encoding/json"

// ODataID represents an OData reference
type ODataID struct {
	ODataID string `json:"@odata.id"`
}

// ServiceRoot is the Redfish service root
type ServiceRoot struct {
	ODataType      string           `json:"@odata.type"`
	ODataID        string           `json:"@odata.id"`
	ODataContext   string           `json:"@odata.context,omitempty"`
	ID             string           `json:"Id"`
	Name           string           `json:"Name"`
	RedfishVersion string           `json:"RedfishVersion"`
	Systems        ODataID          `json:"Systems"`
	Managers       ODataID          `json:"Managers"`
	Chassis        ODataID          `json:"Chassis"`
	SessionService ODataID          `json:"SessionService"`
	Links          ServiceRootLinks `json:"Links"`
}

// ServiceRootLinks are the service root links
type ServiceRootLinks struct {
	Sessions ODataID `json:"Sessions"`
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
	UUID         string                `json:"UUID,omitempty"`
	PowerState   string                `json:"PowerState"`
	Boot         BootSource            `json:"Boot"`
	Actions      ComputerSystemActions `json:"Actions"`

	MemorySummary      *MemorySummary    `json:"MemorySummary,omitempty"`
	ProcessorSummary   *ProcessorSummary `json:"ProcessorSummary,omitempty"`
	Processors         ODataID           `json:"Processors"`
	Storage            ODataID           `json:"Storage"`
	SimpleStorage      ODataID           `json:"SimpleStorage"`
	EthernetInterfaces ODataID           `json:"EthernetInterfaces"`
	VirtualMedia       ODataID           `json:"VirtualMedia"`
	Status             ResourceStatus    `json:"Status"`
	Links              SystemLinks       `json:"Links"`
}

// SystemLinks are the links from a computer system to its manager and chassis
type SystemLinks struct {
	ManagedBy []ODataID `json:"ManagedBy"`
	Chassis   []ODataID `json:"Chassis"`
}

// MemorySummary describes the system memory
type MemorySummary struct {
	TotalSystemMemoryGiB float64 `json:"TotalSystemMemoryGiB"`
}

// ProcessorSummary describes the system processors
type ProcessorSummary struct {
	Count int `json:"Count"`
}

// EthernetInterfaceCollection is a collection of system NICs
type EthernetInterfaceCollection struct {
	ODataType    string    `json:"@odata.type"`
	ODataID      string    `json:"@odata.id"`
	Name         string    `json:"Name"`
	MembersCount int       `json:"Members@odata.count"`
	Members      []ODataID `json:"Members"`
}

// EthernetInterface represents a system NIC
type EthernetInterface struct {
	ODataType           string         `json:"@odata.type"`
	ODataID             string         `json:"@odata.id"`
	ODataContext        string         `json:"@odata.context,omitempty"`
	ID                  string         `json:"Id"`
	Name                string         `json:"Name"`
	MACAddress          string         `json:"MACAddress,omitempty"`
	PermanentMACAddress string         `json:"PermanentMACAddress,omitempty"`
	LinkStatus          string         `json:"LinkStatus"`
	Status              ResourceStatus `json:"Status"`
}

// ResourceStatus is the common Redfish Status object
type ResourceStatus struct {
	State  string `json:"State"`
	Health string `json:"Health"`
}

// BootSource represents boot source override
type BootSource struct {
	BootSourceOverrideEnabled string   `json:"BootSourceOverrideEnabled"`
	BootSourceOverrideTarget  string   `json:"BootSourceOverrideTarget"`
	BootSourceOverrideMode    string   `json:"BootSourceOverrideMode"`
	AllowableValues           []string `json:"BootSourceOverrideTarget@Redfish.AllowableValues"`
	// HttpBootUri is always present (null when unset): sushy only sends it
	// when the key exists
	HttpBootUri *string `json:"HttpBootUri"`
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
	// HttpBootUri is kept raw to tell null (clear) from absent (no change)
	HttpBootUri json.RawMessage `json:"HttpBootUri,omitempty"`
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

// ManagerCollection is a collection of managers
type ManagerCollection struct {
	ODataType    string    `json:"@odata.type"`
	ODataID      string    `json:"@odata.id"`
	Name         string    `json:"Name"`
	MembersCount int       `json:"Members@odata.count"`
	Members      []ODataID `json:"Members"`
}

// Manager represents a BMC manager
type Manager struct {
	ODataType       string         `json:"@odata.type"`
	ODataID         string         `json:"@odata.id"`
	ODataContext    string         `json:"@odata.context,omitempty"`
	ID              string         `json:"Id"`
	Name            string         `json:"Name"`
	ManagerType     string         `json:"ManagerType"`
	FirmwareVersion string         `json:"FirmwareVersion,omitempty"`
	Status          ResourceStatus `json:"Status"`
	VirtualMedia    ODataID        `json:"VirtualMedia"`
	Links           ManagerLinks   `json:"Links"`
}

// ManagerLinks are the links from a manager to the system and chassis it manages
type ManagerLinks struct {
	ManagerForServers []ODataID `json:"ManagerForServers"`
	ManagerInChassis  ODataID   `json:"ManagerInChassis"`
}

// VirtualMediaCollection is a collection of virtual media
type VirtualMediaCollection struct {
	ODataType    string    `json:"@odata.type"`
	ODataID      string    `json:"@odata.id"`
	Name         string    `json:"Name"`
	MembersCount int       `json:"Members@odata.count"`
	Members      []ODataID `json:"Members"`
}

// VirtualMedia represents a virtual media resource
type VirtualMedia struct {
	ODataType      string              `json:"@odata.type"`
	ODataID        string              `json:"@odata.id"`
	ODataContext   string              `json:"@odata.context,omitempty"`
	ID             string              `json:"Id"`
	Name           string              `json:"Name"`
	MediaTypes     []string            `json:"MediaTypes"`
	Image          string              `json:"Image"`
	ImageName      string              `json:"ImageName,omitempty"`
	Inserted       bool                `json:"Inserted"`
	WriteProtected bool                `json:"WriteProtected"`
	ConnectedVia   string              `json:"ConnectedVia"`
	Actions        VirtualMediaActions `json:"Actions"`
}

// VirtualMediaActions contains available actions for virtual media
type VirtualMediaActions struct {
	InsertMedia VirtualMediaAction `json:"#VirtualMedia.InsertMedia"`
	EjectMedia  VirtualMediaAction `json:"#VirtualMedia.EjectMedia"`
}

// VirtualMediaAction describes a virtual media action
type VirtualMediaAction struct {
	Target string `json:"target"`
}

// InsertMediaRequest is the request body for inserting virtual media
type InsertMediaRequest struct {
	Image    string `json:"Image"`
	Inserted bool   `json:"Inserted"`
	// UserName and Password authenticate an http(s) Image download
	UserName string `json:"UserName"`
	Password string `json:"Password"`
}

// ChassisCollection is a collection of chassis
type ChassisCollection struct {
	ODataType    string    `json:"@odata.type"`
	ODataID      string    `json:"@odata.id"`
	Name         string    `json:"Name"`
	MembersCount int       `json:"Members@odata.count"`
	Members      []ODataID `json:"Members"`
}

// Chassis represents a chassis resource
type Chassis struct {
	ODataType    string         `json:"@odata.type"`
	ODataID      string         `json:"@odata.id"`
	ODataContext string         `json:"@odata.context,omitempty"`
	ID           string         `json:"Id"`
	Name         string         `json:"Name"`
	ChassisType  string         `json:"ChassisType"`
	PowerState   string         `json:"PowerState"`
	Status       ResourceStatus `json:"Status"`
	Drives       *ODataID       `json:"Drives,omitempty"`
	Links        ChassisLinks   `json:"Links"`
}

// ChassisLinks are the links from a chassis to the system and manager in it
type ChassisLinks struct {
	ComputerSystems []ODataID `json:"ComputerSystems"`
	ManagedBy       []ODataID `json:"ManagedBy"`
}

// Collection is a generic Redfish resource collection
type Collection struct {
	ODataType    string    `json:"@odata.type"`
	ODataID      string    `json:"@odata.id"`
	Name         string    `json:"Name"`
	MembersCount int       `json:"Members@odata.count"`
	Members      []ODataID `json:"Members"`
}

// Processor represents a system processor
type Processor struct {
	ODataType             string         `json:"@odata.type"`
	ODataID               string         `json:"@odata.id"`
	ODataContext          string         `json:"@odata.context,omitempty"`
	ID                    string         `json:"Id"`
	Name                  string         `json:"Name"`
	ProcessorType         string         `json:"ProcessorType"`
	ProcessorArchitecture string         `json:"ProcessorArchitecture"`
	InstructionSet        string         `json:"InstructionSet"`
	TotalCores            int            `json:"TotalCores"`
	TotalThreads          int            `json:"TotalThreads"`
	Status                ResourceStatus `json:"Status"`
}

// Storage represents a storage subsystem and its drives
type Storage struct {
	ODataType    string         `json:"@odata.type"`
	ODataID      string         `json:"@odata.id"`
	ODataContext string         `json:"@odata.context,omitempty"`
	ID           string         `json:"Id"`
	Name         string         `json:"Name"`
	Drives       []ODataID      `json:"Drives"`
	DrivesCount  int            `json:"Drives@odata.count"`
	Volumes      ODataID        `json:"Volumes"`
	Status       ResourceStatus `json:"Status"`
}

// Volume represents a logical volume; each guest disk is reported as one
// volume on its drive (no RAID).
type Volume struct {
	ODataType     string         `json:"@odata.type"`
	ODataID       string         `json:"@odata.id"`
	ODataContext  string         `json:"@odata.context,omitempty"`
	ID            string         `json:"Id"`
	Name          string         `json:"Name"`
	CapacityBytes int64          `json:"CapacityBytes,omitempty"`
	Status        ResourceStatus `json:"Status"`
	Links         VolumeLinks    `json:"Links"`
}

// VolumeLinks links a volume to the drives backing it
type VolumeLinks struct {
	Drives      []ODataID `json:"Drives"`
	DrivesCount int       `json:"Drives@odata.count"`
}

// Drive represents a disk drive
type Drive struct {
	ODataType     string         `json:"@odata.type"`
	ODataID       string         `json:"@odata.id"`
	ODataContext  string         `json:"@odata.context,omitempty"`
	ID            string         `json:"Id"`
	Name          string         `json:"Name"`
	CapacityBytes int64          `json:"CapacityBytes,omitempty"`
	MediaType     string         `json:"MediaType"`
	Protocol      string         `json:"Protocol,omitempty"`
	Status        ResourceStatus `json:"Status"`
}

// SimpleStorage represents a simple storage controller and its devices
type SimpleStorage struct {
	ODataType    string                `json:"@odata.type"`
	ODataID      string                `json:"@odata.id"`
	ODataContext string                `json:"@odata.context,omitempty"`
	ID           string                `json:"Id"`
	Name         string                `json:"Name"`
	Devices      []SimpleStorageDevice `json:"Devices"`
	Status       ResourceStatus        `json:"Status"`
}

// SimpleStorageDevice is a device of a simple storage controller
type SimpleStorageDevice struct {
	Name          string         `json:"Name"`
	CapacityBytes int64          `json:"CapacityBytes,omitempty"`
	Status        ResourceStatus `json:"Status"`
}
