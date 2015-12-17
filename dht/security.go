package dht

import (
	"fmt"
	"hash/crc32"
	"net"
)

func maskForIP(ip net.IP) []byte {
	switch {
	case ip.To4() != nil:
		return []byte{0x03, 0x0f, 0x3f, 0xff}
	default:
		return []byte{0x01, 0x03, 0x07, 0x0f, 0x1f, 0x3f, 0x7f, 0xff}
	}
}

// Generate the CRC used to make or validate secure node ID.
func crcIP(ip net.IP, rand uint8) uint32 {
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	// Copy IP so we can make changes. Go sux at this.
	ip = append(make(net.IP, 0, len(ip)), ip...)
	mask := maskForIP(ip)
	for i := range mask {
		ip[i] &= mask[i]
	}
	r := rand & 7
	ip[0] |= r << 5
	return crc32.Checksum(ip[:len(mask)], crc32.MakeTable(crc32.Castagnoli))
}

// Makes a node ID secure, in-place. The ID is 20 raw bytes.
// http://www.libtorrent.org/dht_sec.html
func SecureNodeId(id []byte, ip net.IP) {
	crc := crcIP(ip, id[19])
	id[0] = byte(crc >> 24 & 0xff)
	id[1] = byte(crc >> 16 & 0xff)
	id[2] = byte(crc>>8&0xf8) | id[2]&7
}

// Returns whether the node ID is considered secure. The id is the 20 raw
// bytes. http://www.libtorrent.org/dht_sec.html
func NodeIdSecure(id string, ip net.IP) bool {
	if len(id) != 20 {
		panic(fmt.Sprintf("%q", id))
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	crc := crcIP(ip, id[19])
	if id[0] != byte(crc>>24&0xff) {
		return false
	}
	if id[1] != byte(crc>>16&0xff) {
		return false
	}
	if id[2]&0xf8 != byte(crc>>8&0xf8) {
		return false
	}
	return true
}
