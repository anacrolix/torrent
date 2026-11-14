package torrent

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"math"
	"math/bits"
	"net"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/internal/testutil"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

// ut_metadata messages from peers carrying out-of-range piece indexes must not panic, in any
// metadata state. Negative indexes are protocol violations and should error (dropping the peer).
func TestUtMetadataOutOfBoundsPiece(t *testing.T) {
	pieces := []int{
		-1,
		math.MinInt,
		1,       // Greeting metadata is a single piece.
		1 << 20, // Way beyond maxMetadataSize.
		math.MaxInt,
		// (1<<14)*piece overflows to 0.
		1 << (bits.UintSize - 14),
		// (1<<14)*piece overflows to a negative.
		1 << (bits.UintSize - 15),
	}
	states := []string{"no-size", "size-known", "have-info"}
	msgTypes := []pp.ExtendedMetadataRequestMsgType{
		pp.RequestMetadataExtensionMsgType,
		pp.DataMetadataExtensionMsgType,
		pp.RejectMetadataExtensionMsgType,
	}
	for _, state := range states {
		for _, msgType := range msgTypes {
			for _, piece := range pieces {
				t.Run(fmt.Sprintf("%s/type%d/piece%d", state, msgType, piece), func(t *testing.T) {
					cl, err := NewClient(TestingConfig(t))
					qt.Assert(t, qt.IsNil(err))
					defer cl.Close()
					mi := testutil.GreetingMetaInfo()
					tt, _ := cl.AddTorrentInfoHash(mi.HashInfoBytes())
					cl.lock()
					defer cl.unlock()
					switch state {
					case "size-known":
						qt.Assert(t, qt.IsNil(tt.setMetadataSize(len(mi.InfoBytes))))
					case "have-info":
						qt.Assert(t, qt.IsNil(tt.setInfoBytesLocked(mi.InfoBytes, nil)))
					}
					ours, theirs := net.Pipe()
					defer theirs.Close()
					go io.Copy(io.Discard, theirs)
					c := cl.newConnection(ours, newConnectionOpts{network: "test"})
					c.setTorrent(tt)
					c.startMessageWriter()
					defer c.closed.Set()
					c.PeerExtensionIDs = map[pp.ExtensionName]pp.ExtensionNumber{pp.ExtensionNameMetadata: 1}
					payload := append(bencode.MustMarshal(pp.ExtendedMetadataRequestMsg{
						Piece:     piece,
						TotalSize: len(mi.InfoBytes),
						Type:      msgType,
					}), 'x')
					err = cl.gotMetadataExtensionMsg(payload, tt, c)
					switch {
					case piece < 0:
						qt.Check(t, qt.IsNotNil(err))
					case msgType == pp.RequestMetadataExtensionMsgType:
						// Rejected, but not a protocol error.
						qt.Check(t, qt.IsNil(err))
					case msgType == pp.DataMetadataExtensionMsgType:
						// We never requested it.
						qt.Check(t, qt.IsNotNil(err))
					}
					if state == "have-info" {
						qt.Check(t, qt.IsFalse(tt.haveMetadataPiece(piece)))
					}
				})
			}
		}
	}
}

// Over the wire, a peer that knows only the infohash sends a ut_metadata request for piece -1.
// This used to panic the client's connection goroutine, crashing the process.
func TestUtMetadataNegativePieceRequestOverWire(t *testing.T) {
	cl, err := NewClient(TestingConfig(t))
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()
	ih := testutil.GreetingMetaInfo().HashInfoBytes()
	cl.AddTorrentInfoHash(ih)
	conn, err := net.Dial("tcp", cl.ListenAddrs()[0].String())
	qt.Assert(t, qt.IsNil(err))
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	var peerID [20]byte
	copy(peerID[:], "-XX0000-ut-metadata-")
	_, err = pp.Handshake(ctx, conn, &ih, peerID, pp.NewPeerExtensionBytes(pp.ExtensionBitLtep, pp.ExtensionBitFast))
	qt.Assert(t, qt.IsNil(err))
	_, err = conn.Write(pp.Message{
		Type:       pp.Extended,
		ExtendedID: pp.HandshakeExtendedID,
		ExtendedPayload: bencode.MustMarshal(pp.ExtendedHandshakeMessage{
			M: map[pp.ExtensionName]pp.ExtensionNumber{pp.ExtensionNameMetadata: 3},
		}),
	}.MustMarshalBinary())
	qt.Assert(t, qt.IsNil(err))
	d := pp.Decoder{R: bufio.NewReader(conn), MaxLength: 1 << 20}
	var theirID pp.ExtensionNumber
	for theirID == 0 {
		var m pp.Message
		qt.Assert(t, qt.IsNil(d.Decode(&m)))
		if m.Type == pp.Extended && m.ExtendedID == pp.HandshakeExtendedID {
			var hs pp.ExtendedHandshakeMessage
			qt.Assert(t, qt.IsNil(bencode.Unmarshal(m.ExtendedPayload, &hs)))
			theirID = hs.M[pp.ExtensionNameMetadata]
		}
	}
	_, err = conn.Write(pp.Message{
		Type:       pp.Extended,
		ExtendedID: theirID,
		ExtendedPayload: bencode.MustMarshal(pp.ExtendedMetadataRequestMsg{
			Piece: -1,
			Type:  pp.RequestMetadataExtensionMsgType,
		}),
	}.MustMarshalBinary())
	qt.Assert(t, qt.IsNil(err))
	// The client should drop us, and stay up.
	for {
		var m pp.Message
		if d.Decode(&m) != nil {
			break
		}
	}
}
