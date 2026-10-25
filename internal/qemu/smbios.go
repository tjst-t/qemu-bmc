package qemu

import "strings"

// SMBIOS is the system identity qemu-bmc puts into the guest's SMBIOS
// tables (and reports via Redfish). Empty fields are left to QEMU.
type SMBIOS struct {
	Manufacturer string
	Product      string
	Serial       string
	SKU          string
}

// smbiosFields lists, per SMBIOS table type, the fields QEMU accepts for it:
// type 1 (system), 2 (baseboard) and 3 (chassis). Type 2 has no sku and
// type 3 no product.
var smbiosFields = []struct {
	typ    string
	fields func(s SMBIOS) [][2]string
}{
	{"1", func(s SMBIOS) [][2]string {
		return [][2]string{{"manufacturer", s.Manufacturer}, {"product", s.Product}, {"serial", s.Serial}, {"sku", s.SKU}}
	}},
	{"2", func(s SMBIOS) [][2]string {
		return [][2]string{{"manufacturer", s.Manufacturer}, {"product", s.Product}, {"serial", s.Serial}}
	}},
	{"3", func(s SMBIOS) [][2]string {
		return [][2]string{{"manufacturer", s.Manufacturer}, {"serial", s.Serial}, {"sku", s.SKU}}
	}},
}

// SMBIOSArgs returns the -smbios type=1/2/3 arguments for s. A type the user
// already passes with -smbios in userArgs is skipped, so the user's setting
// wins (like -uuid). Commas in values are escaped as ",,".
func SMBIOSArgs(userArgs []string, s SMBIOS) []string {
	var args []string
	for _, t := range smbiosFields {
		if hasSMBIOSType(userArgs, t.typ) {
			continue
		}
		opts := []string{"type=" + t.typ}
		for _, f := range t.fields(s) {
			if f[1] != "" {
				opts = append(opts, f[0]+"="+escapeOptValue(f[1]))
			}
		}
		if len(opts) > 1 {
			args = append(args, "-smbios", strings.Join(opts, ","))
		}
	}
	return args
}

// hasSMBIOSType reports whether args contain -smbios type=<typ>.
func hasSMBIOSType(args []string, typ string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-smbios" {
			continue
		}
		for _, opt := range strings.Split(args[i+1], ",") {
			if opt == "type="+typ {
				return true
			}
		}
	}
	return false
}

// escapeOptValue escapes a QEMU option value: "," is written as ",,".
func escapeOptValue(v string) string {
	return strings.ReplaceAll(v, ",", ",,")
}
