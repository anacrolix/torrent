package util

import (
	"encoding"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strconv"

	"github.com/anacrolix/torrent/bencode"
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

func (me CompactPeers) WriteBinary(w io.Writer) (err error) {
	for _, cp := range me {
		cp.Write(w)
		if err != nil {
			return
		}
	}
	return
}

type CompactPeer struct {
	IP   net.IP
	Port uint16
}

var _ encoding.BinaryUnmarshaler = &CompactPeer{}

func (cp *CompactPeer) UnmarshalBinary(b []byte) (err error) {
	switch len(b) {
	case 18:
		cp.IP = make([]byte, 16)
	case 6:
		cp.IP = make([]byte, 4)
	default:
		err = fmt.Errorf("bad length: %d", len(b))
		return
	}
	if n := copy(cp.IP, b); n != len(cp.IP) {
		panic(n)
	}
	b = b[len(cp.IP):]
	if len(b) != 2 {
		panic(len(b))
	}
	cp.Port = binary.BigEndian.Uint16(b)
	return
}

func (cp *CompactPeer) Write(w io.Writer) (err error) {
	_, err = w.Write(cp.IP)
	if err != nil {
		return
	}
	err = binary.Write(w, binary.BigEndian, cp.Port)
	return
}

func (cp *CompactPeer) String() string {
	return net.JoinHostPort(cp.IP.String(), strconv.FormatUint(uint64(cp.Port), 10))
}
