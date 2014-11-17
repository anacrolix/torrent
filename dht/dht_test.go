package dht

import (
	"math/rand"
	"net"
	"testing"
)

func TestMarshalCompactNodeInfo(t *testing.T) {
	cni := NodeInfo{
		ID: [20]byte{'a', 'b', 'c'},
	}
	addr, err := net.ResolveUDPAddr("udp4", "1.2.3.4:5")
	if err != nil {
		t.Fatal(err)
	}
	cni.Addr = newDHTAddr(addr)
	var b [CompactNodeInfoLen]byte
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

var testIDs = []string{
	zeroID,
	"\x03" + zeroID[1:],
	"\x03" + zeroID[1:18] + "\x55\xf0",
	"\x55" + zeroID[1:17] + "\xff\x55\x0f",
	"\x54" + zeroID[1:18] + "\x50\x0f",
}

func TestDistances(t *testing.T) {
	if idDistance(testIDs[3], testIDs[0]) != 4+8+4+4 {
		t.FailNow()
	}
	if idDistance(testIDs[3], testIDs[1]) != 4+8+4+4 {
		t.FailNow()
	}
	if idDistance(testIDs[3], testIDs[2]) != 4+8+8 {
		t.FailNow()
	}
}

func TestBadIdStrings(t *testing.T) {
	var a, b string
	recoverPanicOrDie(t, func() {
		idDistance(a, b)
	})
	recoverPanicOrDie(t, func() {
		idDistance(a, zeroID)
	})
	recoverPanicOrDie(t, func() {
		idDistance(zeroID, b)
	})
	if idDistance(zeroID, zeroID) != 0 {
		t.FailNow()
	}
	a = "\x03" + zeroID[1:]
	b = zeroID
	if idDistance(a, b) != 2 {
		t.FailNow()
	}
	a = "\x03" + zeroID[1:18] + "\x55\xf0"
	b = "\x55" + zeroID[1:17] + "\xff\x55\x0f"
	if c := idDistance(a, b); c != 20 {
		t.Fatal(c)
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
		m[id] = true
	}
	if !m[testIDs[3]] || !m[testIDs[4]] {
		t.FailNow()
	}
}

// func TestUnmarshalGetPeersResponse(t *testing.T) {
// 	var gpr getPeersResponse
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// 	err = dec.Decode(map[string]interface{}{
// 		"values": []string{"\x01\x02\x03\x04\x05\x06", "\x07\x08\x09\x0a\x0b\x0c"},
// 		"nodes":  "\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d",
// 	})
// 	if err != nil {
// 		t.Fatal(err)
// 	}
// 	if len(gpr.Values) != 2 {
// 		t.FailNow()
// 	}
// 	if len(gpr.Nodes) != 2 {
// 		t.FailNow()
// 	}
// }

func TestDHTDefaultConfig(t *testing.T) {
	s, err := NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}
