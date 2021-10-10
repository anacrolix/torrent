package torrent

import (
	"container/heap"
	"context"
	"encoding/gob"
	"reflect"
	"runtime/pprof"
	"time"
	"unsafe"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2/bitmap"
	"github.com/anacrolix/multiless"

	request_strategy "github.com/anacrolix/torrent/request-strategy"
)

func (cl *Client) tickleRequester() {
	cl.updateRequests.Broadcast()
}

func (cl *Client) getRequestStrategyInput() request_strategy.Input {
	ts := make([]request_strategy.Torrent, 0, len(cl.torrents))
	for _, t := range cl.torrents {
		if !t.haveInfo() {
			// This would be removed if metadata is handled here. We have to guard against not
			// knowing the piece size. If we have no info, we have no pieces too, so the end result
			// is the same.
			continue
		}
		rst := request_strategy.Torrent{
			InfoHash:       t.infoHash,
			ChunksPerPiece: t.chunksPerRegularPiece(),
		}
		if t.storage != nil {
			rst.Capacity = t.storage.Capacity
		}
		rst.Pieces = make([]request_strategy.Piece, 0, len(t.pieces))
		for i := range t.pieces {
			p := &t.pieces[i]
			rst.Pieces = append(rst.Pieces, request_strategy.Piece{
				Request:           !t.ignorePieceForRequests(i),
				Priority:          p.purePriority(),
				Partial:           t.piecePartiallyDownloaded(i),
				Availability:      p.availability,
				Length:            int64(p.length()),
				NumPendingChunks:  int(t.pieceNumPendingChunks(i)),
				IterPendingChunks: p.undirtiedChunksIter(),
			})
		}
		t.iterPeers(func(p *Peer) {
			if p.closed.IsSet() {
				return
			}
			if p.piecesReceivedSinceLastRequestUpdate > p.maxPiecesReceivedBetweenRequestUpdates {
				p.maxPiecesReceivedBetweenRequestUpdates = p.piecesReceivedSinceLastRequestUpdate
			}
			p.piecesReceivedSinceLastRequestUpdate = 0
			rst.Peers = append(rst.Peers, request_strategy.Peer{
				Pieces:           *p.newPeerPieces(),
				MaxRequests:      p.nominalMaxRequests(),
				ExistingRequests: p.actualRequestState.Requests,
				Choking:          p.peerChoking,
				PieceAllowedFast: p.peerAllowedFast,
				DownloadRate:     p.downloadRate(),
				Age:              time.Since(p.completedHandshake),
				Id: peerId{
					Peer: p,
					ptr:  uintptr(unsafe.Pointer(p)),
				},
			})
		})
		ts = append(ts, rst)
	}
	return request_strategy.Input{
		Torrents:           ts,
		MaxUnverifiedBytes: cl.config.MaxUnverifiedBytes,
	}
}

func init() {
	gob.Register(peerId{})
}

type peerId struct {
	*Peer
	ptr uintptr
}

func (p peerId) Uintptr() uintptr {
	return p.ptr
}

func (p peerId) GobEncode() (b []byte, _ error) {
	*(*reflect.SliceHeader)(unsafe.Pointer(&b)) = reflect.SliceHeader{
		Data: uintptr(unsafe.Pointer(&p.ptr)),
		Len:  int(unsafe.Sizeof(p.ptr)),
		Cap:  int(unsafe.Sizeof(p.ptr)),
	}
	return
}

func (p *peerId) GobDecode(b []byte) error {
	if uintptr(len(b)) != unsafe.Sizeof(p.ptr) {
		panic(len(b))
	}
	ptr := unsafe.Pointer(&b[0])
	p.ptr = *(*uintptr)(ptr)
	log.Printf("%p", ptr)
	dst := reflect.SliceHeader{
		Data: uintptr(unsafe.Pointer(&p.Peer)),
		Len:  int(unsafe.Sizeof(p.Peer)),
		Cap:  int(unsafe.Sizeof(p.Peer)),
	}
	copy(*(*[]byte)(unsafe.Pointer(&dst)), b)
	return nil
}

type RequestIndex = request_strategy.RequestIndex
type chunkIndexType = request_strategy.ChunkIndex

type peerRequests struct {
	requestIndexes       []RequestIndex
	peer                 *Peer
	torrentStrategyInput request_strategy.Torrent
}

func (p peerRequests) Len() int {
	return len(p.requestIndexes)
}

