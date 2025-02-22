package krpc

import (
	"encoding/hex"
	"fmt"
	"math"
	"math/rand"
	"net"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	qt "github.com/frankban/quicktest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testMarshalUnmarshalMsg(t *testing.T, m Msg, expected string) {
	c := qt.New(t)
	b, err := bencode.Marshal(m)
	c.Assert(err, qt.IsNil)
	c.Assert(string(b), qt.Equals, expected)
	var _m Msg
	err = bencode.Unmarshal([]byte(expected), &_m)
	c.Assert(err, qt.IsNil)
	c.Assert(_m, qt.ContentEquals, m)
	c.Assert(_m.A, qt.ContentEquals, m.A)
	c.Assert(_m.R, qt.ContentEquals, m.R)
}

func TestMarshalUnmarshalMsg(t *testing.T) {
	//{
	//	"r":
	//	{
	//		"id": <20 byte id of sending node (string)>,
	//		"interval": <the subset refresh interval in seconds (integer)>,
	//		"nodes": <nodes close to 'target'>,
	//		"num": <number of infohashes in storage (integer)>,
	//		"samples": <subset of stored infohashes, N × 20 bytes (string)>
	//	},
	//	"t": <transaction-id (string)>,
	//	"y": "r"
	//}
	// Test BEP 51 features
	testMarshalUnmarshalMsg(t, Msg{
		R: &Return{
			ID: IdFromString("hellohellohellohello"),
			Bep51Return: Bep51Return{
				Interval: func() *int64 { var ret int64 = 420; return &ret }(),
				// Num:      func() *int64 { var ret int64 = 69; return &ret }(),
				Samples: func() *CompactInfohashes { var ret CompactInfohashes; return &ret }(),
			},
		},
		T: "hello",
		Y: "r",
	}, `d1:rd2:id20:hellohellohellohello8:intervali420e7:samples0:e1:t5:hello1:y1:re`)
	testMarshalUnmarshalMsg(t, Msg{}, "d1:t0:1:y0:e")
	testMarshalUnmarshalMsg(t, Msg{
		Y: "q",
		Q: "ping",
		T: "hi",
	}, "d1:q4:ping1:t2:hi1:y1:qe")
	testMarshalUnmarshalMsg(t, Msg{
		Y: "e",
		T: "42",
		E: &Error{Code: 200, Msg: "fuck"},
	}, "d1:eli200e4:fucke1:t2:421:y1:ee")
	testMarshalUnmarshalMsg(t, Msg{
		Y: "r",
		T: "\x8c%",
		R: &Return{},
	}, "d1:rd2:id20:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00e1:t2:\x8c%1:y1:re")
	testMarshalUnmarshalMsg(t,
		Msg{
			Y: "r",
			T: "\x8c%",
			R: &Return{
				Nodes: CompactIPv4NodeInfo{
					NodeInfo{
						Addr: NodeAddr{
							IP:   net.IPv4(1, 2, 3, 4).To4(),
							Port: 0x1234,
						},
					},
				},
			},
		},
		"d1:rd2:id20:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x005:nodes26:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x01\x02\x03\x04\x124e1:t2:\x8c%1:y1:re")
	testMarshalUnmarshalMsg(t, Msg{
		Y: "r",
		T: "\x8c%",
		R: &Return{
			Values: []NodeAddr{
				{
					IP:   net.IPv4(1, 2, 3, 4).To4(),
					Port: 0x5678,
				},
			},
		},
	}, "d1:rd2:id20:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x006:valuesl6:\x01\x02\x03\x04\x56\x78ee1:t2:\x8c%1:y1:re")
	testMarshalUnmarshalMsg(t, Msg{
		Y: "r",
		T: "\x03",
		R: &Return{
			ID: IdFromString("\xeb\xff6isQ\xffJ\xec)ͺ\xab\xf2\xfb\xe3F|\xc2g"),
		},
		IP: NodeAddr{
			IP:   net.IPv4(124, 168, 180, 8).To4(),
			Port: 62844,
		},
	}, "d2:ip6:|\xa8\xb4\b\xf5|1:rd2:id20:\xeb\xff6isQ\xffJ\xec)ͺ\xab\xf2\xfb\xe3F|\xc2ge1:t1:\x031:y1:re")

	var k [32]byte
	rand.Read(k[:])
	var sig [64]byte
	rand.Read(sig[:])
	testMarshalUnmarshalMsg(t, Msg{
		A: &MsgArgs{
			V:    nil,
			Seq:  new(int64),
			Cas:  0,
			K:    k,
			Salt: nil,
			Sig:  sig,
		},
	}, "d1:ad2:id20:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x001:k32:"+
		string(k[:])+"3:seqi0e3:sig64:"+string(sig[:])+"e1:t0:1:y0:e")
	testMarshalUnmarshalMsg(t, Msg{
		R: &Return{
			Bep44Return: Bep44Return{
				V:   bencode.MustMarshal([]interface{}{"tee", "hee"}),
				Seq: new(int64),
				K:   k,
				Sig: sig,
			},
		},
	}, "d1:rd2:id20:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x001:k32:"+
		string(k[:])+"3:seqi0e3:sig64:"+string(sig[:])+"1:vl3:tee3:heeee1:t0:1:y0:e")
}

