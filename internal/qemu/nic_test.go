package qemu

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testDeriveMAC(index int) string {
	return fmt.Sprintf("52:54:aa:bb:cc:%02x", index)
}

func TestAssignMACs_InjectsDerivedMACs(t *testing.T) {
	args := []string{
		"-netdev", "tap,id=net0,ifname=tap0,script=no,downscript=no",
		"-device", "virtio-net-pci,netdev=net0",
		"-netdev", "tap,id=net1,ifname=tap1,script=no,downscript=no",
		"-device", "e1000,netdev=net1",
	}
	result, err := AssignMACs(args, nil, testDeriveMAC)
	require.NoError(t, err)
	assert.Equal(t, "virtio-net-pci,netdev=net0,mac=52:54:aa:bb:cc:00", result[3])
	assert.Equal(t, "e1000,netdev=net1,mac=52:54:aa:bb:cc:01", result[7])
	assert.Equal(t, "virtio-net-pci,netdev=net0", args[3], "input not modified")
}

func TestAssignMACs_ExplicitMACsByIndex(t *testing.T) {
	args := []string{
		"-device", "virtio-net-pci,netdev=net0",
		"-device", "virtio-net-pci,netdev=net1",
	}
	// Empty entries fall back to the derived MAC; input is normalized
	result, err := AssignMACs(args, []string{"", "02:00:00:AB:CD:EF"}, testDeriveMAC)
	require.NoError(t, err)
	assert.Equal(t, "virtio-net-pci,netdev=net0,mac=52:54:aa:bb:cc:00", result[1])
	assert.Equal(t, "virtio-net-pci,netdev=net1,mac=02:00:00:ab:cd:ef", result[3])
}

func TestAssignMACs_UserMACKept(t *testing.T) {
	args := []string{"-device", "virtio-net-pci,mac=52:54:00:11:22:33,netdev=net0"}
	result, err := AssignMACs(args, nil, testDeriveMAC)
	require.NoError(t, err)
	assert.Equal(t, args, result)

	// Matching explicit value is fine
	_, err = AssignMACs(args, []string{"52:54:00:11:22:33"}, testDeriveMAC)
	assert.NoError(t, err)
}

func TestAssignMACs_UserMACConflict(t *testing.T) {
	args := []string{"-device", "virtio-net-pci,mac=52:54:00:11:22:33,netdev=net0"}
	_, err := AssignMACs(args, []string{"52:54:00:11:22:44"}, testDeriveMAC)
	assert.Error(t, err)
}

func TestAssignMACs_NicAndNetForms(t *testing.T) {
	args := []string{
		"-nic", "user,model=virtio-net-pci",
		"-net", "nic,model=e1000",
		"-net", "user",
		"-nic", "none",
	}
	result, err := AssignMACs(args, nil, testDeriveMAC)
	require.NoError(t, err)
	assert.Equal(t, "user,model=virtio-net-pci,mac=52:54:aa:bb:cc:00", result[1])
	assert.Equal(t, "nic,model=e1000,macaddr=52:54:aa:bb:cc:01", result[3])
	assert.Equal(t, "user", result[5])
	assert.Equal(t, "none", result[7])
}

func TestAssignMACs_NonNICDevicesIgnored(t *testing.T) {
	args := []string{
		"-device", "virtio-rng-pci",
		"-device", "sga",
		"-netdev", "user,id=n0",
		"-device", "isa-ipmi-kcs,bmc=bmc0",
	}
	result, err := AssignMACs(args, nil, testDeriveMAC)
	require.NoError(t, err)
	assert.Equal(t, args, result)
}

func TestAssignMACs_DriverKeyAndEscapedCommas(t *testing.T) {
	args := []string{"-device", "driver=e1000e,id=a,,b"}
	result, err := AssignMACs(args, nil, testDeriveMAC)
	require.NoError(t, err)
	assert.Equal(t, "driver=e1000e,id=a,,b,mac=52:54:aa:bb:cc:00", result[1])
}

