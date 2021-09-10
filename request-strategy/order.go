package request_strategy

import (
	"fmt"
	"sort"
	"sync"

	"github.com/anacrolix/multiless"

	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/types"
)

type (
	Request       = types.Request
	pieceIndex    = types.PieceIndex
	piecePriority = types.PiecePriority
	// This can be made into a type-param later, will be great for testing.
	ChunkSpec = types.ChunkSpec
)

type ClientPieceOrder struct{}

type filterTorrent struct {
	*Torrent
	unverifiedBytes int64
	// Potentially shared with other torrents.
	storageLeft *int64
}

func sortFilterPieces(pieces []filterPiece) {
	sort.Slice(pieces, func(_i, _j int) bool {
		i := &pieces[_i]
		j := &pieces[_j]
		return multiless.New().Int(
			int(j.Priority), int(i.Priority),
		).Bool(
			j.Partial, i.Partial,
		).Int64(
			i.Availability, j.Availability,
		).Int(
			i.index, j.index,
		).Uintptr(
			i.t.StableId, j.t.StableId,
		).MustLess()
	})
}

type requestsPeer struct {
	Peer
	nextState                  PeerNextRequestState
	requestablePiecesRemaining int
}

func (rp *requestsPeer) canFitRequest() bool {
	return len(rp.nextState.Requests) < rp.MaxRequests
}

func (rp *requestsPeer) addNextRequest(r Request) {
	_, ok := rp.nextState.Requests[r]
	if ok {
		panic("should only add once")
	}
	rp.nextState.Requests[r] = struct{}{}
}

type peersForPieceRequests struct {
	requestsInPiece int
	*requestsPeer
}

func (me *peersForPieceRequests) addNextRequest(r Request) {
	me.requestsPeer.addNextRequest(r)
	me.requestsInPiece++
}

type requestablePiece struct {
	index             pieceIndex
	t                 *Torrent
	alwaysReallocate  bool
	NumPendingChunks  int
	IterPendingChunks ChunksIter
}

type filterPiece struct {
	t     *filterTorrent
	index pieceIndex
	*Piece
}

func getRequestablePieces(input Input) (ret []requestablePiece) {
	maxPieces := 0
	for i := range input.Torrents {
		maxPieces += len(input.Torrents[i].Pieces)
	}
	pieces := make([]filterPiece, 0, maxPieces)
	ret = make([]requestablePiece, 0, maxPieces)
	// Storage capacity left for this run, keyed by the storage capacity pointer on the storage
	// TorrentImpl.
	storageLeft := make(map[*func() *int64]*int64)
	for _t := range input.Torrents {
		// TODO: We could do metainfo requests here.
		t := &filterTorrent{
			Torrent:         &input.Torrents[_t],
			unverifiedBytes: 0,
		}
		key := t.Capacity
		if key != nil {
			if _, ok := storageLeft[key]; !ok {
				storageLeft[key] = (*key)()
			}
			t.storageLeft = storageLeft[key]
		}
		for i := range t.Pieces {
			pieces = append(pieces, filterPiece{
				t:     t,
				index: i,
				Piece: &t.Pieces[i],
			})
		}
	}
	sortFilterPieces(pieces)
	var allTorrentsUnverifiedBytes int64
	for _, piece := range pieces {
		if left := piece.t.storageLeft; left != nil {
			if *left < int64(piece.Length) {
				continue
			}
			*left -= int64(piece.Length)
		}
		if !piece.Request || piece.NumPendingChunks == 0 {
			// TODO: Clarify exactly what is verified. Stuff that's being hashed should be
			// considered unverified and hold up further requests.
			continue
		}
		if piece.t.MaxUnverifiedBytes != 0 && piece.t.unverifiedBytes+piece.Length > piece.t.MaxUnverifiedBytes {
			continue
		}
		if input.MaxUnverifiedBytes != 0 && allTorrentsUnverifiedBytes+piece.Length > input.MaxUnverifiedBytes {
			continue
		}
		piece.t.unverifiedBytes += piece.Length
		allTorrentsUnverifiedBytes += piece.Length
		ret = append(ret, requestablePiece{
			index:             piece.index,
			t:                 piece.t.Torrent,
			NumPendingChunks:  piece.NumPendingChunks,
			IterPendingChunks: piece.iterPendingChunksWrapper,
			alwaysReallocate:  piece.Priority >= types.PiecePriorityNext,
		})
	}
	return
}

