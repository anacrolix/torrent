package krpc

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"

	"github.com/anacrolix/multiless"
	"github.com/james-lawrence/torrent/bencode"
)

func NewNodeAddrFromAddrPort(ap netip.AddrPort) NodeAddr {
	return NodeAddr{
		AddrPort: ap,
	}
}

func NewNodeAddrFromIPPort(ip net.IP, port int) NodeAddr {
	if ip == nil {
		addr, _ := netip.AddrFromSlice(ip)
		return NodeAddr{
			AddrPort: netip.AddrPortFrom(addr, uint16(port)),
		}
	}

	ipaddr := netip.AddrFrom16([16]byte(ip.To16()))
	if ip.To4() != nil {
		ipaddr = netip.AddrFrom4([4]byte(ip.To4()))
	}

	return NodeAddr{
		AddrPort: netip.AddrPortFrom(ipaddr, uint16(port)),
	}
}

// This is a comparable replacement for NodeAddr.
type NodeAddr struct {
	netip.AddrPort
}

func (me NodeAddr) String() string {
	if me.IsValid() {
		return me.Addr().String()
	}

	return ""
}
func (me NodeAddr) UDP() *net.UDPAddr {
	return &net.UDPAddr{
		IP:   me.Addr().AsSlice(),
		Port: int(me.Port()),
	}
}

func (me NodeAddr) IP() net.IP {
	return me.Addr().AsSlice()
}

func (me NodeAddr) Compare(r NodeAddr) int {
	return multiless.EagerOrdered(
		multiless.New().Cmp(me.Addr().Compare(r.Addr())),
		me.Port(), r.Port(),
	).OrderingInt()
}

func (me NodeAddr) Equal(r NodeAddr) bool {
	return me.Compare(r) == 0
}

func (ni NodeAddr) MarshalBinary() ([]byte, error) {
	var encoded serialized = serialized{
		IP:   ni.IP(),
		Port: int(ni.Port()),
	}

	return encoded.MarshalBinary()
}

func (ni *NodeAddr) UnmarshalBinary(b []byte) error {
	var decoded serialized
	if err := decoded.UnmarshalBinary(b); err != nil {
		return err
	}

	ni.AddrPort = NewNodeAddrFromIPPort(decoded.IP, decoded.Port).AddrPort
	return nil
}

func (ni *NodeAddr) UnmarshalBencode(b []byte) (err error) {
	var (
		decoded serialized
	)

	if err = decoded.UnmarshalBencode(b); err != nil {
		return err
	}

	ni.AddrPort = NewNodeAddrFromIPPort(decoded.IP, decoded.Port).AddrPort
	return nil
}

func (ni NodeAddr) MarshalBencode() ([]byte, error) {
	var encoded serialized = serialized{
		IP:   ni.IP(),
		Port: int(ni.Port()),
	}

	return encoded.MarshalBencode()
}

type serialized struct {
	IP   net.IP
	Port int
}

func (me *serialized) UnmarshalBinary(b []byte) error {
	me.IP = make(net.IP, len(b)-2)
	copy(me.IP, b[:len(b)-2])
	me.Port = int(binary.BigEndian.Uint16(b[len(b)-2:]))
	return nil
}

func (me *serialized) UnmarshalBencode(b []byte) (err error) {
	var _b []byte
	err = bencode.Unmarshal(b, &_b)
	if err != nil {
		return
	}
	return me.UnmarshalBinary(_b)
}

func (me serialized) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	b.Write(me.IP)
	binary.Write(&b, binary.BigEndian, uint16(me.Port))
	return b.Bytes(), nil
}

func (me serialized) MarshalBencode() ([]byte, error) {
	return bencodeBytesResult(me.MarshalBinary())
}
