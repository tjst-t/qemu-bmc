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
	Registries     ODataID `json:"Registries"`
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
	ODataType     string                `json:"@odata.type"`
	ODataID       string                `json:"@odata.id"`
	ODataContext  string                `json:"@odata.context,omitempty"`
	ODataEtag     string                `json:"@odata.etag,omitempty"`
	ID            string                `json:"Id"`
	Name          string                `json:"Name"`
	UUID          string                `json:"UUID,omitempty"`
	Manufacturer  string                `json:"Manufacturer,omitempty"`
	Model         string                `json:"Model,omitempty"`
	SKU           string                `json:"SKU,omitempty"`
	SerialNumber  string                `json:"SerialNumber,omitempty"`
	BiosVersion   string                `json:"BiosVersion,omitempty"`
	IndicatorLED  string                `json:"IndicatorLED,omitempty"`
	PowerState    string                `json:"PowerState"`
	Boot          BootSource            `json:"Boot"`
	MemorySummary MemorySummary         `json:"MemorySummary"`
	Processors    ODataID               `json:"Processors"`
	Bios          ODataID               `json:"Bios,omitempty"`
	Actions       ComputerSystemActions `json:"Actions"`
	Links         ComputerSystemLinks   `json:"Links"`
}

// MemorySummary describes the central memory for a ComputerSystem.
type MemorySummary struct {
	TotalSystemMemoryGiB float64 `json:"TotalSystemMemoryGiB"`
}

// ComputerSystemLinks holds related-resource references for a ComputerSystem.
// Ironic's redfish inspect interface locates the managing BMC via Links/ManagedBy
// and refuses to start inspection if it is absent ("The attribute Links/ManagedBy
// is missing from the resource /redfish/v1/Systems/1"). Real BMCs always populate it.
type ComputerSystemLinks struct {
	ManagedBy []ODataID `json:"ManagedBy"`
}

// BootSource represents boot source override plus the persistent BootOrder.
type BootSource struct {
	BootSourceOverrideEnabled string   `json:"BootSourceOverrideEnabled"`
	BootSourceOverrideTarget  string   `json:"BootSourceOverrideTarget"`
	BootSourceOverrideMode    string   `json:"BootSourceOverrideMode"`
	AllowableValues           []string `json:"BootSourceOverrideTarget@Redfish.AllowableValues"`
	// BootOrder is the persistent boot order (Redfish Boot.BootOrder). Unlike the
	// one-time override above it survives resets/power cycles. metal-operator's
	// ServerReconciler.applyBootOrder reads it back and rewrites it via SetBoot.
	BootOrder []string `json:"BootOrder"`
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
	Boot         *PatchBootSource `json:"Boot,omitempty"`
	IndicatorLED *string          `json:"IndicatorLED,omitempty"`
}

// PatchBootSource is the boot source in a patch request. gofish's SetBoot sends
// BootOrder together with BootSourceOverrideEnabled:"Continuous" /
// BootSourceOverrideTarget:"None"; handlePatchSystem treats a request carrying
// BootOrder as a boot-order-only change.
type PatchBootSource struct {
	BootSourceOverrideEnabled string   `json:"BootSourceOverrideEnabled,omitempty"`
	BootSourceOverrideTarget  string   `json:"BootSourceOverrideTarget,omitempty"`
	BootSourceOverrideMode    string   `json:"BootSourceOverrideMode,omitempty"`
	BootOrder                 []string `json:"BootOrder,omitempty"`
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
	Manufacturer    string         `json:"Manufacturer,omitempty"`
	Model           string         `json:"Model,omitempty"`
	SerialNumber    string         `json:"SerialNumber,omitempty"`
	PartNumber      string         `json:"PartNumber,omitempty"`
	FirmwareVersion string         `json:"FirmwareVersion,omitempty"`
	LastResetTime   string         `json:"LastResetTime,omitempty"`
	PowerState      string         `json:"PowerState,omitempty"`
	Status          ResourceStatus `json:"Status,omitempty"`
	VirtualMedia    ODataID        `json:"VirtualMedia"`
	Actions         ManagerActions `json:"Actions"`
	Links           *ManagerLinks  `json:"Links,omitempty"`
}

