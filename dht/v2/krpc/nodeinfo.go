package krpc

import (
	"bytes"
	crand "crypto/rand"
	"encoding"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"net"
	"net/netip"
)

type NodeInfo struct {
	ID   [20]byte
	Addr NodeAddr
}

func (me NodeInfo) String() string {
	return fmt.Sprintf("{%x at %s}", me.ID, me.Addr)
}

func RandomNodeInfo(ipLen int) (ni NodeInfo) {
	tmp := make(net.IP, ipLen)
	crand.Read(ni.ID[:])
	crand.Read(tmp)
	ni.Addr.IP, _ = netip.AddrFromSlice(tmp)
	ni.Addr.Port = rand.Intn(math.MaxUint16 + 1)
	return
}

var _ interface {
	encoding.BinaryMarshaler
	encoding.BinaryUnmarshaler
} = (*NodeInfo)(nil)

func (ni NodeInfo) MarshalBinary() ([]byte, error) {
	var w bytes.Buffer
	w.Write(ni.ID[:])
	w.Write(ni.Addr.IP.AsSlice())
	binary.Write(&w, binary.BigEndian, uint16(ni.Addr.Port))
	return w.Bytes(), nil
}

func (ni *NodeInfo) UnmarshalBinary(b []byte) error {
	copy(ni.ID[:], b)
	return ni.Addr.UnmarshalBinary(b[20:])
}
