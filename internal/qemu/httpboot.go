package qemu

import (
	"log"
	"strings"
)

const (
	// HTTPBootDriveID is the -drive id of the hidden CD drive holding the
	// image downloaded from Boot.HttpBootUri. It is not the VirtualMedia
	// drive.
	HTTPBootDriveID = "httpboot0"
	// HTTPBootDeviceID is the qdev id of that drive's ide-cd device.
	HTTPBootDeviceID = "httpboot-cd0"
	// httpBootControllerID is the qdev id of the AHCI controller added for
	// it, so that it does not compete for the ports of the machine's own
	// IDE/AHCI buses (q35 has one unit per port).
	httpBootControllerID = "httpboot-ahci"
)

// ovmfNoPXE makes OVMF skip PXE on its NICs so that only HTTP boot is tried.
var ovmfNoPXE = []string{
	"-fw_cfg", "name=opt/org.tianocore/IPv4PXESupport,string=n",
	"-fw_cfg", "name=opt/org.tianocore/IPv6PXESupport,string=n",
}

// ApplyHTTPBoot modifies QEMU args for the UefiHttp boot target (after
// ApplyBootOverride, which sets -boot n as for Pxe).
//
// With an image (downloaded from Boot.HttpBootUri) a hidden CD drive holding
// it is added, on its own AHCI controller, with bootindex=0, so the VM boots
// it first.
//
// Without one the VM boots from the network. With UEFI firmware OVMF is told
// not to try PXE, and the first NIC gets bootindex=0 and no option ROM: its
// iPXE ROM would otherwise boot before OVMF's HTTP boot. The boot URL then
// comes from the DHCP server (answering the HTTPClient vendor class). With
// BIOS it is the same as Pxe.
//
// bootindex=0 is not set if another device already has it.
func ApplyHTTPBoot(args []string, image string) []string {
	if image != "" {
		dev := "ide-cd,bus=" + httpBootControllerID + ".0,drive=" + HTTPBootDriveID + ",id=" + HTTPBootDeviceID
		if !hasBootIndex0(args) {
			dev += ",bootindex=0"
		} else {
			log.Printf("WARN: HTTP boot: another device has bootindex=0, the HTTP boot image may not boot first")
		}
		result := make([]string, len(args), len(args)+6)
		copy(result, args)
		return append(result,
			"-device", "ich9-ahci,id="+httpBootControllerID,
			"-drive", "if=none,id="+HTTPBootDriveID+",media=cdrom,readonly=on,format=raw,file="+strings.ReplaceAll(image, ",", ",,"),
			"-device", dev,
		)
	}

	if !IsUEFI(args) {
		return args
	}
	result := make([]string, len(args), len(args)+len(ovmfNoPXE))
	copy(result, args)
	if nics := findNICs(result); len(nics) > 0 && result[nics[0].pos-1] == "-device" {
		val := result[nics[0].pos]
		if !hasOption(val, "romfile") {
			val += ",romfile="
		}
		if optionValue(val, "bootindex") == "" && !hasBootIndex0(result) {
			val += ",bootindex=0"
		}
		result[nics[0].pos] = val
	}
	return append(result, ovmfNoPXE...)
}

// IsUEFI reports whether args boot UEFI firmware: a pflash drive (OVMF as
// set up by entrypoint.sh) or a -bios/-pflash image named like OVMF.
func IsUEFI(args []string) bool {
	for i := 0; i+1 < len(args); i++ {
		val := args[i+1]
		switch args[i] {
		case "-drive":
			if optionValue(val, "if") == "pflash" {
				return true
			}
		case "-bios", "-pflash":
			if strings.Contains(strings.ToLower(val), "ovmf") {
				return true
			}
		case "-machine", "-M":
			if optionValue(val, "pflash0") != "" {
				return true
			}
		}
	}
	return false
}

// hasBootIndex0 reports whether a device in args has bootindex=0.
func hasBootIndex0(args []string) bool {
	for _, arg := range args {
		if optionValue(arg, "bootindex") == "0" {
			return true
		}
	}
	return false
}

// hasOption reports whether a QEMU option string sets key (even to "").
func hasOption(opts, key string) bool {
	for _, opt := range splitOpts(opts) {
		if k, _, ok := strings.Cut(opt, "="); ok && k == key {
			return true
		}
	}
	return false
}
