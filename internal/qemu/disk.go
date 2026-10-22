package qemu

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Disk is a guest hard disk on the QEMU command line.
type Disk struct {
	File   string // image file (file= of -drive, or the -hdX value)
	Format string // format= of -drive, empty if not set
	// Protocol is the Redfish Protocol of the disk's bus: "PCIe" (virtio),
	// "SATA" (IDE/AHCI), "SAS" (SCSI), "NVMe" or "USB"; empty if unknown.
	Protocol string
}

// ifProtocols maps -drive if= values that attach a disk to its Redfish
// Protocol. Other interfaces (pflash, floppy, mtd, ...) are not disks.
var ifProtocols = map[string]string{
	"ide":    "SATA",
	"virtio": "PCIe",
	"scsi":   "SAS",
	"none":   "",
}

// diskDevices maps -device drivers of disks to their Redfish Protocol.
var diskDevices = map[string]string{
	"virtio-blk-pci":                  "PCIe",
	"virtio-blk-pci-transitional":     "PCIe",
	"virtio-blk-pci-non-transitional": "PCIe",
	"virtio-blk-device":               "PCIe",
	"ide-hd":                          "SATA",
	"ide-drive":                       "SATA",
	"scsi-hd":                         "SAS",
	"scsi-disk":                       "SAS",
	"scsi-block":                      "SAS",
	"nvme":                            "NVMe",
	"usb-storage":                     "USB",
}

// DisksFromArgs returns the guest hard disks in command-line order: -drive
// with a file (except CD-ROMs, which includes the VirtualMedia drive, and
// non-disk interfaces such as pflash) and -hda to -hdd.
func DisksFromArgs(args []string) []Disk {
	// Drive id → -device driver, to tell the bus (and CD drives) of
	// if=none drives
	devices := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-device" {
			continue
		}
		driver := splitOpts(args[i+1])[0]
		if d := optionValue(args[i+1], "driver"); d != "" {
			driver = d
		}
		if id := optionValue(args[i+1], "drive"); id != "" {
			devices[id] = driver
		}
	}

	var disks []Disk
	for i := 0; i+1 < len(args); i++ {
		val := args[i+1]
		switch args[i] {
		case "-hda", "-hdb", "-hdc", "-hdd":
			disks = append(disks, Disk{File: val, Protocol: "SATA"})
			i++
		case "-drive":
			i++
			file := optionValue(val, "file")
			if file == "" || optionValue(val, "media") == "cdrom" {
				continue
			}
			iface := optionValue(val, "if")
			if iface == "" {
				iface = "ide" // QEMU's default on x86
			}
			protocol, ok := ifProtocols[iface]
			if !ok {
				continue
			}
			if iface == "none" {
				driver, attached := devices[optionValue(val, "id")]
				if driver == "ide-cd" || driver == "scsi-cd" {
					continue
				}
				if attached {
					protocol = diskDevices[driver]
				}
			}
			disks = append(disks, Disk{File: file, Format: optionValue(val, "format"), Protocol: protocol})
		}
	}
	return disks
}

// ImageVirtualSize returns the virtual size in bytes of a disk image using
// "qemu-img info". format may be empty to let qemu-img probe it. The image
// is opened with -U so a running QEMU's lock does not get in the way.
func ImageVirtualSize(qemuImg, file, format string) (int64, error) {
	args := []string{"info", "--output=json", "-U"}
	if format != "" {
		args = append(args, "-f", format)
	}
	args = append(args, file)
	out, err := exec.Command(qemuImg, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return 0, fmt.Errorf("qemu-img info %s: %v: %s", file, err, strings.TrimSpace(string(ee.Stderr)))
		}
		return 0, fmt.Errorf("qemu-img info %s: %w", file, err)
	}
	return ParseImageInfoSize(out)
}

// ParseImageInfoSize returns virtual-size from "qemu-img info --output=json"
// output.
func ParseImageInfoSize(out []byte) (int64, error) {
	var info struct {
		VirtualSize *int64 `json:"virtual-size"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return 0, fmt.Errorf("parse qemu-img info output: %w", err)
	}
	if info.VirtualSize == nil || *info.VirtualSize <= 0 {
		return 0, fmt.Errorf("qemu-img info output has no virtual-size")
	}
	return *info.VirtualSize, nil
}
