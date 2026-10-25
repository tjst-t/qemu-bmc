package qemu

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testSMBIOS = SMBIOS{Manufacturer: "QEMU", Product: "qemu-bmc", Serial: "QBMC-0123456789"}

func TestSMBIOSArgs(t *testing.T) {
	assert.Equal(t, []string{
		"-smbios", "type=1,manufacturer=QEMU,product=qemu-bmc,serial=QBMC-0123456789",
		"-smbios", "type=2,manufacturer=QEMU,product=qemu-bmc,serial=QBMC-0123456789",
		"-smbios", "type=3,manufacturer=QEMU,serial=QBMC-0123456789",
	}, SMBIOSArgs(nil, testSMBIOS))
}

func TestSMBIOSArgs_SKU(t *testing.T) {
	s := testSMBIOS
	s.SKU = "SKU-1"
	args := SMBIOSArgs(nil, s)
	require.Len(t, args, 6)
	assert.Equal(t, "type=1,manufacturer=QEMU,product=qemu-bmc,serial=QBMC-0123456789,sku=SKU-1", args[1])
	assert.NotContains(t, args[3], "sku=") // type 2 has no SKU
	assert.Equal(t, "type=3,manufacturer=QEMU,serial=QBMC-0123456789,sku=SKU-1", args[5])
}

func TestSMBIOSArgs_EscapesCommas(t *testing.T) {
	args := SMBIOSArgs(nil, SMBIOS{Manufacturer: "Acme, Inc.", Product: "a,b"})
	assert.Equal(t, "type=1,manufacturer=Acme,, Inc.,product=a,,b", args[1])
}

func TestSMBIOSArgs_UserTypeWins(t *testing.T) {
	user := []string{"-smbios", "type=1,manufacturer=Mine", "-m", "1024"}
	args := SMBIOSArgs(user, testSMBIOS)
	assert.Equal(t, []string{
		"-smbios", "type=2,manufacturer=QEMU,product=qemu-bmc,serial=QBMC-0123456789",
		"-smbios", "type=3,manufacturer=QEMU,serial=QBMC-0123456789",
	}, args)
}

func TestSMBIOSArgs_OtherUserTypesDoNotCount(t *testing.T) {
	user := []string{"-smbios", "type=11,value=foo", "-smbios", "file=/tmp/x"}
	assert.Len(t, SMBIOSArgs(user, testSMBIOS), 6)
}

func TestSMBIOSArgs_Empty(t *testing.T) {
	assert.Nil(t, SMBIOSArgs(nil, SMBIOS{}))
}

func TestBuildCommandLine_SMBIOS(t *testing.T) {
	args, err := BuildCommandLine([]string{"-smbios", "type=1,serial=X"}, BuildOptions{
		QMPSocketPath: "/tmp/qmp.sock",
		SMBIOS:        testSMBIOS,
	})
	require.NoError(t, err)
	var smbios []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-smbios" {
			smbios = append(smbios, args[i+1])
		}
	}
	assert.Equal(t, []string{
		"type=1,serial=X",
		"type=2,manufacturer=QEMU,product=qemu-bmc,serial=QBMC-0123456789",
		"type=3,manufacturer=QEMU,serial=QBMC-0123456789",
	}, smbios)
}
