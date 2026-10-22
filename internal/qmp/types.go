package qmp

// Status represents QEMU VM running status
type Status string

const (
	StatusRunning  Status = "running"
	StatusShutdown Status = "shutdown"
	StatusPaused   Status = "paused"
)

// Client is the interface for QMP communication
type Client interface {
	Connect() error
	QueryStatus() (Status, error)
	SystemPowerdown() error
	SystemReset() error
	Stop() error
	Cont() error
	Quit() error
	// InjectNMI injects a non-maskable interrupt into the guest.
	InjectNMI() error
	QueryBlock() ([]BlockInfo, error)
	// The id argument of the medium commands is the guest device's qdev ID
	// or QOM path (BlockInfo.QDev).
	BlockdevChangeMedium(id, filename, format string) error
	BlockdevOpenTray(id string, force bool) error
	BlockdevCloseTray(id string) error
	BlockdevRemoveMedium(id string) error
	Close() error
}

// QMP protocol message types
type qmpGreeting struct {
	QMP struct {
		Version struct {
			QEMU struct {
				Micro int `json:"micro"`
				Minor int `json:"minor"`
				Major int `json:"major"`
			} `json:"qemu"`
		} `json:"version"`
		Capabilities []interface{} `json:"capabilities"`
	} `json:"QMP"`
}

type qmpCommand struct {
	Execute   string      `json:"execute"`
	Arguments interface{} `json:"arguments,omitempty"`
}

type qmpResponse struct {
	Return interface{} `json:"return,omitempty"`
	Error  *qmpError   `json:"error,omitempty"`
	Event  string      `json:"event,omitempty"`
}

type qmpError struct {
	Class string `json:"class"`
	Desc  string `json:"desc"`
}

type qmpStatusResponse struct {
	Return struct {
		Running bool   `json:"running"`
		Status  string `json:"status"`
	} `json:"return"`
}

// BlockInfo is one entry of the query-block result.
type BlockInfo struct {
	Device    string         `json:"device"` // legacy drive name; empty for -blockdev
	QDev      string         `json:"qdev"`   // guest device qdev ID or QOM path
	Removable bool           `json:"removable"`
	TrayOpen  bool           `json:"tray_open"`
	Inserted  *BlockInserted `json:"inserted,omitempty"`
}

// BlockInserted describes the medium inserted in a block device.
type BlockInserted struct {
	File     string `json:"file"`
	ReadOnly bool   `json:"ro"`
}

type qmpQueryBlockResponse struct {
	Return []BlockInfo `json:"return"`
	Error  *qmpError   `json:"error,omitempty"`
}

type blockdevChangeMediumArgs struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Format   string `json:"format,omitempty"`
}

type blockdevOpenTrayArgs struct {
	ID    string `json:"id"`
	Force bool   `json:"force,omitempty"`
}

type blockdevIDArgs struct {
	ID string `json:"id"`
}
