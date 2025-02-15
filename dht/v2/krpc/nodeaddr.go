package krpc

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"strconv"

	"github.com/james-lawrence/torrent/bencode"
)

func NewNodeAddrFromAddrPort(ap netip.AddrPort) NodeAddr {
	return NodeAddr{
		IP:   ap.Addr(),
		Port: int(ap.Port()),
	}
}

func NewNodeAddrFromIPPort(ip net.IP, port int) NodeAddr {
	if ip == nil {
		ip = net.IPv4zero
	}
	ipaddr := netip.AddrFrom16([16]byte(ip.To16()))
	if ip.To4() != nil {
		ipaddr = netip.AddrFrom4([4]byte(ip.To4()))
	}
	return NodeAddr{
		IP:   ipaddr,
		Port: port,
	}
}

type NodeAddr struct {
	IP   netip.Addr
	Port int
}

// A zero Port is taken to mean no port provided, per BEP 7.
func (me NodeAddr) String() string {
	if me.Port == 0 {
		return me.IP.String()
	}
	return net.JoinHostPort(me.IP.String(), strconv.FormatInt(int64(me.Port), 10))
}

func (me *NodeAddr) UnmarshalBinary(b []byte) error {
	var (
		ok     bool
		offset = len(b) - 2
	)
	if me.IP, ok = netip.AddrFromSlice(b[:offset]); !ok {
		return fmt.Errorf("unable to create netip.Addr from %v", b)
	}
	me.Port = int(binary.BigEndian.Uint16(b[offset:]))
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
	b.Write(me.IP.AsSlice())
	binary.Write(&b, binary.BigEndian, uint16(me.Port))
	return b.Bytes(), nil
}

func (me NodeAddr) MarshalBencode() ([]byte, error) {
	return bencodeBytesResult(me.MarshalBinary())
}

func (me NodeAddr) UDP() *net.UDPAddr {
	return &net.UDPAddr{
		IP:   me.IP.AsSlice(),
		Port: me.Port,
	}
}

func (me *NodeAddr) FromUDPAddr(ua *net.UDPAddr) {
	me.IP, _ = netip.AddrFromSlice(ua.IP)
	me.Port = ua.Port
}
