package qemu

import (
	"strings"
)

const (
	// CDDriveID is the -drive id of the CD drive qemu-bmc adds for
	// VirtualMedia.
	CDDriveID = "vmedia0"
	// CDDeviceID is the qdev id of that drive's ide-cd device.
	CDDeviceID = "vmedia-cd0"
)

// EnsureCDROM makes sure the VM has a CD drive for VirtualMedia. A
// user-defined CD drive (-drive media=cdrom, or an ide-cd/scsi-cd device) is
// left as it is. Otherwise -cdrom is replaced by a managed drive holding the
// same image, or an empty managed drive is added.
func EnsureCDROM(args []string) []string {
	if hasUserCDDrive(args) {
		return args
	}

	image, _ := ArgValue(args, "-cdrom")
	result := make([]string, 0, len(args)+4)
	for i := 0; i < len(args); i++ {
		if args[i] == "-cdrom" && i+1 < len(args) {
			i++ // skip value
			continue
		}
		result = append(result, args[i])
	}
	return append(result,
		"-drive", cdDriveValue(image),
		"-device", "ide-cd,drive="+CDDriveID+",id="+CDDeviceID,
	)
}

// hasUserCDDrive reports whether args define a CD drive other than -cdrom.
func hasUserCDDrive(args []string) bool {
	for i := 0; i+1 < len(args); i++ {
		val := args[i+1]
		switch args[i] {
		case "-drive":
			if optionValue(val, "media") == "cdrom" {
				return true
			}
		case "-device":
			driver := splitOpts(val)[0]
			if d := optionValue(val, "driver"); d != "" {
				driver = d
			}
			if driver == "ide-cd" || driver == "scsi-cd" {
				return true
			}
		}
	}
	return false
}

// SetCDImage sets the image of the managed CD drive ("" leaves it empty).
// Args without the managed drive are returned unchanged.
func SetCDImage(args []string, image string) []string {
	result := make([]string, len(args))
	copy(result, args)
	for i := 0; i+1 < len(result); i++ {
		if result[i] == "-drive" && optionValue(result[i+1], "id") == CDDriveID {
			result[i+1] = cdDriveValue(image)
		}
	}
	return result
}

// InitialCDImage returns the image the first CD drive in args is started
// with, or "" if there is none or it is empty.
func InitialCDImage(args []string) string {
	if image, ok := ArgValue(args, "-cdrom"); ok {
		return image
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-drive" && optionValue(args[i+1], "media") == "cdrom" {
			return optionValue(args[i+1], "file")
		}
	}
	return ""
}

func cdDriveValue(image string) string {
	val := "if=none,id=" + CDDriveID + ",media=cdrom,readonly=on"
	if image != "" {
		// QEMU option values escape a literal comma as ",,"
		val += ",format=raw,file=" + strings.ReplaceAll(image, ",", ",,")
	}
	return val
}

// setCDBootIndex gives the managed CD device boot priority. QEMU's -boot
// order is ignored by UEFI (OVMF), which only honors bootindex. Nothing is
// changed if another device already has bootindex=0.
func setCDBootIndex(args []string) []string {
	for _, arg := range args {
		if optionValue(arg, "bootindex") == "0" {
			return args
		}
	}
	result := make([]string, len(args))
	copy(result, args)
	for i := 0; i+1 < len(result); i++ {
		if result[i] == "-device" && optionValue(result[i+1], "id") == CDDeviceID &&
			optionValue(result[i+1], "bootindex") == "" {
			result[i+1] += ",bootindex=0"
		}
	}
	return result
}

// optionValue returns the value of key in a QEMU option string such as
// "if=none,id=cd0,file=a,,b.iso" (escaped commas are unescaped).
func optionValue(opts, key string) string {
	for _, opt := range splitOpts(opts) {
		if k, v, ok := strings.Cut(opt, "="); ok && k == key {
			return v
		}
	}
	return ""
}
