package torrent

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/internal/testutil"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
)

// Flags in the first byte of FuzzPeerConnMessages input.
const (
	fuzzPeerConnHaveInfo byte = 1 << iota
	fuzzPeerConnFast
	fuzzPeerConnExtended
	fuzzPeerConnDisablePex
)

// Builds FuzzPeerConnMessages input: flags, then each message as it appears on the wire.
func fuzzPeerConnInput(flags byte, msgs ...pp.Message) (b []byte) {
	b = append(b, flags)
	for _, m := range msgs {
		b = append(b, m.MustMarshalBinary()...)
	}
	return
}

func fuzzPeerConnSeeds(infoBytes []byte) (seeds [][]byte) {
	protocols := makeBuiltinLtepProtocols(true)
	local := protocols.toSupportedExtensionDict()
	ext := func(name pp.ExtensionName, payload string) pp.Message {
		return pp.Message{Type: pp.Extended, ExtendedID: local[name], ExtendedPayload: []byte(payload)}
	}
	extHandshake := pp.Message{
		Type:       pp.Extended,
		ExtendedID: pp.HandshakeExtendedID,
		ExtendedPayload: bencode.MustMarshal(pp.ExtendedHandshakeMessage{
			M: map[pp.ExtensionName]pp.ExtensionNumber{
				pp.ExtensionNameMetadata: 1,
				pp.ExtensionNamePex:      2,
			},
			MetadataSize: len(infoBytes),
		}),
	}
	metadata := func(msgType pp.ExtendedMetadataRequestMsgType, piece int, data []byte) pp.Message {
		payload := fmt.Sprintf("d8:msg_typei%de5:piecei%de", msgType, piece)
		if msgType == pp.DataMetadataExtensionMsgType {
			payload += fmt.Sprintf("10:total_sizei%de", len(infoBytes))
		}
		return ext(pp.ExtensionNameMetadata, payload+"e"+string(data))
	}
	for _, msgs := range [][]pp.Message{
		{extHandshake, metadata(pp.DataMetadataExtensionMsgType, 0, infoBytes)},
		{extHandshake, metadata(pp.RequestMetadataExtensionMsgType, 0, nil)},
		{extHandshake, metadata(pp.RequestMetadataExtensionMsgType, -1, nil)},
		{extHandshake, metadata(pp.RejectMetadataExtensionMsgType, 0, nil)},
		{extHandshake, ext(pp.ExtensionNamePex, "d5:added6:\x01\x02\x03\x04\x00\x017:added.f1:\x00e")},
		{
			{Type: pp.Bitfield, Bitfield: []bool{true}},
			{Type: pp.Have, Index: 0},
			{Type: pp.Interested},
			{Type: pp.Unchoke},
			{Type: pp.Request, Index: 0, Begin: 0, Length: 5},
			{Type: pp.Cancel, Index: 0, Begin: 0, Length: 5},
			{Type: pp.Choke},
			{Type: pp.NotInterested},
		},
		{{Type: pp.Piece, Index: 0, Begin: 0, Piece: []byte("hello")}},
		{
			{Type: pp.HaveAll},
			{Type: pp.HaveNone},
			{Type: pp.Suggest, Index: 0},
			{Type: pp.AllowedFast, Index: 0},
			{Type: pp.Port, Port: 1234},
		},
		{{Type: pp.Reject, Index: 0, Begin: 0, Length: 5}},
		{{Type: pp.HashRequest, Length: 2}},
	} {
		for _, flags := range []byte{
			0,
			fuzzPeerConnExtended,
			fuzzPeerConnFast | fuzzPeerConnExtended,
			fuzzPeerConnHaveInfo | fuzzPeerConnFast | fuzzPeerConnExtended,
		} {
			seeds = append(seeds, fuzzPeerConnInput(flags, msgs...))
		}
	}
	return
}

// Reads the remote peer's messages from r, and accepts and discards everything written, so the
// connection stays up until the input runs out. The embedded Conn provides addresses and deadlines.
type fuzzPeerConnConn struct {
	net.Conn
	r io.Reader
}

func (me *fuzzPeerConnConn) Read(b []byte) (int, error) {
	return me.r.Read(b)
}

func (me *fuzzPeerConnConn) Write(b []byte) (int, error) {
	return len(b), nil
}

// Feeds peer wire protocol messages from a remote peer to a connected PeerConn on a torrent with or
// without its info. Any panic, in the read loop or anything it triggers, is a remote crash.
func FuzzPeerConnMessages(f *testing.F) {
	mi := testutil.GreetingMetaInfo()
	for _, s := range fuzzPeerConnSeeds(mi.InfoBytes) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) == 0 {
			return
		}
		flags := b[0]
		b = b[1:]

		dir, err := os.MkdirTemp("", "torrent-fuzz-")
		if err != nil {
			panic(err)
		}
		defer os.RemoveAll(dir)
		cfg := NewDefaultClientConfig()
		cfg.DataDir = dir
		cfg.DefaultStorage = storage.NewFileWithCompletion(dir, storage.NewMapPieceCompletion())
		cfg.DisableTCP = true
		cfg.DisableUTP = true
		cfg.NoDHT = true
		cfg.DisableTrackers = true
		cfg.DisableWebtorrent = true
		cfg.DisableWebseeds = true
		cfg.NoDefaultPortForwarding = true
		cfg.DisablePEX = flags&fuzzPeerConnDisablePex != 0
		cfg.Slogger = slog.New(slog.DiscardHandler)
		cl, err := NewClient(cfg)
		if err != nil {
			panic(err)
		}
		defer cl.Close()
		tt, _ := cl.AddTorrentInfoHash(mi.HashInfoBytes())

		pipe, other := net.Pipe()
		defer pipe.Close()
		defer other.Close()
		ours := &fuzzPeerConnConn{Conn: pipe, r: bytes.NewReader(b)}

		cl.lock()
		defer cl.unlock()
		if flags&fuzzPeerConnHaveInfo != 0 {
			if err := tt.setInfoBytesLocked(mi.InfoBytes, nil); err != nil {
				panic(fmt.Sprintf("setting info: %v", err))
			}
		}
		pc := cl.newConnection(ours, newConnectionOpts{
			remoteAddr: netip.AddrPortFrom(netip.AddrFrom4([4]byte{1, 2, 3, 4}), 1234),
			network:    "tcp",
		})
		pc.PeerExtensionBytes.SetBit(pp.ExtensionBitFast, flags&fuzzPeerConnFast != 0)
		pc.PeerExtensionBytes.SetBit(pp.ExtensionBitLtep, flags&fuzzPeerConnExtended != 0)
		pc.PeerID = [20]byte{'f', 'u', 'z', 'z'}
		// Errors are expected: most inputs are invalid. Only panics matter.
		_ = tt.runHandshookConn(pc)
	})
}
