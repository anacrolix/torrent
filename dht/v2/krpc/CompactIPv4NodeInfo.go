package krpc

import (
	"net/netip"

	"github.com/anacrolix/missinggo/slices"
)

type (
	CompactIPv4NodeInfo []NodeInfo
)

func (CompactIPv4NodeInfo) ElemSize() int {
	return 26
}

func (me CompactIPv4NodeInfo) MarshalBinary() ([]byte, error) {
	return marshalBinarySlice(slices.Map(func(na NodeInfo) NodeInfo {
		na.Addr.IP = netip.AddrFrom4(na.Addr.IP.As4())
		return na
	}, me).(CompactIPv4NodeInfo))
}

func (me CompactIPv4NodeInfo) MarshalBencode() ([]byte, error) {
	return bencodeBytesResult(me.MarshalBinary())
}

func (me *CompactIPv4NodeInfo) UnmarshalBinary(b []byte) error {
	return unmarshalBinarySlice(me, b)
}

func (me *CompactIPv4NodeInfo) UnmarshalBencode(b []byte) error {
	return unmarshalBencodedBinary(me, b)
}
