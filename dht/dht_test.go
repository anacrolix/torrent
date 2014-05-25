package dht

import (
	"net"
	"testing"
)

func TestMarshalCompactNodeInfo(t *testing.T) {
	cni := compactNodeInfo{
		ID: [20]byte{'a', 'b', 'c'},
	}
	var err error
	cni.Addr, err = net.ResolveUDPAddr("udp4", "1.2.3.4:5")
	if err != nil {
		t.Fatal(err)
	}
	var b [compactAddrInfoLen]byte
	cni.PutBinary(b[:])
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
