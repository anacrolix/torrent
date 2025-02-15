package bencode_test

import (
	"net/netip"
	"reflect"
	"testing"

	"github.com/bradfitz/iter"
	"github.com/james-lawrence/torrent/dht/v2/krpc"
	"github.com/stretchr/testify/require"

	"github.com/james-lawrence/torrent/bencode"
)

func marshalAndUnmarshal(tb testing.TB, orig krpc.Msg) (ret krpc.Msg) {
	b, err := bencode.Marshal(orig)
	if err != nil {
		tb.Fatal(err)
	}
	err = bencode.Unmarshal(b, &ret)
	if err != nil {
		tb.Fatal(err)
	}
	// ret.Q = "what"
	return
}

func BenchmarkMarshalThenUnmarshalKrpcMsg(tb *testing.B) {
	ip, err := netip.ParseAddr("1.2.3.4")
	require.NoError(tb, err)
	orig := krpc.Msg{
		T: "420",
		Y: "r",
		R: &krpc.Return{
			Token: func() *string { t := "re-up"; return &t }(),
		},
		IP:       krpc.NodeAddr{IP: ip, Port: 1337},
		ReadOnly: true,
	}
	first := marshalAndUnmarshal(tb, orig)
	if !reflect.DeepEqual(orig, first) {
		tb.Fail()
	}
	tb.ReportAllocs()
	tb.ResetTimer()
	for range iter.N(tb.N) {
		marshalAndUnmarshal(tb, orig)
	}
}
