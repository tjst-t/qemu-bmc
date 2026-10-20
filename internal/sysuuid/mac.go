package sysuuid

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// MAC returns the MAC address derived for the NIC at index (0-based, in QEMU
// command-line order) of the system with this UUID. It is 52:54 (the
// locally administered prefix QEMU uses) followed by 4 bytes of
// SHA-256(UUID || index), so it is stable across restarts and distinct
// between nodes whose UUIDs differ.
func (u UUID) MAC(index int) string {
	var idx [4]byte
	binary.BigEndian.PutUint32(idx[:], uint32(index))
	h := sha256.New()
	h.Write(u[:])
	h.Write(idx[:])
	sum := h.Sum(nil)
	return fmt.Sprintf("52:54:%02x:%02x:%02x:%02x", sum[0], sum[1], sum[2], sum[3])
}