// ManagerActions contains available actions for a Manager.
type ManagerActions struct {
	Reset ResetAction `json:"#Manager.Reset"`
}

// ManagerLinks / ManagerLinksOem / ManagerLinksOemDell surface the Dell iDRAC
// OEM link that metal-operator's DellRedfishBMC follows from Manager.Links.Oem.Dell
// to locate the writable BMC attribute objects (see getCurrentBMCSettingAttribute
// in metal-operator's bmc/redfish_dell.go).
type ManagerLinks struct {
	Oem ManagerLinksOem `json:"Oem"`
}

// ManagerLinksOem wraps vendor-specific manager links.
type ManagerLinksOem struct {
	Dell ManagerLinksOemDell `json:"Dell"`
}

// ManagerLinksOemDell lists the DellAttributes objects that hold writable BMC
// attributes. qemu-bmc exposes a single one at /redfish/v1/Managers/1/Attributes.
type ManagerLinksOemDell struct {
	DellAttributes      []ODataID `json:"DellAttributes"`
	DellAttributesCount int       `json:"DellAttributes@odata.count"`
}

// DellManagerAttributes represents the live BMC ("Manager") attribute resource
// (/Managers/1/Attributes) or its pending-settings object
// (/Managers/1/Attributes/Settings). Both share this shape, differing only in
// Id/Attributes and whether @Redfish.Settings is populated — mirroring the Bios
// type. metal-operator's Dell client reads Attributes for current values and
// PATCHes the @Redfish.Settings.SettingsObject to change them.
type DellManagerAttributes struct {
	ODataType         string           `json:"@odata.type"`
	ODataID           string           `json:"@odata.id"`
	ID                string           `json:"Id"`
	Name              string           `json:"Name"`
	AttributeRegistry string           `json:"AttributeRegistry,omitempty"`
	Attributes        map[string]any   `json:"Attributes"`
	RedfishSettings   *RedfishSettings `json:"@Redfish.Settings,omitempty"`
}

// PatchManagerAttributesRequest is the request body for PATCHing
// /Managers/1/Attributes/Settings. The optional @Redfish.SettingsApplyTime block
// that Dell clients send is accepted and ignored (qemu-bmc always applies
// immediately).
type PatchManagerAttributesRequest struct {
	Attributes map[string]any `json:"Attributes"`
}

// ResourceStatus is the standard Redfish Status object (State/Health). Clients
// like metal-operator map Manager.Status.State onto the BMC's state.
type ResourceStatus struct {
	State  string `json:"State,omitempty"`
	Health string `json:"Health,omitempty"`
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
	ODataType    string              `json:"@odata.type"`
	ODataID      string              `json:"@odata.id"`
	ODataContext string              `json:"@odata.context,omitempty"`
	ID           string              `json:"Id"`
	Name         string              `json:"Name"`
	MediaTypes   []string            `json:"MediaTypes"`
	Image        string              `json:"Image,omitempty"`
	Inserted     bool                `json:"Inserted"`
	ConnectedVia string              `json:"ConnectedVia,omitempty"`
	Actions      VirtualMediaActions `json:"Actions"`
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
	ODataType    string `json:"@odata.type"`
	ODataID      string `json:"@odata.id"`
	ODataContext string `json:"@odata.context,omitempty"`
	ID           string `json:"Id"`
	Name         string `json:"Name"`
	ChassisType  string `json:"ChassisType"`
}

// ProcessorCollection is a collection of processors
type ProcessorCollection struct {
	ODataType    string    `json:"@odata.type"`
	ODataID      string    `json:"@odata.id"`
	Name         string    `json:"Name"`
	MembersCount int       `json:"Members@odata.count"`
	Members      []ODataID `json:"Members"`
}

// Processor represents a single processor resource
type Processor struct {
	ODataType             string `json:"@odata.type"`
	ODataID               string `json:"@odata.id"`
	ODataContext          string `json:"@odata.context,omitempty"`
	ID                    string `json:"Id"`
	Name                  string `json:"Name"`
	ProcessorType         string `json:"ProcessorType"`
	ProcessorArchitecture string `json:"ProcessorArchitecture"`
	InstructionSet        string `json:"InstructionSet"`
	Manufacturer          string `json:"Manufacturer"`
	Model                 string `json:"Model"`
	MaxSpeedMHz           *int   `json:"MaxSpeedMHz,omitempty"`
	TotalCores            *int   `json:"TotalCores,omitempty"`
	TotalThreads          *int   `json:"TotalThreads,omitempty"`
}

