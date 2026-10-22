package qemu

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SecureBootFirmware is the Secure Boot variant of the OVMF on a QEMU
// command line (see FindSecureBootFirmware).
type SecureBootFirmware struct {
	Code         string // Secure Boot OVMF code (OVMF_CODE[_4M].secboot.fd)
	VarsTemplate string // VARS with the Microsoft keys enrolled (OVMF_VARS[_4M].ms.fd)
	Vars         string // VARS used for Secure Boot, copied from VarsTemplate

	codePos, varsPos int // indexes of the pflash -drive values in args
}

// FindSecureBootFirmware finds the OVMF pflash drives in args (-drive
// if=pflash with an OVMF_CODE*.fd file, and the other pflash drive holding
// its VARS) and returns their Secure Boot counterparts: the secboot code and
// the Microsoft-keys VARS template in the code's directory (the 4M variants
// for OVMF_CODE_4M*), and a VARS file next to the current one
// (OVMF_VARS.fd → OVMF_VARS.secboot.fd). ok is false without OVMF pflash
// drives. The files are not checked (see Check).
func FindSecureBootFirmware(args []string) (fw SecureBootFirmware, ok bool) {
	fw.codePos, fw.varsPos = -1, -1
	for i := 0; i+1 < len(args); i++ {
		if args[i] != "-drive" || optionValue(args[i+1], "if") != "pflash" {
			continue
		}
		file := optionValue(args[i+1], "file")
		if base := filepath.Base(file); fw.codePos < 0 && strings.HasPrefix(base, "OVMF_CODE") && strings.HasSuffix(base, ".fd") {
			fw.codePos = i + 1
			dir, variant := filepath.Dir(file), ""
			if strings.HasPrefix(base, "OVMF_CODE_4M") {
				variant = "_4M"
			}
			fw.Code = filepath.Join(dir, "OVMF_CODE"+variant+".secboot.fd")
			fw.VarsTemplate = filepath.Join(dir, "OVMF_VARS"+variant+".ms.fd")
		} else if fw.varsPos < 0 && file != "" {
			fw.varsPos = i + 1
			fw.Vars = strings.TrimSuffix(file, ".fd") + ".secboot.fd"
		}
	}
	if fw.codePos < 0 || fw.varsPos < 0 {
		return SecureBootFirmware{}, false
	}
	return fw, true
}

// Check reports why the Secure Boot firmware cannot be used: a missing
// secboot code or VARS template.
func (fw SecureBootFirmware) Check() error {
	for _, f := range []string{fw.Code, fw.VarsTemplate} {
		if _, err := os.Stat(f); err != nil {
			return fmt.Errorf("Secure Boot OVMF not available: %w", err)
		}
	}
	return nil
}

// PrepareVars copies the VARS template to Vars unless it exists.
func (fw SecureBootFirmware) PrepareVars() error {
	if _, err := os.Stat(fw.Vars); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	src, err := os.Open(fw.VarsTemplate)
	if err != nil {
		return err
	}
	defer src.Close()
	// Copy to a temporary file first so that a failed copy leaves no
	// truncated VARS behind
	tmp := fw.Vars + ".tmp"
	dst, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		os.Remove(tmp)
		return err
	}
	if err := dst.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, fw.Vars)
}

// ResetVars removes Vars so that the next PrepareVars copies the template
// again. A running QEMU keeps the removed file open and is not affected.
func (fw SecureBootFirmware) ResetVars() error {
	if err := os.Remove(fw.Vars); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Apply returns args (the command line fw was found in) booting the Secure
// Boot firmware: the pflash drives get the secboot code and Vars, the
// machine gets smm=on and the pflash is only writable from SMM, as the
// Secure Boot OVMF requires.
func (fw SecureBootFirmware) Apply(args []string) []string {
	result := make([]string, len(args), len(args)+2)
	copy(result, args)
	result[fw.codePos] = setOption(result[fw.codePos], "file", fw.Code)
	result[fw.varsPos] = setOption(result[fw.varsPos], "file", fw.Vars)
	machine := false
	for i := 0; i+1 < len(result); i++ {
		if result[i] == "-machine" || result[i] == "-M" {
			result[i+1] = setOption(result[i+1], "smm", "on")
			machine = true
		}
	}
	if !machine {
		result = append(result, "-machine", "smm=on")
	}
	return append(result, "-global", "driver=cfi.pflash01,property=secure,value=on")
}

// setOption sets key=value in a QEMU option string, replacing an existing
// value or appending it.
func setOption(opts, key, value string) string {
	parts := splitOpts(opts)
	found := false
	for i, opt := range parts {
		if k, _, ok := strings.Cut(opt, "="); ok && k == key {
			parts[i] = key + "=" + value
			found = true
		}
	}
	if !found {
		parts = append(parts, key+"="+value)
	}
	for i := range parts {
		parts[i] = strings.ReplaceAll(parts[i], ",", ",,")
	}
	return strings.Join(parts, ",")
}
