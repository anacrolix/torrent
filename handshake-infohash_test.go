package torrent

import (
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	g "github.com/anacrolix/generics"
	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/mse"
	pp "github.com/anacrolix/torrent/peer_protocol"
	infohash_v2 "github.com/anacrolix/torrent/types/infohash-v2"
)

// Info for a hybrid torrent arrives while an outgoing handshake is in flight. The MSE secret key,
// the infohash we send and the reply check must all use the infohash from when the handshake
// started, even though the canonical infohash changes from truncated v2 to v1.
func TestInitiateHandshakesInfohashChangesMidHandshake(t *testing.T) {
	mi, err := metainfo.LoadFromFile("testdata/bittorrent-v2-hybrid-test.torrent")
	qt.Assert(t, qt.IsNil(err))
	v1 := mi.HashInfoBytes()
	v2 := infohash_v2.HashBytes(mi.InfoBytes)
	v2Short := *v2.ToShort()

	type reply struct {
		ih     metainfo.Hash
		ok     bool
		wantV2 bool
	}
	for _, start := range []struct {
		name        string
		opts        AddTorrentOpts
		infoKnown   bool
		wantSent    metainfo.Hash
		wantUpgrade bool
		replies     []reply
	}{
		{
			name:     "v1",
			opts:     AddTorrentOpts{InfoHash: v1},
			wantSent: v1,
			replies:  []reply{{v1, true, false}, {v2Short, false, false}},
		},
		{
			name:     "v2",
			opts:     AddTorrentOpts{InfoHashV2: g.Some(v2)},
			wantSent: v2Short,
			replies:  []reply{{v2Short, true, true}, {v1, false, false}},
		},
		{
			name:     "truncatedV2AsV1",
			opts:     AddTorrentOpts{InfoHash: v2Short},
			wantSent: v2Short,
			replies:  []reply{{v2Short, true, true}, {v1, false, false}},
		},
		{
			name:        "hybridWithInfo",
			opts:        AddTorrentOpts{InfoHash: v1, InfoHashV2: g.Some(v2), InfoBytes: mi.InfoBytes},
			infoKnown:   true,
			wantSent:    v1,
			wantUpgrade: true,
			replies:     []reply{{v1, true, false}, {v2Short, true, true}},
		},
	} {
		for _, encrypted := range []bool{false, true} {
			for _, r := range start.replies {
				name := fmt.Sprintf("%s/encrypted=%v/reply=%x", start.name, encrypted, r.ih[:4])
				t.Run(name, func(t *testing.T) {
					cfg := TestingConfig(t)
					cfg.DisableTCP = true
					cfg.DisableUTP = true
					cl, err := NewClient(cfg)
					qt.Assert(t, qt.IsNil(err))
					defer cl.Close()
					opts := start.opts
					opts.DisableInitialPieceCheck = true
					tor, _ := cl.AddTorrentOpt(opts)

					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					local, remote := net.Pipe()
					defer local.Close()
					defer remote.Close()
					deadline, _ := ctx.Deadline()
					qt.Assert(t, qt.IsNil(local.SetDeadline(deadline)))
					qt.Assert(t, qt.IsNil(remote.SetDeadline(deadline)))

					type peerResult struct {
						sent     metainfo.Hash
						reserved pp.PeerExtensionBits
						err      error
					}
					peerDone := make(chan peerResult, 1)
					go func() {
						var res peerResult
						defer func() { peerDone <- res }()
						var rw io.ReadWriter = remote
						if encrypted {
							// Only the expected key is offered, so a different MSE secret key fails.
							rw, _, res.err = mse.ReceiveHandshake(ctx, rw, func(yield func([]byte) bool) {
								yield(start.wantSent[:])
							}, mse.DefaultCryptoSelector)
							if res.err != nil {
								return
							}
						}
						b := make([]byte, 68)
						if _, res.err = io.ReadFull(rw, b); res.err != nil {
							return
						}
						copy(res.reserved[:], b[20:28])
						copy(res.sent[:], b[28:48])
						// Info arrives after the initiator has committed to an infohash, and
						// before the peer replies.
						if !start.infoKnown {
							if res.err = tor.SetInfoBytes(mi.InfoBytes); res.err != nil {
								return
							}
						}
						copy(b[28:48], r.ih[:])
						_, res.err = rw.Write(b)
					}()
					conn := &PeerConn{r: local, w: local}
					conn.headerEncrypted = encrypted
					err = cl.initiateHandshakes(ctx, conn, tor)
					peer := <-peerDone
					qt.Assert(t, qt.IsNil(peer.err))
					qt.Check(t, qt.Equals(peer.sent, start.wantSent))
					qt.Check(t, qt.Equals(peer.reserved.GetBit(pp.ExtensionBitV2Upgrade), start.wantUpgrade))
					if r.ok {
						qt.Check(t, qt.IsNil(err))
						qt.Check(t, qt.Equals(conn.v2, r.wantV2))
					} else {
						qt.Check(t, qt.IsNotNil(err))
					}
					// The canonical infohash changes when info arrives for a torrent added by v2.
					qt.Check(t, qt.Equals(tor.InfoHash(), v1))
				})
			}
		}
	}
}
