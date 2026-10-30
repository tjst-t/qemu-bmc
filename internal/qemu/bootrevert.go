package qemu

import (
	"fmt"
	"strings"
)

// defaultBootOrder is QEMU's boot order for PC machines without -boot.
const defaultBootOrder = "cad"

// BootRevert undoes a boot override in a running QEMU, for the next reset
// of the guest: a BootSourceOverrideEnabled=Once override is put on the
// command line (-boot and bootindex=0), which QEMU keeps across resets,
// so a guest that reboots itself would boot the override target again.
//
// After the boot that consumed the override, setting the bootindex of
// Devices back to -1 (qom-set) makes the firmware boot order of the next
// reset that of the default command line (QEMU regenerates it at every
// reset), and Order (HMP boot_set) does the same for the BIOS boot order
// set by -boot.
//
// UEFI (OVMF) ignores -boot and, when QEMU gives no boot order, keeps the
// order in its variable store, which it rewrote with the override device
// first during the boot that used it. With DisksFirst the disks are given
// a bootindex too, so that OVMF puts them first again on the next reset,
// as -boot c (the default order, disk first) means. As OVMF then drops the
// devices without a bootindex from its boot order, the CD drives and NICs
// (Others) get one after the disks, so that a machine whose disk does not
// boot still falls back to an inserted CD or the network, as before the
// override.
type BootRevert struct {
	Devices    []string // qdev ids given bootindex=0 by the override
	Order      string   // boot order without the override, e.g. "cad"
	DisksFirst bool     // UEFI with a disk-first default order and no bootindex of the user's
	// Others are the qdev ids of the CD drives, then the NICs, in
	// command-line order: put after the disks with DisksFirst (also when
	// the order is restored at a later start without an override)
	Others []string
}

// Empty reports whether there is nothing to undo.
func (r BootRevert) Empty() bool { return len(r.Devices) == 0 && r.Order == "" && !r.DisksFirst }

// bootOverrideRevert compares the command line before and after the boot
// override was applied (ApplyBootOverride, ApplyHTTPBoot) and returns what
// undoes it. Devices that got bootindex=0 but have no id are given one in
// after, so that they can be found at run time; the override only edits
// option values in place or appends, so positions in before still match.
func bootOverrideRevert(before, after []string) BootRevert {
	var r BootRevert
	for i := 0; i+1 < len(after); i++ {
		if after[i] != "-device" || optionValue(after[i+1], "bootindex") != "0" {
			continue
		}
		if i+1 < len(before) && before[i] == "-device" && optionValue(before[i+1], "bootindex") == "0" {
			continue // the user's own bootindex=0
		}
		id := optionValue(after[i+1], "id")
		if id == "" {
			id = fmt.Sprintf("qbmc-boot%d", i+1)
			after[i+1] += ",id=" + id
		}
		r.Devices = append(r.Devices, id)
	}
	o, _ := ArgValue(before, "-boot")
	if b, _ := ArgValue(after, "-boot"); b != "" && o != b {
		r.Order = bootOrder(o)
	}
	if (len(r.Devices) > 0 || r.Order != "") && IsUEFI(after) &&
		strings.HasPrefix(bootOrder(o), "c") && !hasBootIndex(before) {
		r.DisksFirst = true
	}
	return r
}

// fallbackBootDevices returns the qdev ids of the CD drives (ide-cd,
// scsi-cd; not the UEFI HTTP boot image) and then of the NICs (-device) in
// args, in command-line order, giving an id to those without one (in args).
// -drive media=cdrom, -nic and -net nic devices cannot be addressed and are
// left out.
func fallbackBootDevices(args []string) []string {
	var cds, nics []string
	isNIC := map[int]bool{}
	for _, nic := range findNICs(args) {
		if args[nic.pos-1] == "-device" {
			isNIC[nic.pos] = true
		}
	}
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-device" {
			continue
		}
		driver := splitOpts(args[i+1])[0]
		if d := optionValue(args[i+1], "driver"); d != "" {
			driver = d
		}
		isCD := driver == "ide-cd" || driver == "scsi-cd"
		if !isCD && !isNIC[i+1] {
			continue
		}
		id := optionValue(args[i+1], "id")
		if id == HTTPBootDeviceID {
			continue
		}
		if id == "" {
			id = fmt.Sprintf("qbmc-boot%d", i+1)
			args[i+1] += ",id=" + id
		}
		if isCD {
			cds = append(cds, id)
		} else {
			nics = append(nics, id)
		}
	}
	return append(cds, nics...)
}

// hasBootIndex reports whether a device in args has a bootindex.
func hasBootIndex(args []string) bool {
	for _, arg := range args {
		if optionValue(arg, "bootindex") != "" {
			return true
		}
	}
	return false
}

// bootOrder returns the boot order of a -boot value ("c", "order=dc,menu=on"),
// or QEMU's default if it has none.
func bootOrder(val string) string {
	for j, opt := range splitOpts(val) {
		key, value, hasEq := strings.Cut(opt, "=")
		if !hasEq && j == 0 && opt != "" {
			return opt
		}
		if key == "order" && value != "" {
			return value
		}
	}
	return defaultBootOrder
}
