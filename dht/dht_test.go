package dht

import (
	"math/big"
	"math/rand"
	"net"
	"testing"
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
		m[id.String()] = true
	}
	if !m[testIDs[3].String()] || !m[testIDs[4].String()] {
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

func TestPing(t *testing.T) {
	srv, err := NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	defer srv.Close()

	srv0, err := NewServer(nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv0.Serve()
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
		ok <- msg.ID() == srv0.ID()
	})
	if !<-ok {
		t.FailNow()
	}
}
