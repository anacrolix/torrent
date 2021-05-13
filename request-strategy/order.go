package request_strategy

import (
	"sort"

	"github.com/anacrolix/multiless"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/types"
	"github.com/davecgh/go-spew/spew"
)

type (
	Request       = types.Request
	pieceIndex    = types.PieceIndex
	piecePriority = types.PiecePriority
	// This can be made into a type-param later, will be great for testing.
	ChunkSpec = types.ChunkSpec
)

type ClientPieceOrder struct {
	pieces []pieceRequestOrderPiece
}

type pieceRequestOrderPiece struct {
	t     *Torrent
	index pieceIndex
	Piece
}

func (me *ClientPieceOrder) Len() int {
	return len(me.pieces)
}

func (me ClientPieceOrder) sort() {
	sort.Slice(me.pieces, me.less)
}

func (me ClientPieceOrder) less(_i, _j int) bool {
	i := me.pieces[_i]
	j := me.pieces[_j]
	return multiless.New().Int(
		int(j.Priority), int(i.Priority),
	).Bool(
		j.Partial, i.Partial,
	).Int64(i.Availability, j.Availability).Int(i.index, j.index).Less()
}

type requestsPeer struct {
	Peer
	nextState                  PeerNextRequestState
	requestablePiecesRemaining int
}

func (rp *requestsPeer) canFitRequest() bool {
	return len(rp.nextState.Requests) < rp.MaxRequests
}

// Returns true if it is added and wasn't there before.
func (rp *requestsPeer) addNextRequest(r Request) bool {
	_, ok := rp.nextState.Requests[r]
	if ok {
		return false
	}
	rp.nextState.Requests[r] = struct{}{}
	return true
}

type peersForPieceRequests struct {
	requestsInPiece int
	*requestsPeer
}

func (me *peersForPieceRequests) addNextRequest(r Request) {
	if me.requestsPeer.addNextRequest(r) {
		me.requestsInPiece++
	}
}

type Torrent struct {
	Pieces   []Piece
	Capacity *func() *int64
	Peers    []Peer // not closed.
}

func (requestOrder *ClientPieceOrder) DoRequests(torrents []*Torrent) map[PeerId]PeerNextRequestState {
	requestOrder.pieces = requestOrder.pieces[:0]
	allPeers := make(map[*Torrent][]*requestsPeer)
	// Storage capacity left for this run, keyed by the storage capacity pointer on the storage
	// TorrentImpl.
	storageLeft := make(map[*func() *int64]*int64)
	for _, t := range torrents {
		// TODO: We could do metainfo requests here.
		key := t.Capacity
		if key != nil {
			if _, ok := storageLeft[key]; !ok {
				storageLeft[key] = (*key)()
			}
		}
		var peers []*requestsPeer
		for _, p := range t.Peers {
			peers = append(peers, &requestsPeer{
				Peer: p,
				nextState: PeerNextRequestState{
					Requests: make(map[Request]struct{}),
				},
			})
		}
		for i, tp := range t.Pieces {
			requestOrder.pieces = append(requestOrder.pieces, pieceRequestOrderPiece{
				t:     t,
				index: i,
				Piece: tp,
			})
			if tp.Request {
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
		torrentPiece := p
		if left := storageLeft[p.t.Capacity]; left != nil {
			if *left < int64(torrentPiece.Length) {
				continue
			}
			*left -= int64(torrentPiece.Length)
		}
		if !p.Request {
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
					peersForPiece[j].DownloadRate,
					peersForPiece[i].DownloadRate,
				).Int64(
					int64(peersForPiece[j].Age), int64(peersForPiece[i].Age),
					// TODO: Probably peer priority can come next
				).Uintptr(
					peersForPiece[i].Id.Uintptr(),
					peersForPiece[j].Id.Uintptr(),
				).MustLess()
			})
		}
		pendingChunksRemaining := int(p.NumPendingChunks)
		if f := torrentPiece.IterPendingChunks; f != nil {
			f(func(chunk types.ChunkSpec) {
				req := Request{pp.Integer(p.index), chunk}
				pendingChunksRemaining--
				sortPeersForPiece()
				spew.Dump(peersForPiece)
				skipped := 0
				// Try up to the number of peers that could legitimately receive the request equal to
				// the number of chunks left. This should ensure that only the best peers serve the last
				// few chunks in a piece.
				for _, peer := range peersForPiece {
					if !peer.canFitRequest() || !peer.HasPiece(p.index) || (!peer.pieceAllowedFastOrDefault(p.index) && peer.Choking) {
						continue
					}
					if skipped >= pendingChunksRemaining {
						break
					}
					if f := peer.HasExistingRequest; f == nil || !f(req) {
						skipped++
						continue
					}
					if !peer.pieceAllowedFastOrDefault(p.index) {
						// We must stay interested for this.
						peer.nextState.Interested = true
					}
					peer.addNextRequest(req)
					return
				}
				for _, peer := range peersForPiece {
					if !peer.canFitRequest() {
						continue
					}
					if !peer.HasPiece(p.index) {
						continue
					}
					if !peer.pieceAllowedFastOrDefault(p.index) {
						// TODO: Verify that's okay to stay uninterested if we request allowed fast
						// pieces.
						peer.nextState.Interested = true
						if peer.Choking {
							continue
						}
					}
					peer.addNextRequest(req)
					return
				}
			})
		}
		if pendingChunksRemaining != 0 {
			panic(pendingChunksRemaining)
		}
		for _, peer := range peersForPiece {
			if peer.canRequestPiece(p.index) {
				peer.requestablePiecesRemaining--
			}
		}
	}
	ret := make(map[PeerId]PeerNextRequestState)
	for _, peers := range allPeers {
		for _, rp := range peers {
			if rp.requestablePiecesRemaining != 0 {
				panic(rp.requestablePiecesRemaining)
			}
			ret[rp.Id] = rp.nextState
		}
	}
	return ret
}
