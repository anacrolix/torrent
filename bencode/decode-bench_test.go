package bencode_test

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/anacrolix/dht/v2/krpc"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

func benchKrpcMsg() krpc.Msg {
	tok := "re-up"
	return krpc.Msg{
		T: "420",
		Y: "r",
		R: &krpc.Return{
			ID:    krpc.ID{1, 2, 3},
			Token: &tok,
			Nodes: krpc.CompactIPv4NodeInfo{
				{ID: krpc.ID{4}, Addr: krpc.NodeAddr{IP: net.IPv4(1, 2, 3, 4).To4(), Port: 1}},
				{ID: krpc.ID{5}, Addr: krpc.NodeAddr{IP: net.IPv4(5, 6, 7, 8).To4(), Port: 2}},
			},
		},
		IP:       krpc.NodeAddr{IP: net.ParseIP("1.2.3.4"), Port: 1337},
		ReadOnly: true,
	}
}

func benchUnmarshal[T any](b *testing.B, data []byte) {
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	for b.Loop() {
		var v T
		if err := bencode.Unmarshal(data, &v); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUnmarshalKrpcMsg(b *testing.B) {
	benchUnmarshal[krpc.Msg](b, bencode.MustMarshal(benchKrpcMsg()))
}

func BenchmarkUnmarshalExtendedHandshake(b *testing.B) {
	benchUnmarshal[pp.ExtendedHandshakeMessage](b, bencode.MustMarshal(pp.ExtendedHandshakeMessage{
		M: map[pp.ExtensionName]pp.ExtensionNumber{
			"ut_metadata": 1, "ut_pex": 2, "ut_holepunch": 3,
		},
		V:            "Transmission 4.0",
		Reqq:         512,
		MetadataSize: 12345,
		Port:         6881,
		YourIp:       pp.CompactIp(net.IPv4(1, 2, 3, 4).To4()),
	}))
}

func benchTorrentFiles(b *testing.B, f func(b *testing.B, data []byte)) {
	paths, err := filepath.Glob("../metainfo/testdata/*.torrent")
	if err != nil || len(paths) == 0 {
		b.Fatal(paths, err)
	}
	for _, name := range []string{"archlinux-2011.08.19-netinstall-i686.iso.torrent", "continuum.torrent", "23516C72685E8DB0C8F15553382A927F185C4F01.torrent"} {
		data, err := os.ReadFile(filepath.Join("../metainfo/testdata", name))
		if err != nil {
			b.Fatal(err)
		}
		b.Run(name, func(b *testing.B) { f(b, data) })
	}
}

func BenchmarkUnmarshalInterfaceTorrent(b *testing.B) {
	benchTorrentFiles(b, benchUnmarshal[any])
}

func BenchmarkUnmarshalMetaInfo(b *testing.B) {
	benchTorrentFiles(b, benchUnmarshal[metainfo.MetaInfo])
}

func BenchmarkUnmarshalInfo(b *testing.B) {
	benchTorrentFiles(b, func(b *testing.B, data []byte) {
		var mi metainfo.MetaInfo
		if err := bencode.Unmarshal(data, &mi); err != nil {
			b.Fatal(err)
		}
		benchUnmarshal[metainfo.Info](b, mi.InfoBytes)
	})
}

func BenchmarkDecoderMetaInfo(b *testing.B) {
	benchTorrentFiles(b, func(b *testing.B, data []byte) {
		b.ReportAllocs()
		b.SetBytes(int64(len(data)))
		for b.Loop() {
			var mi metainfo.MetaInfo
			if err := bencode.NewDecoder(bytes.NewReader(data)).Decode(&mi); err != nil {
				b.Fatal(err)
			}
		}
	})
}
