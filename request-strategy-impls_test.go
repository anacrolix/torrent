package torrent

import (
	"context"
	"runtime"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/iter"
	"github.com/go-quicktest/qt"

	requestStrategy "github.com/anacrolix/torrent/internal/request-strategy"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	infohash_v2 "github.com/anacrolix/torrent/types/infohash-v2"
)

// Assigned to keep the result alive so the call below isn't optimised away, without itself
// allocating.
var requestStrategyResultSink bool

func TestRequestStrategyPieceDoesntAlloc(t *testing.T) {
	akshalTorrent := &Torrent{pieces: make([]pieceState, 1)}
	// Query through the interface, as the request strategy does. Piece is now an index-based call
	// returning a bool, so no per-piece value is boxed and nothing is allocated.
	var input requestStrategy.Torrent = requestStrategyTorrent{akshalTorrent}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	requestStrategyResultSink = input.PieceRequest(0)
	runtime.ReadMemStats(&after)
	qt.Assert(t, qt.Equals(before.HeapAlloc, after.HeapAlloc))
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
	tor.cl.lock()
	for i := 0; i < tor.numPieces(); i++ {
		tor.pieces[i].priority.Raise(PiecePriorityNormal)
		tor.updatePiecePriorityNoRequests(i)
	}
	tor.cl.unlock()
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
				func() {
					tor.cl.lock()
					defer tor.cl.unlock()
					tor.updatePieceCompletion(completed - 1)
				}()
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
