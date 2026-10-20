package qemu

import (
	"strconv"
	"strings"
)

// Hardware describes the VM hardware configured on the QEMU command line.
// Zero values mean the setting could not be determined.
type Hardware struct {
	MemoryMiB int
	CPUs      int
	// MACAddresses holds each guest NIC's MAC in command-line order (empty
	// when not set on the command line).
	MACAddresses []string
}

// HardwareFromArgs extracts the memory size (-m), CPU count (-smp) and NIC
// MAC addresses from QEMU arguments. When -m or -smp appears more than once,
// the last one wins, matching QEMU's behaviour.
func HardwareFromArgs(args []string) Hardware {
	hw := Hardware{MACAddresses: MACAddressesFromArgs(args)}
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "-m":
			hw.MemoryMiB = ParseMemoryMiB(args[i+1])
			i++
		case "-smp":
			hw.CPUs = ParseSMPCPUs(args[i+1])
			i++
		}
	}
	return hw
}

// ParseMemoryMiB parses a -m value such as "2048", "4G" or "size=4G,maxmem=8G".
// A value without a suffix is in MiB. It returns 0 if the value is invalid.
func ParseMemoryMiB(val string) int {
	size := ""
	for i, opt := range strings.Split(val, ",") {
		if v, ok := strings.CutPrefix(opt, "size="); ok {
			size = v
		} else if i == 0 && !strings.Contains(opt, "=") {
			size = opt
		}
	}
	if size == "" {
		return 0
	}

	multiplier := 1.0
	unit := size[len(size)-1]
	switch unit {
	case 'k', 'K':
		multiplier = 1.0 / 1024
	case 'g', 'G':
		multiplier = 1024
	case 't', 'T':
		multiplier = 1024 * 1024
	}
	if strings.IndexByte("kKmMgGtT", unit) >= 0 {
		size = size[:len(size)-1]
	}

	n, err := strconv.ParseFloat(size, 64)
	if err != nil || n <= 0 {
		return 0
	}
	return int(n * multiplier)
}

// ParseSMPCPUs parses a -smp value such as "4", "cpus=4,sockets=2" or
// "sockets=2,cores=2,threads=2". Without an explicit count, the CPU count
// is the product of the topology parameters. It returns 0 if the value is
// invalid.
func ParseSMPCPUs(val string) int {
	count := 0
	topology := 1
	hasTopology := false
	for i, opt := range strings.Split(val, ",") {
		key, value, hasEq := strings.Cut(opt, "=")
		if !hasEq {
			if i == 0 {
				count = atoiPositive(key)
			}
			continue
		}
		switch key {
		case "cpus":
			count = atoiPositive(value)
		case "sockets", "dies", "clusters", "cores", "threads", "books", "drawers":
			n := atoiPositive(value)
			if n == 0 {
				return 0
			}
			topology *= n
			hasTopology = true
		}
	}
	if count > 0 {
		return count
	}
	if hasTopology {
		return topology
	}
	return 0
}

func atoiPositive(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
