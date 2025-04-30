package torrent

import (
	"context"
	"io"
	"runtime"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/iter"
	"github.com/davecgh/go-spew/spew"
	qt "github.com/frankban/quicktest"

	"github.com/anacrolix/torrent/metainfo"
	request_strategy "github.com/anacrolix/torrent/request-strategy"
	"github.com/anacrolix/torrent/storage"
	infohash_v2 "github.com/anacrolix/torrent/types/infohash-v2"
)

func makeRequestStrategyPiece(t request_strategy.Torrent) request_strategy.Piece {
	return t.Piece(0)
}

func TestRequestStrategyPieceDoesntAlloc(t *testing.T) {
	c := qt.New(t)
	akshalTorrent := &Torrent{pieces: make([]Piece, 1)}
	rst := requestStrategyTorrent{akshalTorrent}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	p := makeRequestStrategyPiece(rst)
	runtime.ReadMemStats(&after)
	c.Assert(before.HeapAlloc, qt.Equals, after.HeapAlloc)
	// We have to use p, or it gets optimized away.
	spew.Fdump(io.Discard, p)
}

type storagePiece struct {
	complete bool
}

func (s storagePiece) ReadAt(p []byte, off int64) (n int, err error) {
	//TODO implement me
	panic("implement me")
}

func (s storagePiece) WriteAt(p []byte, off int64) (n int, err error) {
	//TODO implement me
	panic("implement me")
}

func (s storagePiece) MarkComplete() error {
	//TODO implement me
	panic("implement me")
}

func (s storagePiece) MarkNotComplete() error {
	//TODO implement me
	panic("implement me")
}

func (s storagePiece) Completion() storage.Completion {
	return storage.Completion{Ok: true, Complete: s.complete}
}

var _ storage.PieceImpl = storagePiece{}

type storageClient struct {
	completed int
}

func (s *storageClient) OpenTorrent(
	_ context.Context,
	info *metainfo.Info,
	infoHash metainfo.Hash,
) (storage.TorrentImpl, error) {
	return storage.TorrentImpl{
		Piece: func(p metainfo.Piece) storage.PieceImpl {
			return storagePiece{complete: p.Index() < s.completed}
		},
	}, nil
}

func BenchmarkRequestStrategy(b *testing.B) {
	c := qt.New(b)
	cl := newTestingClient(b)
	storageClient := storageClient{}
	tor, new := cl.AddTorrentOpt(AddTorrentOpts{
		InfoHashV2: g.Some(infohash_v2.FromHexString("deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")),
		Storage:    &storageClient,
	})
	tor.disableTriggers = true
	c.Assert(new, qt.IsTrue)
	const pieceLength = 1 << 8 << 10
	const numPieces = 30_000
	err := tor.setInfo(&metainfo.Info{
		Pieces:      make([]byte, numPieces*metainfo.HashSize),
		PieceLength: pieceLength,
		Length:      pieceLength * numPieces,
	})
	c.Assert(err, qt.IsNil)
	tor.onSetInfo()
	peer := cl.newConnection(nil, newConnectionOpts{
		network: "test",
	})
	peer.setTorrent(tor)
	c.Assert(tor.storage, qt.IsNotNil)
	const chunkSize = defaultChunkSize
	peer.onPeerHasAllPiecesNoTriggers()
	for i := 0; i < tor.numPieces(); i++ {
		tor.pieces[i].priority.Raise(PiecePriorityNormal)
		tor.updatePiecePriorityNoRequests(i)
	}
	peer.peerChoking = false
	//b.StopTimer()
	b.ResetTimer()
	//b.ReportAllocs()
	for _ = range iter.N(b.N) {
		storageClient.completed = 0
		for pieceIndex := range iter.N(numPieces) {
			tor.updatePieceCompletion(pieceIndex)
		}
		for completed := 0; completed <= numPieces; completed += 1 {
			storageClient.completed = completed
			if completed > 0 {
				tor.updatePieceCompletion(completed - 1)
			}
			// Starting and stopping timers around this part causes lots of GC overhead.
			rs := peer.getDesiredRequestState()
			tor.cacheNextRequestIndexesForReuse(rs.Requests.requestIndexes)
			// End of part that should be timed.
			remainingChunks := (numPieces - completed) * (pieceLength / chunkSize)
			c.Assert(rs.Requests.requestIndexes, qt.HasLen, minInt(
				remainingChunks,
				int(cl.config.MaxUnverifiedBytes/chunkSize)))
		}
	}
}
