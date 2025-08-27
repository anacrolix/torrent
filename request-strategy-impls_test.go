package torrent

import (
	"context"
	"io"
	"runtime"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/iter"
	"github.com/davecgh/go-spew/spew"
	"github.com/go-quicktest/qt"

	requestStrategy "github.com/anacrolix/torrent/internal/request-strategy"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	infohash_v2 "github.com/anacrolix/torrent/types/infohash-v2"
)

func makeRequestStrategyPiece(t requestStrategy.Torrent) requestStrategy.Piece {
	return t.Piece(0)
}

func TestRequestStrategyPieceDoesntAlloc(t *testing.T) {
	akshalTorrent := &Torrent{pieces: make([]Piece, 1)}
	rst := requestStrategyTorrent{akshalTorrent}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	p := makeRequestStrategyPiece(rst)
	runtime.ReadMemStats(&after)
	qt.Assert(t, qt.Equals(before.HeapAlloc, after.HeapAlloc))
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
	cl := newTestingClient(b)
	storageClient := storageClient{}
	tor, new := cl.AddTorrentOpt(AddTorrentOpts{
		InfoHash:   testingTorrentInfoHash,
		InfoHashV2: g.Some(infohash_v2.FromHexString("deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")),
		Storage:    &storageClient,
	})
	tor.disableTriggers = true
	qt.Assert(b, qt.IsTrue(new))
	const pieceLength = 1 << 8 << 10
	const numPieces = 10_000
	err := tor.setInfoUnlocked(&metainfo.Info{
		Pieces:      make([]byte, numPieces*metainfo.HashSize),
		PieceLength: pieceLength,
		Length:      pieceLength * numPieces,
	})
	qt.Assert(b, qt.IsNil(err))
	peer := cl.newConnection(nil, newConnectionOpts{
		network: "test",
	})
	peer.setTorrent(tor)
	qt.Assert(b, qt.IsNotNil(tor.storage))
	const chunkSize = defaultChunkSize
	peer.onPeerHasAllPiecesNoTriggers()
	for i := 0; i < tor.numPieces(); i++ {
		tor.pieces[i].priority.Raise(PiecePriorityNormal)
		tor.updatePiecePriorityNoRequests(i)
	}
	peer.peerChoking = false
	for b.Loop() {
		storageClient.completed = 0
		for pieceIndex := range iter.N(numPieces) {
			tor.cl.lock()
			tor.updatePieceCompletion(pieceIndex)
			tor.cl.unlock()
		}
		for completed := 0; completed <= numPieces; completed += 1 {
			storageClient.completed = completed
			if completed > 0 {
				tor.cl.lock()
				tor.updatePieceCompletion(completed - 1)
				tor.cl.unlock()
			}
			// Starting and stopping timers around this part causes lots of GC overhead.
			rs := peer.getDesiredRequestState()
			tor.cacheNextRequestIndexesForReuse(rs.Requests.requestIndexes)
			// End of part that should be timed.
			remainingChunks := (numPieces - completed) * (pieceLength / chunkSize)
			qt.Assert(b, qt.HasLen(rs.Requests.requestIndexes, min(
				remainingChunks,
				int(cl.config.MaxUnverifiedBytes/chunkSize))))
		}
	}
}
