package dht

import (
	"encoding/hex"
	"math/big"
	"math/rand"
	"net"
	"testing"

	"github.com/anacrolix/missinggo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/util"
)

func TestSetNilBigInt(t *testing.T) {
	i := new(big.Int)
	i.SetBytes(make([]byte, 2))
}

func TestMarshalCompactNodeInfo(t *testing.T) {
	cni := NodeInfo{
		ID: [20]byte{'a', 'b', 'c'},
	}
	addr, err := net.ResolveUDPAddr("udp4", "1.2.3.4:5")
	if err != nil {
		t.Fatal(err)
	}
	cni.Addr = newDHTAddr(addr)
	var b [CompactIPv4NodeInfoLen]byte
	cni.PutCompact(b[:])
	if err != nil {
		t.Fatal(err)
	}
	var bb [26]byte
	copy(bb[:], []byte("abc"))
	copy(bb[20:], []byte("\x01\x02\x03\x04\x00\x05"))
	if b != bb {
		t.FailNow()
	}
}

func recoverPanicOrDie(t *testing.T, f func()) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
	}()
	f()
}

const zeroID = "\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"

var testIDs []nodeID

func init() {
	for _, s := range []string{
		zeroID,
		"\x03" + zeroID[1:],
		"\x03" + zeroID[1:18] + "\x55\xf0",
		"\x55" + zeroID[1:17] + "\xff\x55\x0f",
		"\x54" + zeroID[1:18] + "\x50\x0f",
		"",
	} {
		testIDs = append(testIDs, nodeIDFromString(s))
	}
}

func TestDistances(t *testing.T) {
	expectBitcount := func(i big.Int, count int) {
		if bitCount(i) != count {
			t.Fatalf("expected bitcount of %d: got %d", count, bitCount(i))
		}
	}
	expectBitcount(testIDs[3].Distance(&testIDs[0]), 4+8+4+4)
	expectBitcount(testIDs[3].Distance(&testIDs[1]), 4+8+4+4)
	expectBitcount(testIDs[3].Distance(&testIDs[2]), 4+8+8)
	for i := 0; i < 5; i++ {
		dist := testIDs[i].Distance(&testIDs[5])
		if dist.Cmp(&maxDistance) != 0 {
			t.Fatal("expected max distance for comparison with unset node id")
		}
	}
}

func TestMaxDistanceString(t *testing.T) {
	if string(maxDistance.Bytes()) != "\x01"+zeroID {
		t.FailNow()
	}
}

func TestClosestNodes(t *testing.T) {
	cn := newKClosestNodesSelector(2, testIDs[3])
	for _, i := range rand.Perm(len(testIDs)) {
		cn.Push(testIDs[i])
	}
	if len(cn.IDs()) != 2 {
		t.FailNow()
	}
	m := map[string]bool{}
	for _, id := range cn.IDs() {
		m[id.ByteString()] = true
	}
	if !m[testIDs[3].ByteString()] || !m[testIDs[4].ByteString()] {
		t.FailNow()
	}
}

func TestUnmarshalGetPeersResponse(t *testing.T) {
	var msg Msg
	err := bencode.Unmarshal([]byte("d1:rd6:valuesl6:\x01\x02\x03\x04\x05\x066:\x07\x08\x09\x0a\x0b\x0ce5:nodes52:\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x02\x03\x04\x05\x06\x07\x08\x09\x02\x03\x04\x05\x06\x07\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x02\x03\x04\x05\x06\x07\x08\x09\x02\x03\x04\x05\x06\x07ee"), &msg)
	require.NoError(t, err)
	assert.Len(t, msg.R.Values, 2)
	assert.Len(t, msg.R.Nodes, 2)
	assert.Nil(t, msg.E)
}

