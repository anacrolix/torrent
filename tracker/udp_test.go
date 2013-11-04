package tracker

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestMarshalUDPAnnounceResponse(t *testing.T) {
	w := bytes.NewBuffer(nil)
	if err := binary.Write(w, binary.BigEndian, &PeerAddrSlice{{1, 2}, {3, 4}}); err != nil {
		t.Fatalf("error writing udp announce response addrs: %s", err)
	}
	if w.String() != "\x00\x00\x00\x01\x00\x02\x00\x00\x00\x03\x00\x04" {
		t.FailNow()
	}
	if binary.Size(UDPAnnounceResponseHeader{}) != 20 {
		t.FailNow()
	}
}
