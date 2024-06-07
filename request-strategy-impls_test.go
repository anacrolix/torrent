package torrent

import (
	"io"
	"runtime"
	"testing"

	"github.com/anacrolix/missinggo/v2/iter"
	"github.com/davecgh/go-spew/spew"
	qt "github.com/frankban/quicktest"

	"github.com/anacrolix/torrent/metainfo"
	request_strategy "github.com/anacrolix/torrent/request-strategy"
	"github.com/anacrolix/torrent/storage"
)

func makeRequestStrategyPiece(t request_strategy.Torrent) request_strategy.Piece {
	return t.Piece(0, true)
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
		Storage: &storageClient,
	})
	tor.disableTriggers = true
	c.Assert(new, qt.IsTrue)
	const pieceLength = 1 << 8 << 10
	const numPieces = 30_000
	err := tor.setInfo(&metainfo.Info{
		Pieces:      make([]byte, numPieces*metainfo.HashSize),
		PieceLength: pieceLength,
		Length:      pieceLength * numPieces,
	}, true)
	c.Assert(err, qt.IsNil)
	tor.onSetInfo(true)
	peer := cl.newConnection(nil, newConnectionOpts{
		network: "test",
	})
	peer.setTorrent(tor)
	c.Assert(tor.storage, qt.IsNotNil)
	const chunkSize = defaultChunkSize
	peer.onPeerHasAllPiecesNoTriggers()
	for i := 0; i < tor.numPieces(); i++ {
		tor.pieces[i].priority.Raise(PiecePriorityNormal)
		tor.updatePiecePriorityNoTriggers(i, true)
	}
	peer.peerChoking = false
	//b.StopTimer()
	b.ResetTimer()
	//b.ReportAllocs()
	for _ = range iter.N(b.N) {
		storageClient.completed = 0
		for pieceIndex := range iter.N(numPieces) {
			tor.updatePieceCompletion(pieceIndex, true)
		}
		for completed := 0; completed <= numPieces; completed += 1 {
			storageClient.completed = completed
			if completed > 0 {
				tor.updatePieceCompletion(completed-1, true)
			}
			// Starting and stopping timers around this part causes lots of GC overhead.
			rs := peer.getDesiredRequestState(false, true, true)
			tor.cacheNextRequestIndexesForReuse(rs.Requests.requestIndexes, true)
			// End of part that should be timed.
			remainingChunks := (numPieces - completed) * (pieceLength / chunkSize)
			c.Assert(rs.Requests.requestIndexes, qt.HasLen, minInt(
				remainingChunks,
				int(cl.config.MaxUnverifiedBytes/chunkSize)))
		}
	}
}
