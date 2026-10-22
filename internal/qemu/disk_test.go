package qemu

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDisksFromArgs(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []Disk
	}{
		{
			name: "no disks",
			args: []string{"-m", "2048"},
			want: nil,
		},
		{
			name: "entrypoint disk with OVMF and managed CD",
			args: EnsureCDROM([]string{
				"-drive", "if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE.fd",
				"-drive", "if=pflash,format=raw,file=/vm/OVMF_VARS.fd",
				"-drive", "file=/vm/disk.qcow2,format=qcow2,if=virtio",
				"-cdrom", "/iso/install.iso",
			}),
			want: []Disk{{File: "/vm/disk.qcow2", Format: "qcow2", Protocol: "PCIe"}},
		},
		{
			name: "default interface is IDE",
			args: []string{"-drive", "file=/vm/a.img"},
			want: []Disk{{File: "/vm/a.img", Protocol: "SATA"}},
		},
		{
			name: "scsi and escaped comma",
			args: []string{"-drive", "if=scsi,file=/vm/a,,b.raw,format=raw"},
			want: []Disk{{File: "/vm/a,b.raw", Format: "raw", Protocol: "SAS"}},
		},
		{
			name: "user CD drive and drive without file are skipped",
			args: []string{
				"-drive", "file=/iso/x.iso,media=cdrom",
				"-drive", "if=virtio,id=empty",
				"-drive", "if=floppy,file=/vm/fd.img",
				"-drive", "if=mtd,file=/vm/mtd.img",
			},
			want: nil,
		},
		{
			name: "-hda to -hdd",
			args: []string{"-hda", "/vm/a.qcow2", "-hdb", "/vm/b.qcow2", "-cdrom", "/iso/x.iso"},
			want: []Disk{{File: "/vm/a.qcow2", Protocol: "SATA"}, {File: "/vm/b.qcow2", Protocol: "SATA"}},
		},
		{
			name: "if=none drives take the protocol of their device",
			args: []string{
				"-drive", "if=none,id=d0,file=/vm/nvme.qcow2",
				"-device", "nvme,drive=d0,serial=abc",
				"-drive", "if=none,id=d1,file=/vm/blk.qcow2,format=qcow2",
				"-device", "driver=virtio-blk-pci,drive=d1",
				"-drive", "if=none,id=cd,file=/iso/x.iso",
				"-device", "scsi-cd,drive=cd",
				"-drive", "if=none,id=d2,file=/vm/loose.img",
			},
			want: []Disk{
				{File: "/vm/nvme.qcow2", Protocol: "NVMe"},
				{File: "/vm/blk.qcow2", Format: "qcow2", Protocol: "PCIe"},
				{File: "/vm/loose.img"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DisksFromArgs(tt.args))
		})
	}
}

func TestParseImageInfoSize(t *testing.T) {
	size, err := ParseImageInfoSize([]byte(`{"virtual-size": 21474836480, "filename": "/vm/disk.qcow2", "format": "qcow2", "actual-size": 200704}`))
	require.NoError(t, err)
	assert.Equal(t, int64(21474836480), size)

	for _, out := range []string{``, `not json`, `{}`, `{"virtual-size": 0}`} {
		_, err := ParseImageInfoSize([]byte(out))
		assert.Error(t, err, out)
	}
}

// fakeQemuImg writes a script that records its arguments and prints out.
func fakeQemuImg(t *testing.T, out string, exitCode int) (bin, argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	bin = filepath.Join(dir, "qemu-img")
	script := "#!/bin/sh\necho \"$@\" > " + argsFile + "\ncat <<'EOF'\n" + out + "\nEOF\necho oops >&2\nexit " + strconv.Itoa(exitCode) + "\n"
	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	return bin, argsFile
}

func TestImageVirtualSize_Fake(t *testing.T) {
	bin, argsFile := fakeQemuImg(t, `{"virtual-size": 1073741824}`, 0)
	size, err := ImageVirtualSize(bin, "/vm/disk.qcow2", "qcow2")
	require.NoError(t, err)
	assert.Equal(t, int64(1073741824), size)
	got, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Equal(t, "info --output=json -U -f qcow2 /vm/disk.qcow2\n", string(got))

	_, err = ImageVirtualSize(bin, "/vm/disk.img", "")
	require.NoError(t, err)
	got, err = os.ReadFile(argsFile)
	require.NoError(t, err)
	assert.Equal(t, "info --output=json -U /vm/disk.img\n", string(got))
}

func TestImageVirtualSize_Errors(t *testing.T) {
	bin, _ := fakeQemuImg(t, "", 1)
	_, err := ImageVirtualSize(bin, "/vm/missing.qcow2", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "oops")

	_, err = ImageVirtualSize(filepath.Join(t.TempDir(), "no-qemu-img"), "/vm/disk.qcow2", "")
	assert.Error(t, err)
}

func TestImageVirtualSize_RealQemuImg(t *testing.T) {
	qemuImg, err := exec.LookPath("qemu-img")
	if err != nil {
		t.Skip("qemu-img not installed")
	}
	file := filepath.Join(t.TempDir(), "disk.qcow2")
	require.NoError(t, exec.Command(qemuImg, "create", "-q", "-f", "qcow2", file, "3G").Run())

	size, err := ImageVirtualSize(qemuImg, file, "qcow2")
	require.NoError(t, err)
	assert.Equal(t, int64(3<<30), size)
}
