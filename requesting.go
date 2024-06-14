package torrent

import (
	"context"
	"encoding/gob"
	"fmt"
	"reflect"
	"runtime/pprof"
	"time"
	"unsafe"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/generics/heap"
	"github.com/anacrolix/log"
	"github.com/anacrolix/multiless"

	requestStrategy "github.com/anacrolix/torrent/request-strategy"
	typedRoaring "github.com/anacrolix/torrent/typed-roaring"
)

type (
	// Since we have to store all the requests in memory, we can't reasonably exceed what could be
	// indexed with the memory space available.
	maxRequests = int
)

func (t *Torrent) requestStrategyPieceOrderState(i int, lock bool) requestStrategy.PieceRequestOrderState {
	p := t.piece(i, lock)
	return requestStrategy.PieceRequestOrderState{
		Priority:     p.purePriority(lock),
		Partial:      t.piecePartiallyDownloaded(i, lock),
		Availability: p.availability(lock),
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
	RequestIndex   = requestStrategy.RequestIndex
	chunkIndexType = requestStrategy.ChunkIndex
)

type desiredPeerRequests struct {
	requestIndexes []RequestIndex
	peer           *Peer
	pieceStates    []requestStrategy.PieceRequestOrderState
}

func (p *desiredPeerRequests) lessByValue(leftRequest, rightRequest RequestIndex) bool {
	t := p.peer.t
	leftPieceIndex := t.pieceIndexOfRequestIndex(leftRequest, false)
	rightPieceIndex := t.pieceIndexOfRequestIndex(rightRequest, false)
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
	leftPiece := &p.pieceStates[leftPieceIndex]
	rightPiece := &p.pieceStates[rightPieceIndex]
	// Putting this first means we can steal requests from lesser-performing peers for our first few
	// new requests.
	priority := func() piecePriority {
		// Technically we would be happy with the cached priority here, except we don't actually
		// cache it anymore, and Torrent.piecePriority just does another lookup of *Piece to resolve
		// the priority through Piece.purePriority, which is probably slower.
		leftPriority := leftPiece.Priority
		rightPriority := rightPiece.Priority
		ml = ml.Int(
			-int(leftPriority),
			-int(rightPriority),
		)
		if !ml.Ok() {
			if leftPriority != rightPriority {
				panic("expected equal")
			}
		}
		return leftPriority
	}()
	if ml.Ok() {
		return ml.MustLess()
	}
	leftRequestState := t.requestState[leftRequest]
	rightRequestState := t.requestState[rightRequest]
	leftPeer := leftRequestState.peer
	rightPeer := rightRequestState.peer
	// Prefer chunks already requested from this peer.
	ml = ml.Bool(rightPeer == p.peer, leftPeer == p.peer)
	// Prefer unrequested chunks.
	ml = ml.Bool(rightPeer == nil, leftPeer == nil)
	if ml.Ok() {
		return ml.MustLess()
	}
	if leftPeer != nil {
		// The right peer should also be set, or we'd have resolved the computation by now.
		ml = ml.Uint64(
			rightPeer.requestState.Requests.GetCardinality(),
			leftPeer.requestState.Requests.GetCardinality(),
		)
		// Could either of the lastRequested be Zero? That's what checking an existing peer is for.
		leftLast := leftRequestState.when
		rightLast := rightRequestState.when
		if leftLast.IsZero() || rightLast.IsZero() {
			panic("expected non-zero last requested times")
		}
		// We want the most-recently requested on the left. Clients like Transmission serve requests
		// in received order, so the most recently-requested is the one that has the longest until
		// it will be served and therefore is the best candidate to cancel.
		ml = ml.CmpInt64(rightLast.Sub(leftLast).Nanoseconds())
	}
	ml = ml.Int(
		leftPiece.Availability,
		rightPiece.Availability)
	if priority == PiecePriorityReadahead {
		// TODO: For readahead in particular, it would be even better to consider distance from the
		// reader position so that reads earlier in a torrent don't starve reads later in the
		// torrent. This would probably require reconsideration of how readahead priority works.
		ml = ml.Int(leftPieceIndex, rightPieceIndex)
	} else {
		ml = ml.Int(t.pieceRequestOrder[leftPieceIndex], t.pieceRequestOrder[rightPieceIndex])
	}
	return ml.Less()
}

type desiredRequestState struct {
	Requests   desiredPeerRequests
	Interested bool
}

func (p *Peer) getDesiredRequestState(debug bool, lock bool, lockTorrent bool) (desired desiredRequestState) {
	t := p.t

	if lockTorrent {
		t.mu.RLock()
	}

	if !t.haveInfo(false) || t.closed.IsSet() || t.dataDownloadDisallowed.Bool() {
		if lockTorrent {
			t.mu.RUnlock()
		}

		return
	}

	input := t.getRequestStrategyInput()
	requestHeap := desiredPeerRequests{
		peer:           p,
		pieceStates:    t.requestPieceStates,
		requestIndexes: t.requestIndexes,
	}

	dirtyChunks := t.dirtyChunks.Clone()
	infoHash := t.infoHash
	pieceRequestOrder := t.getPieceRequestOrder()

	if lockTorrent {
		t.mu.RUnlock()
	}

	// Caller-provided allocation for roaring bitmap iteration.
	var it typedRoaring.Iterator[RequestIndex]

	callCount := 0
	iterCount := 0

	requestStrategy.GetRequestablePieces(
		input,
		pieceRequestOrder,
		func(ih InfoHash, pieceIndex int, pieceExtra requestStrategy.PieceRequestOrderState) {
			callCount++
			if ih != infoHash {
				return
			}
			if !p.peerHasPiece(pieceIndex, lock, lockTorrent) {
				return
			}

			func() {
				if lockTorrent {
					t.mu.Lock()
					defer t.mu.Unlock()
				}
				requestHeap.pieceStates[pieceIndex] = pieceExtra
			}()

			allowedFast := p.peerAllowedFast.Contains(pieceIndex)

			it.Initialize(&dirtyChunks)
			t.iterUndirtiedRequestIndexesInPiece(&it, pieceIndex, func(r requestStrategy.RequestIndex) {
				iterCount++
				if !allowedFast {
					// We must signal interest to request this. TODO: We could set interested if the
					// peers pieces (minus the allowed fast set) overlap with our missing pieces if
					// there are any readers, or any pending pieces.
					desired.Interested = true
					// We can make or will allow sustaining a request here if we're not choked, or
					// have made the request previously (presumably while unchoked), and haven't had
					// the peer respond yet (and the request was retained because we are using the
					// fast extension).

					cantRequest := func() bool {
						if lock {
							p.mu.RLock()
							defer p.mu.RUnlock()
						}
						peerChoking := p.peerChoking
						hasRequest := p.requestState.Requests.Contains(r)
						return peerChoking && !hasRequest
					}

					if cantRequest() {
						// We can't request this right now.
						return
					}
				}

				awaitingCancel := func() bool {
					if lock {
						p.mu.RLock()
						defer p.mu.RUnlock()
					}
					cancelled := &p.requestState.Cancelled
					return !cancelled.IsEmpty() && cancelled.Contains(r)
				}

				if awaitingCancel() {
					// Can't re-request while awaiting acknowledgement.
					return
				}

				func() {
					if lockTorrent {
						t.mu.Lock()
						defer t.mu.Unlock()
					}
					requestHeap.requestIndexes = append(requestHeap.requestIndexes, r)
				}()
			}, lockTorrent)
		},
		lockTorrent)

	t.assertPendingRequests(lockTorrent)
	desired.Requests = requestHeap

	func() {
		if lock {
			p.mu.Lock()
			defer p.mu.Unlock()
		}
		p.desiredRequestLen = len(desired.Requests.requestIndexes)
	}()

	if debug {
		cap, ok := input.Capacity()
		maxuv := input.MaxUnverifiedBytes()
		rolen := t.getPieceRequestOrder().Len()

		p.logger.Levelf(log.Debug, "desired req=%d cap=%d ok=%v maxuv=%d rolen=%d indexes=%d states=%d calls=%d iter=%d", p.desiredRequestLen,
			cap, ok, maxuv, rolen, len(t.requestIndexes), len(t.requestPieceStates), callCount, iterCount)
	}

	return
}

func (p *Peer) maybeUpdateActualRequestState(lock bool, lockTorrent bool) {
	if p.closed.IsSet() {
		return
	}
	if p.needRequestUpdate == "" {
		return
	}
	if p.needRequestUpdate == peerUpdateRequestsTimerReason {
		since := time.Since(p.lastRequestUpdate)
		if since < updateRequestsTimerDuration {
			panic(since)
		}
	}
	pprof.Do(
		context.Background(),
		pprof.Labels("update request", p.needRequestUpdate),
		func(_ context.Context) {
			next := p.getDesiredRequestState(false, lock, lockTorrent)
			p.applyRequestState(next, lock, lockTorrent)
			p.t.cacheNextRequestIndexesForReuse(next.Requests.requestIndexes, lockTorrent)
		},
	)
}

func (t *Torrent) cacheNextRequestIndexesForReuse(slice []RequestIndex, lock bool) {
	if lock {
		t.mu.Lock()
		defer t.mu.Unlock()
	}
	// The incoming slice can be smaller when getDesiredRequestState short circuits on some
	// conditions.
	if cap(slice) > cap(t.requestIndexes) {
		t.requestIndexes = slice[:0]
	}
}

// Whether we should allow sending not interested ("losing interest") to the peer. I noticed
// qBitTorrent seems to punish us for sending not interested when we're streaming and don't
// currently need anything.
func (p *Peer) allowSendNotInterested(lock bool, lockTorrent bool) bool {
	// Except for caching, we're not likely to lose pieces very soon.
	if p.t.haveAllPieces(lockTorrent) {
		return true
	}
	all, known := p.peerHasAllPieces(lock, lockTorrent)
	if all || !known {
		return false
	}
	// Allow losing interest if we have all the pieces the peer has.
	if lockTorrent {
		p.t.mu.RLock()
		defer p.t.mu.RUnlock()
	}
	return roaring.AndNot(p.peerPieces(lock), &p.t._completedPieces).IsEmpty()
}

// Transmit/action the request state to the peer.
func (p *Peer) applyRequestState(next desiredRequestState, lock bool, lockTorrent bool) {
	t := p.t

	if lockTorrent {
		t.mu.Lock()
		defer t.mu.Unlock()
	}

	if lock {
		p.mu.Lock()
		defer p.mu.Unlock()
	}

	current := &p.requestState
	// Make interest sticky
	if !next.Interested && current.Interested {
		if !p.allowSendNotInterested(false, false) {
			next.Interested = true
		}
	}

	if !p.setInterested(next.Interested, false) {
		return
	}

	more := true
	orig := next.Requests.requestIndexes
	requestHeap := heap.InterfaceForSlice(
		&next.Requests.requestIndexes,
		next.Requests.lessByValue,
	)

	heap.Init(requestHeap)

	originalRequestCount := current.Requests.GetCardinality()

	for {

		if requestHeap.Len() == 0 {
			break
		}

		numPending := maxRequests(current.Requests.GetCardinality() + current.Cancelled.GetCardinality())
		if numPending >= p.peerImpl.nominalMaxRequests(false, false) {
			break
		}

		req := heap.Pop(requestHeap)
		if cap(next.Requests.requestIndexes) != cap(orig) {
			panic("changed")
		}

		if p.needRequestUpdate == "Peer.remoteRejectedRequest" {
			continue
		}

		existing := t.requestingPeer(req, false)

		if existing != nil && existing != p {
			if p.needRequestUpdate == "Peer.cancel" {
				continue
			}

			// Don't steal from the poor.
			diff := int64(current.Requests.GetCardinality()) + 1 - (int64(existing.uncancelledRequests(false)) - 1)

			// Steal a request that leaves us with one more request than the existing peer
			// connection if the stealer more recently received a chunk.
			if diff > 1 || (diff == 1 && !p.lastUsefulChunkReceived.After(existing.lastUsefulChunkReceived)) {
				continue
			}

			t.cancelRequest(req, false, false, false)
		}

		more = p.mustRequest(req, false, false)
		if !more {
			break
		}
	}

	if !more {
		// This might fail if we incorrectly determine that we can fit up to the maximum allowed
		// requests into the available write buffer space. We don't want that to happen because it
		// makes our peak requests dependent on how much was already in the buffer.
		panic(fmt.Sprintf(
			"couldn't fill apply entire request state [newRequests=%v]",
			current.Requests.GetCardinality()-originalRequestCount))
	}

	newPeakRequests := maxRequests(current.Requests.GetCardinality() - originalRequestCount)
	//log.Printf(
	//	"%s: requests %v->%v (peak %v->%v) reason %q (peer %v)\n",
	//	t.name(), originalRequestCount, current.Requests.GetCardinality(), p.peakRequests, newPeakRequests, p.needRequestUpdate, p)
	p.peakRequests = newPeakRequests
	p.needRequestUpdate = ""
	p.lastRequestUpdate = time.Now()
	if enableUpdateRequestsTimer {
		p.updateRequestsTimer.Reset(updateRequestsTimerDuration)
	}
}

// This could be set to 10s to match the unchoke/request update interval recommended by some
// specifications. I've set it shorter to trigger it more often for testing for now.
const (
	updateRequestsTimerDuration = 3 * time.Second
	enableUpdateRequestsTimer   = false
)
