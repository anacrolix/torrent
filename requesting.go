package torrent

import (
	"container/heap"
	"context"
	"encoding/gob"
	"math/rand"
	"reflect"
	"runtime/pprof"
	"time"
	"unsafe"

	"github.com/anacrolix/log"
	"github.com/anacrolix/multiless"

	request_strategy "github.com/anacrolix/torrent/request-strategy"
)

func (t *Torrent) requestStrategyPieceOrderState(i int) request_strategy.PieceRequestOrderState {
	return request_strategy.PieceRequestOrderState{
		Priority:     t.piece(i).purePriority(),
		Partial:      t.piecePartiallyDownloaded(i),
		Availability: t.piece(i).availability,
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

type (
	RequestIndex   = request_strategy.RequestIndex
	chunkIndexType = request_strategy.ChunkIndex
)

type peerRequests struct {
	requestIndexes []RequestIndex
	peer           *Peer
}

func (p *peerRequests) Len() int {
	return len(p.requestIndexes)
}

func (p *peerRequests) Less(i, j int) bool {
	leftRequest := p.requestIndexes[i]
	rightRequest := p.requestIndexes[j]
	t := p.peer.t
	leftPieceIndex := leftRequest / t.chunksPerRegularPiece()
	rightPieceIndex := rightRequest / t.chunksPerRegularPiece()
	leftCurrent := p.peer.actualRequestState.Requests.Contains(leftRequest)
	rightCurrent := p.peer.actualRequestState.Requests.Contains(rightRequest)
	pending := func(index RequestIndex, current bool) int {
		ret := t.pendingRequests.Get(index)
		if current {
			ret--
		}
		// See https://github.com/anacrolix/torrent/issues/679 for possible issues. This should be
		// resolved.
		if ret < 0 {
			panic(ret)
		}
		return ret
	}
	ml := multiless.New()
	// Push requests that can't be served right now to the end. But we don't throw them away unless
	// there's a better alternative. This is for when we're using the fast extension and get choked
	// but our requests could still be good when we get unchoked.
	if p.peer.peerChoking {
		ml = ml.Bool(
			!p.peer.peerAllowedFast.Contains(leftPieceIndex),
			!p.peer.peerAllowedFast.Contains(rightPieceIndex),
		)
	}
	ml = ml.Int(
		pending(leftRequest, leftCurrent),
		pending(rightRequest, rightCurrent))
	ml = ml.Bool(!leftCurrent, !rightCurrent)
	leftPiece := t.piece(int(leftPieceIndex))
	rightPiece := t.piece(int(rightPieceIndex))
	ml = ml.Int(
		-int(leftPiece.purePriority()),
		-int(rightPiece.purePriority()),
	)
	ml = ml.Int(
		int(leftPiece.availability),
		int(rightPiece.availability))
	leftLastRequested := p.peer.t.lastRequested[leftRequest]
	rightLastRequested := p.peer.t.lastRequested[rightRequest]
	ml = ml.EagerSameLess(
		leftLastRequested.Equal(rightLastRequested),
		leftLastRequested.Before(rightLastRequested),
	)
	ml = ml.Uint32(leftPieceIndex, rightPieceIndex)
	ml = ml.Uint32(leftRequest, rightRequest)
	return ml.MustLess()
}

func (p *peerRequests) Swap(i, j int) {
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

type desiredRequestState struct {
	Requests   []RequestIndex
	Interested bool
}

func (p *Peer) getDesiredRequestState() (desired desiredRequestState) {
	input := p.t.getRequestStrategyInput()
	requestHeap := peerRequests{
		peer: p,
	}
	request_strategy.GetRequestablePieces(
		input,
		p.t.cl.pieceRequestOrder[p.t.storage.Capacity],
		func(ih InfoHash, pieceIndex int) {
			if ih != p.t.infoHash {
				return
			}
			if !p.peerHasPiece(pieceIndex) {
				return
			}
			allowedFast := p.peerAllowedFast.ContainsInt(pieceIndex)
			p.t.piece(pieceIndex).undirtiedChunksIter.Iter(func(ci request_strategy.ChunkIndex) {
				r := p.t.pieceRequestIndexOffset(pieceIndex) + ci
				// if p.t.pendingRequests.Get(r) != 0 && !p.actualRequestState.Requests.Contains(r) {
				//	return
				// }
				if !allowedFast {
					// We must signal interest to request this
					desired.Interested = true
					// We can make or will allow sustaining a request here if we're not choked, or
					// have made the request previously (presumably while unchoked), and haven't had
					// the peer respond yet (and the request was retained because we are using the
					// fast extension).
					if p.peerChoking && !p.actualRequestState.Requests.Contains(r) {
						// We can't request this right now.
						return
					}
				}
				// Note that we can still be interested if we filter all requests due to being
				// recently requested from another peer.
				if !p.actualRequestState.Requests.Contains(r) {
					if time.Since(p.t.lastRequested[r]) < time.Second {
						return
					}
				}
				requestHeap.requestIndexes = append(requestHeap.requestIndexes, r)
			})
		},
	)
	p.t.assertPendingRequests()
	heap.Init(&requestHeap)
	for requestHeap.Len() != 0 && len(desired.Requests) < p.nominalMaxRequests() {
		requestIndex := heap.Pop(&requestHeap).(RequestIndex)
		desired.Requests = append(desired.Requests, requestIndex)
	}
	return
}

func (p *Peer) maybeUpdateActualRequestState() bool {
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

// Transmit/action the request state to the peer.
func (p *Peer) applyRequestState(next desiredRequestState) bool {
	current := &p.actualRequestState
	if !p.setInterested(next.Interested) {
		return false
	}
	more := true
	cancel := current.Requests.Clone()
	for _, ri := range next.Requests {
		cancel.Remove(ri)
	}
	cancel.Iterate(func(req uint32) bool {
		more = p.cancel(req)
		return more
	})
	if !more {
		return false
	}
	shuffled := false
	lastPending := 0
	for i := 0; i < len(next.Requests); i++ {
		req := next.Requests[i]
		if p.cancelledRequests.Contains(req) {
			// Waiting for a reject or piece message, which will suitably trigger us to update our
			// requests, so we can skip this one with no additional consideration.
			continue
		}
		// The cardinality of our desired requests shouldn't exceed the max requests since it's used
		// in the calculation of the requests. However, if we cancelled requests and they haven't
		// been rejected or serviced yet with the fast extension enabled, we can end up with more
		// extra outstanding requests. We could subtract the number of outstanding cancels from the
		// next request cardinality, but peers might not like that.
		if maxRequests(current.Requests.GetCardinality()) >= p.nominalMaxRequests() {
			// log.Printf("not assigning all requests [desired=%v, cancelled=%v, current=%v, max=%v]",
			//	next.Requests.GetCardinality(),
			//	p.cancelledRequests.GetCardinality(),
			//	current.Requests.GetCardinality(),
			//	p.nominalMaxRequests(),
			// )
			break
		}
		otherPending := p.t.pendingRequests.Get(next.Requests[0])
		if p.actualRequestState.Requests.Contains(next.Requests[0]) {
			otherPending--
		}
		if otherPending < lastPending {
			// Pending should only rise. It's supposed to be the strongest ordering criteria. If it
			// doesn't, our shuffling condition could be wrong.
			panic(lastPending)
		}
		// If the request has already been requested by another peer, shuffle this and the rest of
		// the requests (since according to the increasing condition, the rest of the indices
		// already have an outstanding request with another peer).
		if !shuffled && otherPending > 0 {
			shuffleReqs := next.Requests[i:]
			rand.Shuffle(len(shuffleReqs), func(i, j int) {
				shuffleReqs[i], shuffleReqs[j] = shuffleReqs[j], shuffleReqs[i]
			})
			// log.Printf("shuffled reqs [%v:%v]", i, len(next.Requests))
			shuffled = true
			// Repeat this index
			i--
			continue
		}

		more = p.mustRequest(req)
		if !more {
			break
		}
	}
	// TODO: This may need to change, we might want to update even if there were no requests due to
	// filtering them for being recently requested already.
	p.updateRequestsTimer.Stop()
	if more {
		p.needRequestUpdate = ""
		if current.Interested {
			p.updateRequestsTimer.Reset(3 * time.Second)
		}
	}
	return more
}
