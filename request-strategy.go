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
}

func (me *clientPieceRequestOrder) addPieces(t *Torrent, numPieces pieceIndex) {
	for i := range iter.N(numPieces) {
		me.pieces = append(me.pieces, pieceRequestOrderPiece{
			t:     t,
			index: i,
		})
	}
}

func (me *clientPieceRequestOrder) Len() int {
	return len(me.pieces)
}

func (me *clientPieceRequestOrder) removePieces(t *Torrent) {
	newPieces := make([]pieceRequestOrderPiece, 0, len(me.pieces)-t.numPieces())
	for _, p := range me.pieces {
		if p.t != t {
			newPieces = append(newPieces, p)
		}
	}
	me.pieces = newPieces
}

func (me clientPieceRequestOrder) sort() {
	sort.SliceStable(me.pieces, me.less)
}

func (me *clientPieceRequestOrder) update() {
	for i := range me.pieces {
		p := &me.pieces[i]
		tp := p.t.piece(p.index)
		p.prio = tp.uncachedPriority()
		p.partial = p.t.piecePartiallyDownloaded(p.index)
		p.availability = tp.availability
	}
}

func (me clientPieceRequestOrder) less(_i, _j int) bool {
	i := me.pieces[_i]
	j := me.pieces[_j]
	return multiless.New().Int(
		int(j.prio), int(i.prio),
	).Bool(
		j.partial, i.partial,
	).Int64(
		i.availability, j.availability,
	).Less()
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
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func (cl *Client) doRequests() {
	requestOrder := clientPieceRequestOrder{}
	allPeers := make(map[*Torrent][]*Peer)
	// Storage capacity left for this run, keyed by the storage capacity pointer on the storage
	// TorrentImpl.
	storageLeft := make(map[*func() *int64]*int64)
	for _, t := range cl.torrents {
		// TODO: We could do metainfo requests here.
		if t.haveInfo() {
			if t.storage.Capacity != nil {
				if _, ok := storageLeft[t.storage.Capacity]; !ok {
					storageLeft[t.storage.Capacity] = (*t.storage.Capacity)()
				}
			}
			requestOrder.addPieces(t, t.numPieces())
		}
		var peers []*Peer
		t.iterPeers(func(p *Peer) {
			if !p.closed.IsSet() {
				peers = append(peers, p)
			}
		})
		// Sort in *desc* order, approximately the reverse of worseConn where appropriate.
		sort.Slice(peers, func(i, j int) bool {
			return multiless.New().Float64(
				peers[j].downloadRate(), peers[i].downloadRate(),
			).Uintptr(
				uintptr(unsafe.Pointer(peers[j])), uintptr(unsafe.Pointer(peers[i]))).Less()
		})
		allPeers[t] = peers
	}
	requestOrder.update()
	requestOrder.sort()
	// For a given piece, the set of allPeers indices that absorbed requests for the piece.
	contributed := make(map[int]struct{})
	for _, p := range requestOrder.pieces {
		peers := allPeers[p.t]
		torrentPiece := p.t.piece(p.index)
		if left := storageLeft[p.t.storage.Capacity]; left != nil {
			if *left < int64(torrentPiece.length()) {
				continue
			}
			*left -= int64(torrentPiece.length())
		}
		if p.t.ignorePieceForRequests(p.index) {
			continue
		}
		p.t.piece(p.index).iterUndirtiedChunks(func(chunk ChunkSpec) bool {
			req := Request{pp.Integer(p.index), chunk}
			const skipAlreadyRequested = false
			if skipAlreadyRequested {
				alreadyRequested := false
				p.t.iterPeers(func(p *Peer) {
					if _, ok := p.requests[req]; ok {
						alreadyRequested = true
					}
				})
				if alreadyRequested {
					return true
				}
			}
			alreadyRequested := false
			for peerIndex, peer := range peers {
				if alreadyRequested {
					// Cancel all requests from "slower" peers after the one that requested it.
					peer.cancel(req)
				} else {
					err := peer.request(req)
					if err == nil {
						contributed[peerIndex] = struct{}{}
						alreadyRequested = true
						//log.Printf("requested %v", req)
					}
				}
			}
			return true
		})
		// Move requestees for this piece to the back.
		lastIndex := len(peers) - 1
		// Probably should sort the contributees, to make the ordering more deterministic.
		for peerIndex := range contributed {
			peers[peerIndex], peers[lastIndex] = peers[lastIndex], peers[peerIndex]
			delete(contributed, peerIndex)
			lastIndex--
		}
	}
	for _, t := range cl.torrents {
		t.iterPeers(func(p *Peer) {
			if !p.peerChoking && p.numLocalRequests() == 0 && !p.writeBufferFull() {
				p.setInterested(false)
			}
		})
	}
}