// RegistryFileCollection is a collection of Redfish message/attribute registries.
type RegistryFileCollection struct {
	ODataType    string    `json:"@odata.type"`
	ODataID      string    `json:"@odata.id"`
	Name         string    `json:"Name"`
	MembersCount int       `json:"Members@odata.count"`
	Members      []ODataID `json:"Members"`
}

// MessageRegistryFile locates the actual content of a registry (e.g. the BIOS
// attribute registry) via Location[0].Uri.
type MessageRegistryFile struct {
	ODataType string             `json:"@odata.type"`
	ODataID   string             `json:"@odata.id"`
	ID        string             `json:"Id"`
	Name      string             `json:"Name"`
	Languages []string           `json:"Languages"`
	Registry  string             `json:"Registry"`
	Location  []RegistryLocation `json:"Location"`
}

// RegistryLocation is one language-specific location entry for a MessageRegistryFile.
type RegistryLocation struct {
	Language string `json:"Language"`
	Uri      string `json:"Uri"`
}

// AttributeRegistry is the actual content of a BIOS/BMC attribute registry,
// describing each attribute's type and whether changing it requires a reboot.
type AttributeRegistry struct {
	ODataType       string          `json:"@odata.type"`
	ODataID         string          `json:"@odata.id"`
	ODataContext    string          `json:"@odata.context,omitempty"`
	ID              string          `json:"Id"`
	Name            string          `json:"Name"`
	Description     string          `json:"Description,omitempty"`
	Language        string          `json:"Language,omitempty"`
	OwningEntity    string          `json:"OwningEntity,omitempty"`
	RegistryEntries RegistryEntries `json:"RegistryEntries"`
}

// RegistryEntries wraps the list of attribute definitions in an AttributeRegistry.
type RegistryEntries struct {
	Attributes []RegistryEntryAttribute `json:"Attributes"`
}

// RegistryEntryAttribute describes a single BIOS attribute: its type, whether
// changing it requires a reboot, and (for enumerations) its allowed values.
// ResetRequired is a plain bool (never omitted) because clients such as
// metal-operator treat a missing/null ResetRequired as "true".
type RegistryEntryAttribute struct {
	AttributeName string           `json:"AttributeName"`
	DisplayName   string           `json:"DisplayName,omitempty"`
	Type          string           `json:"Type"`
	ReadOnly      bool             `json:"ReadOnly"`
	Immutable     bool             `json:"Immutable"`
	Hidden        bool             `json:"Hidden"`
	ResetRequired bool             `json:"ResetRequired"`
	Value         []AttributeValue `json:"Value,omitempty"`
}

// AttributeValue is one allowed value for an Enumeration-typed attribute.
type AttributeValue struct {
	ValueName        string `json:"ValueName"`
	ValueDisplayName string `json:"ValueDisplayName,omitempty"`
}

// Bios represents the live BIOS Settings resource (/Bios) or the pending
// Settings resource (/Bios/Settings) — both share this shape, differing only
// in Id/Attributes/whether @Redfish.Settings is populated.
type Bios struct {
	ODataType         string           `json:"@odata.type"`
	ODataID           string           `json:"@odata.id"`
	ID                string           `json:"Id"`
	Name              string           `json:"Name"`
	AttributeRegistry string           `json:"AttributeRegistry,omitempty"`
	Attributes        map[string]any   `json:"Attributes"`
	RedfishSettings   *RedfishSettings `json:"@Redfish.Settings,omitempty"`
}

// RedfishSettings is the @Redfish.Settings block linking a live resource
// (e.g. /Bios) to its pending-settings object (e.g. /Bios/Settings).
type RedfishSettings struct {
	ODataType      string  `json:"@odata.type,omitempty"`
	SettingsObject ODataID `json:"SettingsObject"`
}

// PatchBiosSettingsRequest is the request body for PATCHing /Bios/Settings.
type PatchBiosSettingsRequest struct {
	Attributes map[string]any `json:"Attributes"`
}
