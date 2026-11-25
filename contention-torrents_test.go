package torrent

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
)

// One conn on each of several Torrents, each running mainReadLoop concurrently. With "one-client"
// every Torrent shares a Client, and so its lock. With "client-per-torrent" each Torrent has its
// own Client, which is the most that giving each Torrent its own lock or event loop could gain.
// "shared-capacity" is one Client whose Torrents share an (uncapped) storage capacity, and so a
// request order and a lock domain.
func BenchmarkContentionTorrents(b *testing.B) {
	for _, kind := range []string{"piece", "have"} {
		for _, layout := range []string{"one-client", "client-per-torrent", "shared-capacity"} {
			for _, n := range []int{1, 2, 4, 8} {
				b.Run(fmt.Sprintf("%s/%s/torrents=%d", kind, layout, n), func(b *testing.B) {
					benchContentionTorrents(b, kind, layout != "client-per-torrent", layout == "shared-capacity", n)
				})
			}
		}
	}
}

func benchContentionTorrents(b *testing.B, kind string, shareClient, shareCapacity bool, numTorrents int) {
	const (
		numPieces = 256
		chunkSize = defaultChunkSize
	)
	type benchTorrent struct {
		cl   *Client
		t    *Torrent
		ts   *torrentStorage
		cn   *PeerConn
		w    net.Conn
		msgs []byte
	}
	isPiece := kind == "piece"
	newClient := func() *Client {
		cl := new(Client)
		cl.init(TestingConfig(b))
		return cl
	}
	var sharedClient *Client
	if shareClient {
		sharedClient = newClient()
	}
	torrents := make([]*benchTorrent, numTorrents)
	var capacity storage.TorrentCapacity
	if shareCapacity {
		f := func() (int64, bool) { return 0, false }
		capacity = &f
	}
	var chunksPerPiece int
	for i := range torrents {
		bt := &benchTorrent{cl: sharedClient, ts: &torrentStorage{}}
		if bt.cl == nil {
			bt.cl = newClient()
		}
		cl := bt.cl
		ih := testingTorrentInfoHash
		ih[19] = byte(i + 1)
		bt.t, _ = cl.AddTorrentOpt(AddTorrentOpts{
			InfoHash:                 ih,
			Storage:                  &torrentStorageClient{ts: bt.ts, capacity: capacity},
			DisableInitialPieceCheck: true,
		})
		qt.Assert(b, qt.IsNil(bt.t.setInfoUnlocked(&metainfo.Info{
			Pieces:      make([]byte, 20*numPieces),
			Length:      numPieces << 20,
			PieceLength: 1 << 20,
		})))
		r, w := net.Pipe()
		bt.w = w
		bt.cn = cl.newConnection(r, newConnectionOpts{
			outgoing:   true,
			remoteAddr: netip.AddrPortFrom(netip.AddrFrom4([4]byte{1, 2, 3, byte(i + 1)}), 1234),
			network:    r.RemoteAddr().Network(),
			connString: regularNetConnPeerConnConnString(r),
		})
		cl.lock()
		bt.t.updatePiecePriority(0, "benchmark setup")
		bt.cn.setTorrent(bt.t)
		cl.unlock()
		chunksPerPiece = int(bt.t.pieceRequestIndexBegin(1) - bt.t.pieceRequestIndexBegin(0))
		if isPiece {
			for c := range chunksPerPiece {
				bt.msgs = append(bt.msgs, pp.Message{
					Type:  pp.Piece,
					Piece: make([]byte, chunkSize),
					Begin: pp.Integer(chunkSize * c),
				}.MustMarshalBinary()...)
			}
		} else {
			for p := range numPieces {
				bt.msgs = append(bt.msgs, pp.Message{Type: pp.Have, Index: pp.Integer(p)}.MustMarshalBinary()...)
			}
		}
		torrents[i] = bt
	}
	msgsPerIter := numTorrents * numPieces
	if isPiece {
		msgsPerIter = numTorrents * chunksPerPiece
		b.SetBytes(int64(msgsPerIter * chunkSize))
	}
	errs := make(chan error, numTorrents)
	for _, bt := range torrents {
		go func() {
			bt.t.lockDomain()
			err := bt.cn.mainReadLoop()
			bt.t.unlockDomain()
			if errors.Is(err, io.EOF) {
				err = nil
			}
			errs <- err
		}()
	}
	b.ResetTimer()
	for range b.N {
		var wg sync.WaitGroup
		for _, bt := range torrents {
			if isPiece {
				bt.t.lockDomain()
				bt.t.pendAllChunkSpecs(0)
				for ri := bt.t.pieceRequestIndexBegin(0); ri < bt.t.pieceRequestIndexBegin(1); ri++ {
					bt.cn.wireState.ExpectChunk(bt.t.requestIndexToRequest(ri))
				}
				bt.t.unlockDomain()
				bt.ts.allChunksWritten.Add(chunksPerPiece)
			}
			wg.Go(func() {
				_, err := bt.w.Write(bt.msgs)
				if err != nil {
					panic(err)
				}
				if isPiece {
					bt.ts.allChunksWritten.Wait()
				}
			})
		}
		wg.Wait()
	}
	b.StopTimer()
	for _, bt := range torrents {
		bt.w.Close()
	}
	for range torrents {
		qt.Assert(b, qt.IsNil(<-errs))
	}
	b.ReportMetric(float64(b.N*msgsPerIter)/b.Elapsed().Seconds(), "msgs/s")
}