func TestMsgReadOnly(t *testing.T) {
	testMarshalUnmarshalMsg(t, Msg{ReadOnly: true}, "d2:roi1e1:t0:1:y0:e")
	testMarshalUnmarshalMsg(t, Msg{ReadOnly: false}, "d1:t0:1:y0:e")
	var m Msg
	require.NoError(t, bencode.Unmarshal([]byte("de"), &m))
	require.EqualValues(t, Msg{}, m)
	require.NoError(t, bencode.Unmarshal([]byte("d2:roi1ee"), &m))
	require.EqualValues(t, Msg{ReadOnly: true}, m)
	require.NoError(t, bencode.Unmarshal([]byte("d2:roi0ee"), &m))
	require.EqualValues(t, Msg{}, m)
}

func TestUnmarshalGetPeersResponse(t *testing.T) {
	var msg Msg
	err := bencode.Unmarshal([]byte("d1:rd6:valuesl6:\x01\x02\x03\x04\x05\x066:\x07\x08\x09\x0a\x0b\x0ce5:nodes52:\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x02\x03\x04\x05\x06\x07\x08\x09\x02\x03\x04\x05\x06\x07\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x02\x03\x04\x05\x06\x07\x08\x09\x02\x03\x04\x05\x06\x07ee"), &msg)
	require.NoError(t, err)
	assert.Len(t, msg.R.Values, 2)
	assert.Len(t, msg.R.Nodes, 2)
	assert.Nil(t, msg.E)
}

func unprettifyHex(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, " ", ""), "\n", "")
}

func TestBep33BloomFilter(t *testing.T) {
	var f ScrapeBloomFilter
	for i := 0; i <= 255; i++ {
		f.AddIp(net.IPv4(192, 0, 2, byte(i)).To4())
	}
	for i := 0; i <= 0x3e7; i++ {
		f.AddIp(net.ParseIP(fmt.Sprintf("2001:DB8::%x", i)))
	}
	expected, err := hex.DecodeString(unprettifyHex(`
F6C3F5EA A07FFD91 BDE89F77 7F26FB2B FF37BDB8 FB2BBAA2 FD3DDDE7 BACFFF75 EE7CCBAE
FE5EEDB1 FBFAFF67 F6ABFF5E 43DDBCA3 FD9B9FFD F4FFD3E9 DFF12D1B DF59DB53 DBE9FA5B
7FF3B8FD FCDE1AFB 8BEDD7BE 2F3EE71E BBBFE93B CDEEFE14 8246C2BC 5DBFF7E7 EFDCF24F
D8DC7ADF FD8FFFDF DDFFF7A4 BBEEDF5C B95CE81F C7FCFF1F F4FFFFDF E5F7FDCB B7FD79B3
FA1FC77B FE07FFF9 05B7B7FF C7FEFEFF E0B8370B B0CD3F5B 7F2BD93F EB4386CF DD6F7FD5
BFAF2E9E BFFFFEEC D67ADBF7 C67F17EF D5D75EBA 6FFEBA7F FF47A91E B1BFBB53 E8ABFB57
62ABE8FF 237279BF EFBFEEF5 FFC5FEBF DFE5ADFF ADFEE1FB 737FFFFB FD9F6AEF FEEE76B6
FD8F72EF
`))
	require.NoError(t, err)
	assert.EqualValues(t, expected, f[:])
	assert.EqualValues(t, 1224.9308, floorDecimals(f.EstimateCount(), 4))
}

func floorDecimals(f float64, decimals int) float64 {
	p := math.Pow10(decimals)
	return math.Floor(f*p) / p
}

func TestEmptyScrapeBloomFilterEstimatedCount(t *testing.T) {
	var f ScrapeBloomFilter
	assert.EqualValues(t, 0, math.Floor(f.EstimateCount()))
}

func marshalAndReturnUnmarshaledMsg(t *testing.T, m Msg, expected string) (ret Msg) {
	c := qt.New(t)
	b, err := bencode.Marshal(m)
	c.Assert(err, qt.IsNil)
	c.Assert(string(b), qt.Equals, expected)
	err = bencode.Unmarshal([]byte(expected), &ret)
	c.Assert(err, qt.IsNil)
	return
}

func TestBep51EmptySampleField(t *testing.T) {
	testMarshalUnmarshalMsg(t,
		Msg{
			R: &Return{},
		},
		"d1:rd2:id20:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00e1:t0:1:y0:e",
	)
	samples := marshalAndReturnUnmarshaledMsg(t,
		Msg{
			R: &Return{
				Bep51Return: Bep51Return{
					Samples: &CompactInfohashes{},
				},
			},
		},
		"d1:rd2:id20:\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x007:samples0:e1:t0:1:y0:e",
	).R.Samples
	c := qt.New(t)
	c.Assert(samples, qt.Not(qt.IsNil))
	c.Assert(*samples, qt.HasLen, 0)
}
