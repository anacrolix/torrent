package torrent

import (
	"encoding/gob"
	"reflect"
	"time"
	"unsafe"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2/bitmap"

	"github.com/anacrolix/chansync"
	request_strategy "github.com/anacrolix/torrent/request-strategy"
)

// Calculate requests individually for each peer.
const peerRequesting = false

func (cl *Client) requester() {
	for {
		update := func() chansync.Signaled {
			cl.lock()
			defer cl.unlock()
			cl.doRequests()
			return cl.updateRequests.Signaled()
		}()
		minWait := time.After(100 * time.Millisecond)
		maxWait := time.After(1000 * time.Millisecond)
		select {
		case <-cl.closed.Done():
			return
		case <-minWait:
		case <-maxWait:
		}
		select {
		case <-cl.closed.Done():
			return
		case <-update:
		case <-maxWait:
		}
	}
}

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
			ChunksPerPiece: (t.usualPieceSize() + int(t.chunkSize) - 1) / int(t.chunkSize),
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

func (cl *Client) doRequests() {
	input := cl.getRequestStrategyInput()
	nextPeerStates := request_strategy.Run(input)
	for p, state := range nextPeerStates {
		setPeerNextRequestState(p, state)
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

func setPeerNextRequestState(_p request_strategy.PeerId, rp request_strategy.PeerNextRequestState) {
	p := _p.(peerId).Peer
	p.nextRequestState = rp
	p.onNextRequestStateChanged()
}

type RequestIndex = request_strategy.RequestIndex
type chunkIndexType = request_strategy.ChunkIndex

func (p *Peer) applyNextRequestState() bool {
	if peerRequesting {
		if p.actualRequestState.Requests.GetCardinality() > uint64(p.nominalMaxRequests()/2) {
			return true
		}
		type piece struct {
			index   int
			endGame bool
		}
		var pieceOrder []piece
		request_strategy.GetRequestablePieces(
			p.t.cl.getRequestStrategyInput(),
			func(t *request_strategy.Torrent, rsp *request_strategy.Piece, pieceIndex int) {
				if t.InfoHash != p.t.infoHash {
					return
				}
				if !p.peerHasPiece(pieceIndex) {
					return
				}
				pieceOrder = append(pieceOrder, piece{
					index:   pieceIndex,
					endGame: rsp.Priority == PiecePriorityNow,
				})
			},
		)
		more := true
		interested := false
		for _, endGameIter := range []bool{false, true} {
			for _, piece := range pieceOrder {
				tp := p.t.piece(piece.index)
				tp.iterUndirtiedChunks(func(cs chunkIndexType) {
					req := cs + tp.requestIndexOffset()
					if !piece.endGame && !endGameIter && p.t.pendingRequests[req] > 0 {
						return
					}
					interested = true
					more = p.setInterested(true)
					if !more {
						return
					}
					if maxRequests(p.actualRequestState.Requests.GetCardinality()) >= p.nominalMaxRequests() {
						return
					}
					if p.peerChoking && !p.peerAllowedFast.Contains(bitmap.BitIndex(piece.index)) {
						return
					}
					var err error
					more, err = p.request(req)
					if err != nil {
						panic(err)
					}
				})
				if interested && maxRequests(p.actualRequestState.Requests.GetCardinality()) >= p.nominalMaxRequests() {
					break
				}
				if !more {
					break
				}
			}
			if !more {
				break
			}
		}
		if !more {
			return false
		}
		if !interested {
			p.setInterested(false)
		}
		return more
	}

	next := p.nextRequestState
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
	return more
}