func (p peerRequests) Less(i, j int) bool {
	leftRequest := p.requestIndexes[i]
	rightRequest := p.requestIndexes[j]
	t := p.peer.t
	leftPieceIndex := leftRequest / p.torrentStrategyInput.ChunksPerPiece
	rightPieceIndex := rightRequest / p.torrentStrategyInput.ChunksPerPiece
	leftCurrent := p.peer.actualRequestState.Requests.Contains(leftRequest)
	rightCurrent := p.peer.actualRequestState.Requests.Contains(rightRequest)
	pending := func(index RequestIndex, current bool) int {
		ret := t.pendingRequests.Get(index)
		if current {
			ret--
		}
		return ret
	}
	ml := multiless.New()
	ml = ml.Int(
		pending(leftRequest, leftCurrent),
		pending(rightRequest, rightCurrent))
	ml = ml.Bool(rightCurrent, leftCurrent)
	ml = ml.Int(
		int(p.torrentStrategyInput.Pieces[leftPieceIndex].Priority),
		int(p.torrentStrategyInput.Pieces[rightPieceIndex].Priority))
	ml = ml.Int(
		int(p.torrentStrategyInput.Pieces[leftPieceIndex].Availability),
		int(p.torrentStrategyInput.Pieces[rightPieceIndex].Availability))
	ml = ml.Uint32(leftPieceIndex, rightPieceIndex)
	ml = ml.Uint32(leftRequest, rightRequest)
	return ml.MustLess()
}

func (p peerRequests) Swap(i, j int) {
	p.requestIndexes[i], p.requestIndexes[j] = p.requestIndexes[j], p.requestIndexes[i]
}

func (p *peerRequests) Push(x interface{}) {
	p.requestIndexes = append(p.requestIndexes, x.(RequestIndex))
}

func (p *peerRequests) Pop() interface{} {
	last := len(p.requestIndexes) - 1
	x := p.requestIndexes[last]
	p.requestIndexes = p.requestIndexes[:last]
	return x
}

func (p *Peer) getDesiredRequestState() (desired requestState) {
	input := p.t.cl.getRequestStrategyInput()
	requestHeap := peerRequests{
		requestIndexes: nil,
		peer:           p,
	}
	for _, t := range input.Torrents {
		if t.InfoHash == p.t.infoHash {
			requestHeap.torrentStrategyInput = t
			break
		}
	}
	request_strategy.GetRequestablePieces(
		input,
		func(t *request_strategy.Torrent, rsp *request_strategy.Piece, pieceIndex int) {
			if t.InfoHash != p.t.infoHash {
				return
			}
			if !p.peerHasPiece(pieceIndex) {
				return
			}
			rsp.IterPendingChunks.Iter(func(ci request_strategy.ChunkIndex) {
				requestHeap.requestIndexes = append(
					requestHeap.requestIndexes,
					p.t.pieceRequestIndexOffset(pieceIndex)+ci)
			})
		},
	)
	heap.Init(&requestHeap)
	for requestHeap.Len() != 0 && desired.Requests.GetCardinality() < uint64(p.nominalMaxRequests()) {
		requestIndex := heap.Pop(&requestHeap).(RequestIndex)
		pieceIndex := requestIndex / p.t.chunksPerRegularPiece()
		allowedFast := p.peerAllowedFast.Contains(pieceIndex)
		if !allowedFast {
			desired.Interested = true
		}
		if allowedFast || !p.peerChoking {
			desired.Requests.Add(requestIndex)
		}
	}
	return
}

func (p *Peer) applyNextRequestState() bool {
	if p.needRequestUpdate == "" {
		return true
	}
	var more bool
	pprof.Do(
		context.Background(),
		pprof.Labels("update request", p.needRequestUpdate),
		func(_ context.Context) {
			next := p.getDesiredRequestState()
			more = p.applyRequestState(next)
		},
	)
	return more
}

func (p *Peer) applyRequestState(next requestState) bool {
	current := p.actualRequestState
	if !p.setInterested(next.Interested) {
		return false
	}
	more := true
	cancel := roaring.AndNot(&current.Requests, &next.Requests)
	cancel.Iterate(func(req uint32) bool {
		more = p.cancel(req)
		return more
	})
	if !more {
		return false
	}
	next.Requests.Iterate(func(req uint32) bool {
		// This could happen if the peer chokes us between the next state being generated, and us
		// trying to transmit the state.
		if p.peerChoking && !p.peerAllowedFast.Contains(bitmap.BitIndex(req/p.t.chunksPerRegularPiece())) {
			return true
		}
		var err error
		more, err = p.request(req)
		if err != nil {
			panic(err)
		} /* else {
			log.Print(req)
		} */
		return more
	})
	if more {
		p.needRequestUpdate = ""
	}
	return more
}
