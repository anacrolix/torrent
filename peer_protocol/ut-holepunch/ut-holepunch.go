package utHolepunch

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/netip"
)

const ExtensionName = "ut_holepunch"

type (
	Msg struct {
		MsgType  MsgType
		AddrPort netip.AddrPort
		ErrCode  ErrCode
	}
	MsgType  byte
	AddrType byte
)

const (
	Rendezvous MsgType = iota
	Connect
	Error
)

func (me MsgType) String() string {
	switch me {
	case Rendezvous:
		return "rendezvous"
	case Connect:
		return "connect"
	case Error:
		return "error"
	default:
		return fmt.Sprintf("unknown %d", me)
	}
}

const (
	Ipv4 AddrType = iota
	Ipv6 AddrType = iota
)

func (m *Msg) UnmarshalBinary(b []byte) error {
	if len(b) < 12 {
		return fmt.Errorf("buffer too small to be valid")
	}
	m.MsgType = MsgType(b[0])
	b = b[1:]
	addrType := AddrType(b[0])
	b = b[1:]
	var addr netip.Addr
	switch addrType {
	case Ipv4:
		addr = netip.AddrFrom4(*(*[4]byte)(b[:4]))
		b = b[4:]
	case Ipv6:
		if len(b) < 22 {
			return fmt.Errorf("not enough bytes")
		}
		addr = netip.AddrFrom16(*(*[16]byte)(b[:16]))
		b = b[16:]
	default:
		return fmt.Errorf("unhandled addr type value %v", addrType)
	}
	port := binary.BigEndian.Uint16(b[:])
	b = b[2:]
	m.AddrPort = netip.AddrPortFrom(addr, port)
	m.ErrCode = ErrCode(binary.BigEndian.Uint32(b[:]))
	b = b[4:]
	if len(b) != 0 {
		return fmt.Errorf("%v trailing unused bytes", len(b))
	}
	return nil
}

func (m *Msg) MarshalBinary() (_ []byte, err error) {
	var buf bytes.Buffer
	buf.Grow(24)
	buf.WriteByte(byte(m.MsgType))
	addr := m.AddrPort.Addr()
	switch {
	case addr.Is4():
		buf.WriteByte(byte(Ipv4))
	case addr.Is6():
		buf.WriteByte(byte(Ipv6))
	default:
		err = fmt.Errorf("unhandled addr type: %v", addr)
		return
	}
	buf.Write(addr.AsSlice())
	binary.Write(&buf, binary.BigEndian, m.AddrPort.Port())
	binary.Write(&buf, binary.BigEndian, m.ErrCode)
	return buf.Bytes(), nil
}