type Input struct {
	Torrents           []Torrent
	MaxUnverifiedBytes int64
}

// TODO: We could do metainfo requests here.
func Run(input Input) map[PeerId]PeerNextRequestState {
	requestPieces := getRequestablePieces(input)
	torrents := input.Torrents
	allPeers := make(map[uintptr][]*requestsPeer, len(torrents))
	for _, t := range torrents {
		peers := make([]*requestsPeer, 0, len(t.Peers))
		for _, p := range t.Peers {
			peers = append(peers, &requestsPeer{
				Peer: p,
				nextState: PeerNextRequestState{
					Requests: make(map[Request]struct{}, p.MaxRequests),
				},
			})
		}
		allPeers[t.StableId] = peers
	}
	for _, piece := range requestPieces {
		for _, peer := range allPeers[piece.t.StableId] {
			if peer.canRequestPiece(piece.index) {
				peer.requestablePiecesRemaining++
			}
		}
	}
	for _, piece := range requestPieces {
		allocatePendingChunks(piece, allPeers[piece.t.StableId])
	}
	ret := make(map[PeerId]PeerNextRequestState)
	for _, peers := range allPeers {
		for _, rp := range peers {
			if rp.requestablePiecesRemaining != 0 {
				panic(rp.requestablePiecesRemaining)
			}
			if _, ok := ret[rp.Id]; ok {
				panic(fmt.Sprintf("duplicate peer id: %v", rp.Id))
			}
			ret[rp.Id] = rp.nextState
		}
	}
	return ret
}

// Checks that a sorted peersForPiece slice makes sense.
func ensureValidSortedPeersForPieceRequests(peers peersForPieceSorter) {
	if !sort.IsSorted(peers) {
		panic("not sorted")
	}
	peerMap := make(map[*peersForPieceRequests]struct{}, peers.Len())
	for _, p := range peers.peersForPiece {
		if _, ok := peerMap[p]; ok {
			panic(p)
		}
		peerMap[p] = struct{}{}
	}
}

var peersForPiecesPool sync.Pool

func makePeersForPiece(cap int) []*peersForPieceRequests {
	got := peersForPiecesPool.Get()
	if got == nil {
		return make([]*peersForPieceRequests, 0, cap)
	}
	return got.([]*peersForPieceRequests)[:0]
}

type peersForPieceSorter struct {
	peersForPiece []*peersForPieceRequests
	req           *Request
	p             requestablePiece
}

func (me peersForPieceSorter) Len() int {
	return len(me.peersForPiece)
}

func (me peersForPieceSorter) Swap(i, j int) {
	me.peersForPiece[i], me.peersForPiece[j] = me.peersForPiece[j], me.peersForPiece[i]
}

func (me peersForPieceSorter) Less(_i, _j int) bool {
	i := me.peersForPiece[_i]
	j := me.peersForPiece[_j]
	req := me.req
	p := me.p
	byHasRequest := func() multiless.Computation {
		ml := multiless.New()
		if req != nil {
			_, iHas := i.nextState.Requests[*req]
			_, jHas := j.nextState.Requests[*req]
			ml = ml.Bool(jHas, iHas)
		}
		return ml
	}()
	ml := multiless.New()
	// We always "reallocate", that is force even striping amongst peers that are either on
	// the last piece they can contribute too, or for pieces marked for this behaviour.
	// Striping prevents starving peers of requests, and will always re-balance to the
	// fastest known peers.
	if !p.alwaysReallocate {
		ml = ml.Bool(
			j.requestablePiecesRemaining == 1,
			i.requestablePiecesRemaining == 1)
	}
	if p.alwaysReallocate || j.requestablePiecesRemaining == 1 {
		ml = ml.Int(
			i.requestsInPiece,
			j.requestsInPiece)
	} else {
		ml = ml.AndThen(byHasRequest)
	}
	ml = ml.Int(
		i.requestablePiecesRemaining,
		j.requestablePiecesRemaining,
	).Float64(
		j.DownloadRate,
		i.DownloadRate,
	)
	ml = ml.AndThen(byHasRequest)
	return ml.Int64(
		int64(j.Age), int64(i.Age),
		// TODO: Probably peer priority can come next
	).Uintptr(
		i.Id.Uintptr(),
		j.Id.Uintptr(),
	).MustLess()
}

