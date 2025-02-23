package krpc

import (
	"bytes"
	crand "crypto/rand"
	"encoding"
	"encoding/binary"
	"math"
	"math/rand/v2"
	"net"
)

// This is a comparable replacement for NodeInfo.
type NodeInfo struct {
	ID   ID
	Addr NodeAddr
}

func RandomNodeInfo(ipLen int) (ni NodeInfo) {
	tmp := make(net.IP, ipLen)
	crand.Read(ni.ID[:])
	crand.Read(tmp)
	ni.Addr = NewNodeAddrFromIPPort(tmp, rand.IntN(math.MaxUint16+1))
	return
}

var _ interface {
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler
} = (*NodeInfo)(nil)

func (ni NodeInfo) MarshalBinary() ([]byte, error) {
	var w bytes.Buffer
	w.Write(ni.ID[:])
	w.Write(ni.Addr.IP())
	err := binary.Write(&w, binary.BigEndian, ni.Addr.Port())
	return w.Bytes(), err
}

func (ni *NodeInfo) UnmarshalBinary(b []byte) error {
	copy(ni.ID[:], b)
	return ni.Addr.UnmarshalBinary(b[20:])
}
