package bencode_test

import (
	"net"
	"reflect"
	"testing"

	"github.com/anacrolix/dht/v2/krpc"

	"github.com/anacrolix/torrent/bencode"
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
	orig := krpc.Msg{
		T: "420",
		Y: "r",
		R: &krpc.Return{
			Token: func() *string { t := "re-up"; return &t }(),
		},
		IP:       krpc.NodeAddr{IP: net.ParseIP("1.2.3.4"), Port: 1337},
		ReadOnly: true,
	}
	first := marshalAndUnmarshal(tb, orig)
	if !reflect.DeepEqual(orig, first) {
		tb.Fail()
	}
	tb.ReportAllocs()
	tb.ResetTimer()
	for i := 0; i < tb.N; i += 1 {
		marshalAndUnmarshal(tb, orig)
	}
}