func TestAssignMACs_InvalidExplicit(t *testing.T) {
	args := []string{"-device", "virtio-net-pci,netdev=net0"}
	for _, mac := range []string{"zz:00:00:00:00:00", "52:54:00:00:00", "01:00:5e:00:00:01"} {
		_, err := AssignMACs(args, []string{mac}, testDeriveMAC)
		assert.Error(t, err, mac)
	}
}

func TestAssignMACs_Duplicate(t *testing.T) {
	args := []string{
		"-device", "virtio-net-pci,netdev=net0",
		"-device", "virtio-net-pci,netdev=net1",
	}
	_, err := AssignMACs(args, []string{"52:54:00:11:22:33", "52:54:00:11:22:33"}, testDeriveMAC)
	assert.Error(t, err)
}

func TestAssignMACs_TooManyExplicitIgnored(t *testing.T) {
	args := []string{"-device", "virtio-net-pci,netdev=net0"}
	result, err := AssignMACs(args, []string{"52:54:00:11:22:33", "52:54:00:11:22:44"}, testDeriveMAC)
	require.NoError(t, err)
	assert.Equal(t, "virtio-net-pci,netdev=net0,mac=52:54:00:11:22:33", result[1])
}

func TestAssignMACs_NoDeriveLeavesUnset(t *testing.T) {
	args := []string{"-device", "virtio-net-pci,netdev=net0"}
	result, err := AssignMACs(args, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, args, result)
}

func TestMACAddressesFromArgs(t *testing.T) {
	args := []string{
		"-device", "virtio-net-pci,netdev=net0,mac=52:54:00:AA:BB:CC",
		"-device", "virtio-rng-pci",
		"-device", "e1000,netdev=net1",
		"-net", "nic,macaddr=52:54:00:11:22:33",
		"-nic", "none",
	}
	assert.Equal(t, []string{"52:54:00:aa:bb:cc", "", "52:54:00:11:22:33"}, MACAddressesFromArgs(args))
	assert.Empty(t, MACAddressesFromArgs([]string{"-m", "2048"}))
}

func TestBuildCommandLine_InjectsMACs(t *testing.T) {
	result, err := BuildCommandLine([]string{"-device", "virtio-net-pci,netdev=net0"}, BuildOptions{
		QMPSocketPath: "/tmp/qmp.sock",
		MACAddresses:  []string{"52:54:00:11:22:33"},
		DeriveMAC:     testDeriveMAC,
	})
	require.NoError(t, err)
	assert.Contains(t, result, "virtio-net-pci,netdev=net0,mac=52:54:00:11:22:33")
	assert.Equal(t, []string{"52:54:00:11:22:33"}, HardwareFromArgs(result).MACAddresses)
}

func TestBuildCommandLine_MACConflict(t *testing.T) {
	_, err := BuildCommandLine([]string{"-device", "virtio-net-pci,mac=52:54:00:11:22:33"}, BuildOptions{
		QMPSocketPath: "/tmp/qmp.sock",
		MACAddresses:  []string{"52:54:00:11:22:44"},
	})
	assert.Error(t, err)
}

// VM_NET_MTU adds host_mtu= to the virtio-net NICs (setup-network.sh); the
// MAC and HTTP boot rewrites must keep it
func TestAssignMACs_KeepsHostMTU(t *testing.T) {
	args := []string{
		"-netdev", "tap,id=net0,ifname=tap0,script=no,downscript=no",
		"-device", "virtio-net-pci,netdev=net0,host_mtu=9000",
	}
	result, err := AssignMACs(args, nil, testDeriveMAC)
	require.NoError(t, err)
	assert.Equal(t, "virtio-net-pci,netdev=net0,host_mtu=9000,mac=52:54:aa:bb:cc:00", result[3])

	uefi := append([]string{"-drive", "if=pflash,format=raw,file=/OVMF_CODE.fd"}, result...)
	booted := ApplyHTTPBoot(uefi, "")
	assert.Equal(t, "virtio-net-pci,netdev=net0,host_mtu=9000,mac=52:54:aa:bb:cc:00,romfile=,bootindex=0", booted[5])
}
