package util

import (
	"bytes"
	"encoding"
	"encoding/binary"
	"fmt"

	"github.com/anacrolix/libtorgo/bencode"
)

type CompactPeers []CompactPeer

func (me *CompactPeers) UnmarshalBencode(bb []byte) (err error) {
	var b []byte
	err = bencode.Unmarshal(bb, &b)
	if err != nil {
		return
	}
	err = me.UnmarshalBinary(b)
	return
}

func (me *CompactPeers) UnmarshalBinary(b []byte) (err error) {
	for i := 0; i < len(b); i += 6 {
		var p CompactPeer
		err = p.UnmarshalBinary([]byte(b[i : i+6]))
		if err != nil {
			return
		}
		*me = append(*me, p)
	}
	return
}

type CompactPeer struct {
	IP   [4]byte
	Port uint16
}

var _ encoding.BinaryUnmarshaler = &CompactPeer{}

func (cp *CompactPeer) UnmarshalBinary(b []byte) (err error) {
	r := bytes.NewReader(b)
	err = binary.Read(r, binary.BigEndian, cp)
	if err != nil {
		return
	}
	if r.Len() != 0 {
		err = fmt.Errorf("%d bytes unused", r.Len())
	}
	return
}