func TestDHTDefaultConfig(t *testing.T) {
	s, err := NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestPing(t *testing.T) {
	srv, err := NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	srv0, err := NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer srv0.Close()
	tn, err := srv.Ping(&net.UDPAddr{
		IP:   []byte{127, 0, 0, 1},
		Port: srv0.Addr().(*net.UDPAddr).Port,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer tn.Close()
	ok := make(chan bool)
	tn.SetResponseHandler(func(msg Msg) {
		ok <- msg.SenderID() == srv0.ID()
	})
	if !<-ok {
		t.FailNow()
	}
}

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
	} {
		ip := net.ParseIP(case_.ipStr)
		id, err := hex.DecodeString(case_.nodeIDHex)
		if err != nil {
			t.Fatal(err)
		}
		secure := NodeIdSecure(string(id), ip)
		if secure != case_.valid {
			t.Fatalf("case failed: %v", case_)
		}
		if !secure {
			SecureNodeId(id, ip)
			if !NodeIdSecure(string(id), ip) {
				t.Fatal("failed to secure node id")
			}
		}
	}
}

func TestServerDefaultNodeIdSecure(t *testing.T) {
	s, err := NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !NodeIdSecure(s.ID(), missinggo.AddrIP(s.Addr())) {
		t.Fatal("not secure")
	}
}

func testMarshalUnmarshalMsg(t *testing.T, m Msg, expected string) {
	b, err := bencode.Marshal(m)
	require.NoError(t, err)
	assert.Equal(t, expected, string(b))
	var _m Msg
	err = bencode.Unmarshal([]byte(expected), &_m)
	assert.NoError(t, err)
	assert.EqualValues(t, m, _m)
	assert.EqualValues(t, m.R, _m.R)
}

func TestMarshalUnmarshalMsg(t *testing.T) {
	testMarshalUnmarshalMsg(t, Msg{}, "d1:t0:1:y0:e")
	testMarshalUnmarshalMsg(t, Msg{
		Y: "q",
		Q: "ping",
		T: "hi",
	}, "d1:q4:ping1:t2:hi1:y1:qe")
	testMarshalUnmarshalMsg(t, Msg{
		Y: "e",
		T: "42",
		E: &KRPCError{Code: 200, Msg: "fuck"},
	}, "d1:eli200e4:fucke1:t2:421:y1:ee")
	testMarshalUnmarshalMsg(t, Msg{
		Y: "r",
		T: "\x8c%",
		R: &Return{},
	}, "d1:rd2:id0:5:token0:e1:t2:\x8c%1:y1:re")
	testMarshalUnmarshalMsg(t, Msg{
		Y: "r",
		T: "\x8c%",
		R: &Return{
			Nodes: CompactIPv4NodeInfo{
				NodeInfo{
					Addr: newDHTAddr(&net.UDPAddr{
						IP:   net.IPv4(1, 2, 3, 4),
						Port: 0x1234,
					}),
				},
			},
		},
	}, "d1:rd2:id0:5:nodes26:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x02\x03\x04\x1245:token0:e1:t2:\x8c%1:y1:re")
	testMarshalUnmarshalMsg(t, Msg{
		Y: "r",
		T: "\x8c%",
		R: &Return{
			Values: []util.CompactPeer{
				util.CompactPeer{
					IP:   net.IPv4(1, 2, 3, 4).To4(),
					Port: 0x5678,
				},
			},
		},
	}, "d1:rd2:id0:5:token0:6:valuesl6:\x01\x02\x03\x04\x56\x78ee1:t2:\x8c%1:y1:re")
}

func TestAnnounceTimeout(t *testing.T) {
	s, err := NewServer(&ServerConfig{
		BootstrapNodes: []string{"1.2.3.4:5"},
	})
	require.NoError(t, err)
	a, err := s.Announce("12341234123412341234", 0, true)
	<-a.Peers
	a.Close()
	s.Close()
}

func TestEqualPointers(t *testing.T) {
	assert.EqualValues(t, &Msg{R: &Return{}}, &Msg{R: &Return{}})
}
