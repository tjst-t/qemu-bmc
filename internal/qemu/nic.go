package qemu

import (
	"fmt"
	"log"
	"net"
	"strings"
)

// nicModels are -device drivers that are network adapters. Any -device with
// a netdev= property is treated as a NIC as well.
var nicModels = map[string]bool{
	"virtio-net-pci":                  true,
	"virtio-net-pci-transitional":     true,
	"virtio-net-pci-non-transitional": true,
	"virtio-net-device":               true,
	"e1000":                           true,
	"e1000-82544gc":                   true,
	"e1000-82545em":                   true,
	"e1000e":                          true,
	"igb":                             true,
	"rtl8139":                         true,
	"ne2k_pci":                        true,
	"pcnet":                           true,
	"vmxnet3":                         true,
	"i82559er":                        true,
	"usb-net":                         true,
}

// nicArg locates a guest NIC on the QEMU command line.
type nicArg struct {
	pos    int    // index of the option value in args
	macKey string // property holding the MAC ("mac" or "macaddr")
	mac    string // MAC from the command line, empty if unset
}

// splitOpts splits a QEMU option string on commas; ",," is an escaped comma.
func splitOpts(val string) []string {
	var opts []string
	var cur strings.Builder
	for i := 0; i < len(val); i++ {
		if val[i] == ',' {
			if i+1 < len(val) && val[i+1] == ',' {
				cur.WriteByte(',')
				i++
				continue
			}
			opts = append(opts, cur.String())
			cur.Reset()
			continue
		}
		cur.WriteByte(val[i])
	}
	return append(opts, cur.String())
}

// findNICs returns the guest NICs in command-line order: -device with a NIC
// driver or netdev=, -nic (except "none") and -net nic.
func findNICs(args []string) []nicArg {
	var nics []nicArg
	for i := 0; i+1 < len(args); i++ {
		flag := args[i]
		if flag != "-device" && flag != "-nic" && flag != "-net" {
			continue
		}
		opts := splitOpts(args[i+1])
		props := map[string]string{}
		for j, opt := range opts {
			key, value, hasEq := strings.Cut(opt, "=")
			if !hasEq && j == 0 {
				// The implicit first option: driver / backend type
				key, value = "", opt
			}
			props[key] = value
		}

		nic := nicArg{pos: i + 1, macKey: "mac"}
		switch flag {
		case "-device":
			driver := props[""]
			if d, ok := props["driver"]; ok {
				driver = d
			}
			_, hasNetdev := props["netdev"]
			if !nicModels[driver] && !hasNetdev {
				continue
			}
		case "-nic":
			if props[""] == "none" {
				continue
			}
		case "-net":
			if props[""] != "nic" {
				continue
			}
			nic.macKey = "macaddr"
		}
		nic.mac = props[nic.macKey]
		nics = append(nics, nic)
		i++
	}
	return nics
}

// NormalizeMAC validates a unicast Ethernet MAC and returns it in lowercase
// colon-separated form.
func NormalizeMAC(mac string) (string, error) {
	hw, err := net.ParseMAC(mac)
	if err != nil || len(hw) != 6 {
		return "", fmt.Errorf("invalid MAC address %q", mac)
	}
	if hw[0]&0x01 != 0 {
		return "", fmt.Errorf("invalid MAC address %q: multicast", mac)
	}
	return hw.String(), nil
}

// AssignMACs sets the MAC address of every guest NIC in args. The i-th NIC
// gets explicit[i] when that entry is non-empty, otherwise derive(i). A MAC
// already on the command line is kept, but must match explicit[i] if both
// are set. NICs are left untouched when derive is nil and no explicit MAC is
// given. All MACs on the node must be unique.
func AssignMACs(args []string, explicit []string, derive func(index int) string) ([]string, error) {
	nics := findNICs(args)
	if len(explicit) > len(nics) {
		log.Printf("WARN: %d MAC addresses configured but only %d NICs found; extra entries ignored", len(explicit), len(nics))
	}

	result := make([]string, len(args))
	copy(result, args)
	used := map[string]int{}
	for i, nic := range nics {
		want := ""
		if i < len(explicit) && explicit[i] != "" {
			mac, err := NormalizeMAC(explicit[i])
			if err != nil {
				return nil, fmt.Errorf("NIC %d: %w", i, err)
			}
			want = mac
		}

		mac := ""
		switch {
		case nic.mac != "":
			m, err := NormalizeMAC(nic.mac)
			if err != nil {
				return nil, fmt.Errorf("NIC %d: %w", i, err)
			}
			if want != "" && want != m {
				return nil, fmt.Errorf("NIC %d: %s=%s conflicts with configured MAC %s", i, nic.macKey, nic.mac, want)
			}
			mac = m
		case want != "":
			mac = want
		case derive != nil:
			mac = derive(i)
		default:
			continue
		}

		if prev, dup := used[mac]; dup {
			return nil, fmt.Errorf("NIC %d: MAC %s is already used by NIC %d", i, mac, prev)
		}
		used[mac] = i
		if nic.mac == "" {
			result[nic.pos] = fmt.Sprintf("%s,%s=%s", args[nic.pos], nic.macKey, mac)
		}
	}
	return result, nil
}

// MACAddressesFromArgs returns the MAC address of each guest NIC in
// command-line order; the entry is empty if the NIC has no valid MAC set.
func MACAddressesFromArgs(args []string) []string {
	nics := findNICs(args)
	macs := make([]string, 0, len(nics))
	for _, nic := range nics {
		mac, err := NormalizeMAC(nic.mac)
		if err != nil {
			mac = ""
		}
		macs = append(macs, mac)
	}
	return macs
}
