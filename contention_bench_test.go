package torrent

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

// K conns on one torrent, each running mainReadLoop concurrently, to measure client lock
// contention in the read path.
func BenchmarkContentionMainReadLoop(b *testing.B) {
	for _, kind := range []string{"piece", "have"} {
		for _, k := range []int{1, 2, 4, 8, 16} {
			b.Run(fmt.Sprintf("%s/conns=%d", kind, k), func(b *testing.B) {
				benchContention(b, kind, k)
			})
		}
	}
}

func benchContention(b *testing.B, kind string, k int) {
	var cl Client
	cfg := TestingConfig(b)
	ts := &torrentStorage{}
	cfg.DefaultStorage = &torrentStorageClient{ts}
	cl.init(cfg)
	t, _ := cl.AddTorrentOpt(AddTorrentOpts{
		InfoHash:                 testingTorrentInfoHash,
		Storage:                  &torrentStorageClient{ts},
		DisableInitialPieceCheck: true,
	})
	const numPieces = 256
	qt.Assert(b, qt.IsNil(t.setInfoUnlocked(&metainfo.Info{
		Pieces:      make([]byte, 20*numPieces),
		Length:      numPieces << 20,
		PieceLength: 1 << 20,
	})))
	cl.lock()
	for i := range k {
		t.updatePiecePriority(i, "benchmark setup")
	}
	cl.unlock()
	type connEnd struct {
		cn *PeerConn
		w  net.Conn
	}
	conns := make([]connEnd, k)
	for i := range conns {
		r, w := net.Pipe()
		cn := cl.newConnection(r, newConnectionOpts{
			outgoing:   true,
			remoteAddr: netip.AddrPortFrom(netip.AddrFrom4([4]byte{1, 2, 3, byte(i + 1)}), 1234),
			network:    r.RemoteAddr().Network(),
			connString: regularNetConnPeerConnConnString(r),
		})
		cl.lock()
		cn.setTorrent(t)
		cl.unlock()
		conns[i] = connEnd{cn, w}
	}
	const chunkSize = defaultChunkSize
	piecesPerPiece := int(t.pieceRequestIndexBegin(1) - t.pieceRequestIndexBegin(0))
	// Pre-marshal messages per conn.
	msgs := make([][]byte, k)
	for i := range k {
		var buf []byte
		switch kind {
		case "piece":
			for c := range piecesPerPiece {
				buf = append(buf, pp.Message{
					Type:  pp.Piece,
					Index: pp.Integer(i),
					Piece: make([]byte, chunkSize),
					Begin: pp.Integer(chunkSize * c),
				}.MustMarshalBinary()...)
			}
		case "have":
			for p := range numPieces {
				buf = append(buf, pp.Message{Type: pp.Have, Index: pp.Integer(p)}.MustMarshalBinary()...)
			}
		}
		msgs[i] = buf
	}
	var msgsPerIter int
	switch kind {
	case "piece":
		msgsPerIter = k * piecesPerPiece
		b.SetBytes(int64(k * piecesPerPiece * chunkSize))
	case "have":
		msgsPerIter = k * numPieces
	}
	errs := make(chan error, k)
	for _, c := range conns {
		go func() {
			cl.lock()
			err := c.cn.mainReadLoop()
			cl.unlock()
			if errors.Is(err, io.EOF) {
				err = nil
			}
			errs <- err
		}()
	}
	b.ResetTimer()
	for range b.N {
		if kind == "piece" {
			cl.lock()
			for i, c := range conns {
				t.pendAllChunkSpecs(i)
				g.MakeMapIfNil(&c.cn.validReceiveChunks)
				for ri := t.pieceRequestIndexBegin(i); ri < t.pieceRequestIndexBegin(i+1); ri++ {
					c.cn.validReceiveChunks[ri] = 1
				}
			}
			cl.unlock()
			ts.allChunksWritten.Add(k * piecesPerPiece)
		}
		var wg sync.WaitGroup
		for i, c := range conns {
			wg.Go(func() {
				_, err := c.w.Write(msgs[i])
				if err != nil {
					panic(err)
				}
			})
		}
		wg.Wait()
		if kind == "piece" {
			ts.allChunksWritten.Wait()
		}
	}
	for _, c := range conns {
		c.w.Close()
	}
	for range conns {
		qt.Assert(b, qt.IsNil(<-errs))
	}
	b.StopTimer()
	b.ReportMetric(float64(b.N*msgsPerIter)/b.Elapsed().Seconds(), "msgs/s")
}
