package request_strategy

import (
	"bytes"
	"fmt"
	"sort"
	"sync"

	"github.com/anacrolix/multiless"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	"github.com/anacrolix/torrent/types"
)

type (
	RequestIndex  = uint32
	ChunkIndex    = uint32
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
		).Lazy(func() multiless.Computation {
			return multiless.New().Cmp(bytes.Compare(
				i.t.InfoHash[:],
				j.t.InfoHash[:],
			))
		}).MustLess()
	})
}

type requestsPeer struct {
	Peer
	nextState                  PeerNextRequestState
	requestablePiecesRemaining int
}

func (rp *requestsPeer) canFitRequest() bool {
	return int(rp.nextState.Requests.GetCardinality()) < rp.MaxRequests
}

func (rp *requestsPeer) addNextRequest(r RequestIndex) {
	if !rp.nextState.Requests.CheckedAdd(r) {
		panic("should only add once")
	}
}

type peersForPieceRequests struct {
	requestsInPiece int
	*requestsPeer
}

func (me *peersForPieceRequests) addNextRequest(r RequestIndex) {
	me.requestsPeer.addNextRequest(r)
	me.requestsInPiece++
}

type requestablePiece struct {
	index             pieceIndex
	t                 *Torrent
	alwaysReallocate  bool
	NumPendingChunks  int
	IterPendingChunks ChunksIterFunc
}

func (p *requestablePiece) chunkIndexToRequestIndex(c ChunkIndex) RequestIndex {
	return p.t.ChunksPerPiece*uint32(p.index) + c
}

type filterPiece struct {
	t     *filterTorrent
	index pieceIndex
	*Piece
}

// Calls f with requestable pieces in order.
func GetRequestablePieces(input Input, f func(t *Torrent, p *Piece, pieceIndex int)) {
	maxPieces := 0
	for i := range input.Torrents {
		maxPieces += len(input.Torrents[i].Pieces)
	}
	pieces := make([]filterPiece, 0, maxPieces)
	// Storage capacity left for this run, keyed by the storage capacity pointer on the storage
	// TorrentImpl. A nil value means no capacity limit.
	storageLeft := make(map[storage.TorrentCapacity]*int64)
	for _t := range input.Torrents {
		// TODO: We could do metainfo requests here.
		t := &filterTorrent{
			Torrent:         &input.Torrents[_t],
			unverifiedBytes: 0,
		}
		key := t.Capacity
		if key != nil {
			if _, ok := storageLeft[key]; !ok {
				capacity, ok := (*key)()
				if ok {
					storageLeft[key] = &capacity
				} else {
					storageLeft[key] = nil
				}
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
		f(piece.t.Torrent, piece.Piece, piece.index)
	}
	return
}

type Input struct {
	Torrents           []Torrent
	MaxUnverifiedBytes int64
}

// TODO: We could do metainfo requests here.
func Run(input Input) map[PeerId]PeerNextRequestState {
	var requestPieces []requestablePiece
	GetRequestablePieces(input, func(t *Torrent, piece *Piece, pieceIndex int) {
		requestPieces = append(requestPieces, requestablePiece{
			index:             pieceIndex,
			t:                 t,
			NumPendingChunks:  piece.NumPendingChunks,
			IterPendingChunks: piece.iterPendingChunksWrapper,
			alwaysReallocate:  piece.Priority >= types.PiecePriorityNext,
		})
	})
	torrents := input.Torrents
	allPeers := make(map[metainfo.Hash][]*requestsPeer, len(torrents))
	for _, t := range torrents {
		peers := make([]*requestsPeer, 0, len(t.Peers))
		for _, p := range t.Peers {
			peers = append(peers, &requestsPeer{
				Peer: p,
			})
		}
		allPeers[t.InfoHash] = peers
	}
	for _, piece := range requestPieces {
		for _, peer := range allPeers[piece.t.InfoHash] {
			if peer.canRequestPiece(piece.index) {
				peer.requestablePiecesRemaining++
			}
		}
	}
	for _, piece := range requestPieces {
		allocatePendingChunks(piece, allPeers[piece.t.InfoHash])
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
func ensureValidSortedPeersForPieceRequests(peers *peersForPieceSorter) {
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
	req           *RequestIndex
	p             requestablePiece
}

func (me *peersForPieceSorter) Len() int {
	return len(me.peersForPiece)
}

func (me *peersForPieceSorter) Swap(i, j int) {
	me.peersForPiece[i], me.peersForPiece[j] = me.peersForPiece[j], me.peersForPiece[i]
}

func (me *peersForPieceSorter) Less(_i, _j int) bool {
	i := me.peersForPiece[_i]
	j := me.peersForPiece[_j]
	req := me.req
	p := &me.p
	byHasRequest := func() multiless.Computation {
		ml := multiless.New()
		if req != nil {
			iHas := i.nextState.Requests.Contains(*req)
			jHas := j.nextState.Requests.Contains(*req)
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
	if ml.Ok() {
		return ml.Less()
	}
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
		if !peer.canRequestPiece(p.index) {
			continue
		}
		if !peer.canFitRequest() {
			peer.requestablePiecesRemaining--
			continue
		}
		peersForPiece = append(peersForPiece, &peersForPieceRequests{
			requestsInPiece: 0,
			requestsPeer:    peer,
		})
	}
	defer func() {
		for _, peer := range peersForPiece {
			peer.requestablePiecesRemaining--
		}
		peersForPiecesPool.Put(peersForPiece)
	}()
	peersForPieceSorter := peersForPieceSorter{
		peersForPiece: peersForPiece,
		p:             p,
	}
	sortPeersForPiece := func(req *RequestIndex) {
		peersForPieceSorter.req = req
		sort.Sort(&peersForPieceSorter)
		//ensureValidSortedPeersForPieceRequests(&peersForPieceSorter)
	}
	// Chunks can be preassigned several times, if peers haven't been able to update their "actual"
	// with "next" request state before another request strategy run occurs.
	preallocated := make([][]*peersForPieceRequests, p.t.ChunksPerPiece)
	p.IterPendingChunks(func(spec ChunkIndex) {
		req := p.chunkIndexToRequestIndex(spec)
		for _, peer := range peersForPiece {
			if !peer.ExistingRequests.Contains(req) {
				continue
			}
			if !peer.canFitRequest() {
				continue
			}
			preallocated[spec] = append(preallocated[spec], peer)
			peer.addNextRequest(req)
		}
	})
	pendingChunksRemaining := int(p.NumPendingChunks)
	p.IterPendingChunks(func(chunk ChunkIndex) {
		if len(preallocated[chunk]) != 0 {
			return
		}
		req := p.chunkIndexToRequestIndex(chunk)
		defer func() { pendingChunksRemaining-- }()
		sortPeersForPiece(nil)
		for _, peer := range peersForPiece {
			if !peer.canFitRequest() {
				continue
			}
			if !peer.PieceAllowedFast.ContainsInt(p.index) {
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
		if len(prePeers) == 0 {
			continue
		}
		pendingChunksRemaining--
		req := p.chunkIndexToRequestIndex(ChunkIndex(chunk))
		for _, pp := range prePeers {
			pp.requestsInPiece--
		}
		sortPeersForPiece(&req)
		for _, pp := range prePeers {
			pp.nextState.Requests.Remove(req)
		}
		for _, peer := range peersForPiece {
			if !peer.canFitRequest() {
				continue
			}
			if !peer.PieceAllowedFast.ContainsInt(p.index) {
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
