package utHolepunch

import (
	"bytes"
	"net/netip"
	"testing"

	qt "github.com/go-quicktest/qt"
)

var exampleMsgs = []Msg{
	{
		MsgType:  Rendezvous,
		AddrPort: netip.MustParseAddrPort("[1234::1]:42069"),
		ErrCode:  16777216,
	},
	{
		MsgType:  Connect,
		AddrPort: netip.MustParseAddrPort("1.2.3.4:42069"),
		ErrCode:  16777216,
	},
}

func TestUnmarshalMsg(t *testing.T) {
	for _, m := range exampleMsgs {
		b, err := m.MarshalBinary()
		qt.Assert(t, qt.IsNil(err))
		expectedLen := 24
		if m.AddrPort.Addr().Is4() {
			expectedLen = 12
		}
		qt.Check(t, qt.HasLen(b, expectedLen))
		var um Msg
		err = um.UnmarshalBinary(b)
		qt.Assert(t, qt.IsNil(err))
		qt.Check(t, qt.Equals(um, m))
	}
}

func FuzzMsg(f *testing.F) {
	for _, m := range exampleMsgs {
		emb, err := m.MarshalBinary()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(emb)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		var m Msg
		err := m.UnmarshalBinary(b)
		if err != nil {
			t.SkipNow()
		}
		mb, err := m.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(b, mb) {
			t.FailNow()
		}
	})
}
