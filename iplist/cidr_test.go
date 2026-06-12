package iplist

import (
	"bytes"
	"net"
	"testing"

	qt "github.com/go-quicktest/qt"
)

func TestIPNetLast(t *testing.T) {
	_, in, err := net.ParseCIDR("138.255.252.0/22")
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(in.IP, []byte{138, 255, 252, 0}))
	qt.Check(t, qt.DeepEquals(in.Mask, []byte{255, 255, 252, 0}))
	qt.Check(t, qt.DeepEquals(IPNetLast(in), []byte{138, 255, 255, 255}))
	_, in, err = net.ParseCIDR("2400:cb00::/31")
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(in.IP, []byte{0x24, 0, 0xcb, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}))
	qt.Check(t, qt.DeepEquals(in.Mask, []byte{255, 255, 255, 254, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}))
	qt.Check(t, qt.DeepEquals(IPNetLast(in), []byte{0x24, 0, 0xcb, 1, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}))
}

func TestParseCIDRList(t *testing.T) {
	r := bytes.NewBufferString(`2400:cb00::/32
2405:8100::/32
2405:b500::/32
2606:4700::/32
2803:f800::/32
2c0f:f248::/32
2a06:98c0::/29
`)
	rs, err := ParseCIDRListReader(r)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.HasLen(rs, 7))
	qt.Check(t, qt.DeepEquals(rs[4], Range{
		First: net.IP{0x28, 3, 0xf8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
		Last:  net.IP{0x28, 3, 0xf8, 0, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
	}))
}
