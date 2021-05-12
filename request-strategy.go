package torrent

import (
	"sort"
	"time"
	"unsafe"

	"github.com/anacrolix/multiless"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/bradfitz/iter"
)

type clientPieceRequestOrder struct {
	pieces []pieceRequestOrderPiece
}

type pieceRequestOrderPiece struct {
	t            *Torrent
	index        pieceIndex
	prio         piecePriority
	partial      bool
	availability int64
	request      bool
}

func (me *clientPieceRequestOrder) Len() int {
	return len(me.pieces)
}

func (me clientPieceRequestOrder) sort() {
	sort.Slice(me.pieces, me.less)
}

func (me clientPieceRequestOrder) less(_i, _j int) bool {
	i := me.pieces[_i]
	j := me.pieces[_j]
	return multiless.New().Int(
		int(j.prio), int(i.prio),
	).Bool(
		j.partial, i.partial,
	).Int64(i.availability, j.availability).Int(i.index, j.index).Less()
}

func (cl *Client) requester() {
	for {
		func() {
			cl.lock()
			defer cl.unlock()
			cl.doRequests()
		}()
		select {
		case <-cl.closed.LockedChan(cl.locker()):
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type requestsPeer struct {
	cur                        *Peer
	nextRequests               map[Request]struct{}
	nextInterest               bool
	requestablePiecesRemaining int
}

func (rp *requestsPeer) canRequestPiece(p pieceIndex) bool {
	return rp.hasPiece(p) && (!rp.choking() || rp.pieceAllowedFast(p))
}

func (rp *requestsPeer) hasPiece(i pieceIndex) bool {
	return rp.cur.peerHasPiece(i)
}

func (rp *requestsPeer) pieceAllowedFast(p pieceIndex) bool {
	return rp.cur.peerAllowedFast.Contains(p)
}

func (rp *requestsPeer) choking() bool {
	return rp.cur.peerChoking
}

func (rp *requestsPeer) hasExistingRequest(r Request) bool {
	_, ok := rp.cur.requests[r]
	return ok
}

func (rp *requestsPeer) canFitRequest() bool {
	return len(rp.nextRequests) < rp.cur.nominalMaxRequests()
}

// Returns true if it is added and wasn't there before.
func (rp *requestsPeer) addNextRequest(r Request) bool {
	_, ok := rp.nextRequests[r]
	if ok {
		return false
	}
	rp.nextRequests[r] = struct{}{}
	return true
}

type peersForPieceRequests struct {
	requestsInPiece int
	*requestsPeer
}

func (me *peersForPieceRequests) addNextRequest(r Request) {
	if me.requestsPeer.addNextRequest(r) {
		return
		me.requestsInPiece++
	}
}

func (cl *Client) doRequests() {
	requestOrder := &cl.pieceRequestOrder
	requestOrder.pieces = requestOrder.pieces[:0]
	allPeers := make(map[*Torrent][]*requestsPeer)
	// Storage capacity left for this run, keyed by the storage capacity pointer on the storage
	// TorrentImpl.
	storageLeft := make(map[*func() *int64]*int64)
	for _, t := range cl.torrents {
		// TODO: We could do metainfo requests here.
		if !t.haveInfo() {
			continue
		}
		key := t.storage.Capacity
		if key != nil {
			if _, ok := storageLeft[key]; !ok {
				storageLeft[key] = (*key)()
			}
		}
		var peers []*requestsPeer
		t.iterPeers(func(p *Peer) {
			if !p.closed.IsSet() {
				peers = append(peers, &requestsPeer{
					cur:          p,
					nextRequests: make(map[Request]struct{}),
				})
			}
		})
		for i := range iter.N(t.numPieces()) {
			tp := t.piece(i)
			pp := tp.purePriority()
			request := !t.ignorePieceForRequests(i)
			requestOrder.pieces = append(requestOrder.pieces, pieceRequestOrderPiece{
				t:            t,
				index:        i,
				prio:         pp,
				partial:      t.piecePartiallyDownloaded(i),
				availability: tp.availability,
				request:      request,
			})
			if request {
				for _, p := range peers {
					if p.canRequestPiece(i) {
						p.requestablePiecesRemaining++
					}
				}
			}
		}
		allPeers[t] = peers
	}
	requestOrder.sort()
	for _, p := range requestOrder.pieces {
		torrentPiece := p.t.piece(p.index)
		if left := storageLeft[p.t.storage.Capacity]; left != nil {
			if *left < int64(torrentPiece.length()) {
				continue
			}
			*left -= int64(torrentPiece.length())
		}
		if !p.request {
			continue
		}
		peersForPiece := make([]*peersForPieceRequests, 0, len(allPeers[p.t]))
		for _, peer := range allPeers[p.t] {
			peersForPiece = append(peersForPiece, &peersForPieceRequests{
				requestsInPiece: 0,
				requestsPeer:    peer,
			})
		}
		sortPeersForPiece := func() {
			sort.Slice(peersForPiece, func(i, j int) bool {
				return multiless.New().Int(
					peersForPiece[i].requestsInPiece,
					peersForPiece[j].requestsInPiece,
				).Int(
					peersForPiece[i].requestablePiecesRemaining,
					peersForPiece[j].requestablePiecesRemaining,
				).Float64(
					peersForPiece[j].cur.downloadRate(),
					peersForPiece[i].cur.downloadRate(),
				).EagerSameLess(
					peersForPiece[i].cur.completedHandshake.Equal(peersForPiece[j].cur.completedHandshake),
					peersForPiece[i].cur.completedHandshake.Before(peersForPiece[j].cur.completedHandshake),
					// TODO: Probably peer priority can come next
				).Uintptr(
					uintptr(unsafe.Pointer(peersForPiece[j].cur)),
					uintptr(unsafe.Pointer(peersForPiece[i].cur)),
				).Less()
			})
		}
		pendingChunksRemaining := int(p.t.pieceNumPendingChunks(p.index))
		torrentPiece.iterUndirtiedChunks(func(chunk ChunkSpec) bool {
			req := Request{pp.Integer(p.index), chunk}
			pendingChunksRemaining--
			sortPeersForPiece()
			skipped := 0
			// Try up to the number of peers that could legitimately receive the request equal to
			// the number of chunks left. This should ensure that only the best peers serve the last
			// few chunks in a piece.
			for _, peer := range peersForPiece {
				if !peer.canFitRequest() || !peer.hasPiece(p.index) || (!peer.pieceAllowedFast(p.index) && peer.choking()) {
					continue
				}
				if skipped > pendingChunksRemaining {
					break
				}
				if !peer.hasExistingRequest(req) {
					skipped++
					continue
				}
				if !peer.pieceAllowedFast(p.index) {
					// We must stay interested for this.
					peer.nextInterest = true
				}
				peer.addNextRequest(req)
				return true
			}
			for _, peer := range peersForPiece {
				if !peer.canFitRequest() {
					continue
				}
				if !peer.hasPiece(p.index) {
					continue
				}
				if !peer.pieceAllowedFast(p.index) {
					// TODO: Verify that's okay to stay uninterested if we request allowed fast
					// pieces.
					peer.nextInterest = true
					if peer.choking() {
						continue
					}
				}
				peer.addNextRequest(req)
				return true
			}
			return true
		})
		if pendingChunksRemaining != 0 {
			panic(pendingChunksRemaining)
		}
		for _, peer := range peersForPiece {
			if peer.canRequestPiece(p.index) {
				peer.requestablePiecesRemaining--
			}
		}
	}
	for _, peers := range allPeers {
		for _, rp := range peers {
			if rp.requestablePiecesRemaining != 0 {
				panic(rp.requestablePiecesRemaining)
			}
			applyPeerNextRequests(rp)
		}
	}
}

func applyPeerNextRequests(rp *requestsPeer) {
	p := rp.cur
	p.setInterested(rp.nextInterest)
	for req := range p.requests {
		if _, ok := rp.nextRequests[req]; !ok {
			p.cancel(req)
		}
	}
	for req := range rp.nextRequests {
		err := p.request(req)
		if err != nil {
			panic(err)
		} else {
			//log.Print(req)
		}
	}
}