func allocatePendingChunks(p requestablePiece, peers []*requestsPeer) {
	peersForPiece := makePeersForPiece(len(peers))
	for _, peer := range peers {
		peersForPiece = append(peersForPiece, &peersForPieceRequests{
			requestsInPiece: 0,
			requestsPeer:    peer,
		})
	}
	defer func() {
		for _, peer := range peersForPiece {
			if peer.canRequestPiece(p.index) {
				peer.requestablePiecesRemaining--
			}
		}
		peersForPiecesPool.Put(peersForPiece)
	}()
	peersForPieceSorter := peersForPieceSorter{
		peersForPiece: peersForPiece,
		p:             p,
	}
	sortPeersForPiece := func(req *Request) {
		peersForPieceSorter.req = req
		sort.Sort(&peersForPieceSorter)
		//ensureValidSortedPeersForPieceRequests(peersForPieceSorter)
	}
	// Chunks can be preassigned several times, if peers haven't been able to update their "actual"
	// with "next" request state before another request strategy run occurs.
	preallocated := make(map[ChunkSpec][]*peersForPieceRequests, p.NumPendingChunks)
	p.IterPendingChunks(func(spec ChunkSpec) {
		req := Request{pp.Integer(p.index), spec}
		for _, peer := range peersForPiece {
			if h := peer.HasExistingRequest; h == nil || !h(req) {
				continue
			}
			if !peer.canFitRequest() {
				continue
			}
			if !peer.canRequestPiece(p.index) {
				continue
			}
			preallocated[spec] = append(preallocated[spec], peer)
			peer.addNextRequest(req)
		}
	})
	pendingChunksRemaining := int(p.NumPendingChunks)
	p.IterPendingChunks(func(chunk types.ChunkSpec) {
		if _, ok := preallocated[chunk]; ok {
			return
		}
		req := Request{pp.Integer(p.index), chunk}
		defer func() { pendingChunksRemaining-- }()
		sortPeersForPiece(nil)
		for _, peer := range peersForPiece {
			if !peer.canFitRequest() {
				continue
			}
			if !peer.HasPiece(p.index) {
				continue
			}
			if !peer.pieceAllowedFastOrDefault(p.index) {
				// TODO: Verify that's okay to stay uninterested if we request allowed fast pieces.
				peer.nextState.Interested = true
				if peer.Choking {
					continue
				}
			}
			peer.addNextRequest(req)
			break
		}
	})
chunk:
	for chunk, prePeers := range preallocated {
		pendingChunksRemaining--
		req := Request{pp.Integer(p.index), chunk}
		for _, pp := range prePeers {
			pp.requestsInPiece--
		}
		sortPeersForPiece(&req)
		for _, pp := range prePeers {
			delete(pp.nextState.Requests, req)
		}
		for _, peer := range peersForPiece {
			if !peer.canFitRequest() {
				continue
			}
			if !peer.HasPiece(p.index) {
				continue
			}
			if !peer.pieceAllowedFastOrDefault(p.index) {
				// TODO: Verify that's okay to stay uninterested if we request allowed fast pieces.
				peer.nextState.Interested = true
				if peer.Choking {
					continue
				}
			}
			peer.addNextRequest(req)
			continue chunk
		}
	}
	if pendingChunksRemaining != 0 {
		panic(pendingChunksRemaining)
	}
}
