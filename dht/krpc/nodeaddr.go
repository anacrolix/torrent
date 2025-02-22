package krpc

import (
	"bytes"
	"encoding/binary"
	"net"
	"net/netip"
	"strconv"

	"github.com/anacrolix/torrent/bencode"
)

// This will be deprecated in favour of NodeAddrPort.
type NodeAddr struct {
	IP   net.IP
	Port int
}

func (me *NodeAddr) FromAddrPort(f netip.AddrPort) {
	me.IP = f.Addr().AsSlice()
	me.Port = int(f.Port())
}

func (me NodeAddr) ToNodeAddrPort() NodeAddrPort {
	addr, _ := netip.AddrFromSlice(me.IP)
	return NodeAddrPort{netip.AddrPortFrom(addr, uint16(me.Port))}
}

// A zero Port is taken to mean no port provided, per BEP 7.
func (me NodeAddr) String() string {
	return net.JoinHostPort(me.IP.String(), strconv.FormatInt(int64(me.Port), 10))
}

func (me *NodeAddr) UnmarshalBinary(b []byte) error {
	me.IP = make(net.IP, len(b)-2)
	copy(me.IP, b[:len(b)-2])
	me.Port = int(binary.BigEndian.Uint16(b[len(b)-2:]))
	return nil
}

func (me *NodeAddr) UnmarshalBencode(b []byte) (err error) {
	var _b []byte
	err = bencode.Unmarshal(b, &_b)
	if err != nil {
		return
	}
	return me.UnmarshalBinary(_b)
}

func (me NodeAddr) MarshalBinary() ([]byte, error) {
	var b bytes.Buffer
	b.Write(me.IP)
	binary.Write(&b, binary.BigEndian, uint16(me.Port))
	return b.Bytes(), nil
}

func (me NodeAddr) MarshalBencode() ([]byte, error) {
	return bencodeBytesResult(me.MarshalBinary())
}

func (me NodeAddr) UDP() *net.UDPAddr {
	return &net.UDPAddr{
		IP:   me.IP,
		Port: me.Port,
	}
}

func (me *NodeAddr) FromUDPAddr(ua *net.UDPAddr) {
	me.IP = ua.IP
	me.Port = ua.Port
}

func (me NodeAddr) Equal(x NodeAddr) bool {
	return me.IP.Equal(x.IP) && me.Port == x.Port
}
