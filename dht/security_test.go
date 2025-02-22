package dht

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/dht/v2/krpc"
)

func TestDHTSec(t *testing.T) {
	for _, case_ := range []struct {
		ipStr     string
		nodeIDHex string
		valid     bool
	}{
		// These 5 are from the spec example. They are all valid.
		{"124.31.75.21", "5fbfbff10c5d6a4ec8a88e4c6ab4c28b95eee401", true},
		{"21.75.31.124", "5a3ce9c14e7a08645677bbd1cfe7d8f956d53256", true},
		{"65.23.51.170", "a5d43220bc8f112a3d426c84764f8c2a1150e616", true},
		{"84.124.73.14", "1b0321dd1bb1fe518101ceef99462b947a01ff41", true},
		{"43.213.53.83", "e56f6cbf5b7c4be0237986d5243b87aa6d51305a", true},
		// spec[0] with one of the rand() bytes changed. Valid.
		{"124.31.75.21", "5fbfbff10c5d7a4ec8a88e4c6ab4c28b95eee401", true},
		// spec[1] with the 21st leading bit changed. Not Valid.
		{"21.75.31.124", "5a3ce1c14e7a08645677bbd1cfe7d8f956d53256", false},
		// spec[2] with the 22nd leading bit changed. Valid.
		{"65.23.51.170", "a5d43620bc8f112a3d426c84764f8c2a1150e616", true},
		// spec[3] with the 4th last bit changed. Valid.
		{"84.124.73.14", "1b0321dd1bb1fe518101ceef99462b947a01fe01", true},
		// spec[4] with the 3rd last bit changed. Not valid.
		{"43.213.53.83", "e56f6cbf5b7c4be0237986d5243b87aa6d51303e", false},
		// Because class A network.
		{"10.213.53.83", "e56f6cbf5b7c4be0237986d5243b87aa6d51305a", true},
		// Because not class A, and id[0]&3 does not match.
		{"12.213.53.83", "e56f6cbf5b7c4be0237986d5243b87aa6d51305a", false},
		// Because class C.
		{"192.168.53.83", "e56f6cbf5b7c4be0237986d5243b87aa6d51305a", true},
	} {
		ip := net.ParseIP(case_.ipStr)
		_id, err := hex.DecodeString(case_.nodeIDHex)
		require.NoError(t, err)
		var id krpc.ID
		require.Equal(t, 20, copy(id[:], _id))
		secure := NodeIdSecure(id, ip)
		assert.Equal(t, case_.valid, secure, "%v", case_)
		if !secure {
			// It's not secure, so secure it in place and then check it again.
			SecureNodeId(&id, ip)
			assert.True(t, NodeIdSecure(id, ip), "%v", case_)
		}
	}
}

func getInsecureIp(nodeId [20]byte, ip net.IP) {
	for {
		rand.Read(ip)
		if !NodeIdSecure(nodeId, ip) {
			break
		}
	}
}

// Show that we can't secure a node ID against more than one IP.
func TestSecureNodeIdMultipleIps(t *testing.T) {
	id := RandomNodeID()
	t.Logf("random node id: %x", id)
	ip4 := make(net.IP, 4)
	getInsecureIp(id, ip4)
	ip6 := make(net.IP, 16)
	getInsecureIp(id, ip6)
	t.Logf("random ip4 address: %s", ip4)
	t.Logf("random ip6 address: %s", ip6)
	require.False(t, NodeIdSecure(id, ip4))
	require.False(t, NodeIdSecure(id, ip6))
	SecureNodeId(&id, ip4)
	assert.True(t, NodeIdSecure(id, ip4))
	assert.False(t, NodeIdSecure(id, ip6))
	SecureNodeId(&id, ip6)
	assert.True(t, NodeIdSecure(id, ip6))
	assert.False(t, NodeIdSecure(id, ip4))
}
