package torrent

import (
	"sort"
	"time"

	"github.com/anacrolix/log"
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
	availability int
}

func (me *clientPieceRequestOrder) addPieces(t *Torrent, numPieces pieceIndex) {
	for i := range iter.N(numPieces) {
		me.pieces = append(me.pieces, pieceRequestOrderPiece{
			t:     t,
			index: i,
		})
	}
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

func (me clientPieceRequestOrder) update() {
	for i := range me.pieces {
		p := &me.pieces[i]
		p.prio = p.t.piece(p.index).uncachedPriority()
		p.partial = p.t.piecePartiallyDownloaded(p.index)
		p.availability = p.t.pieceAvailability(p.index)
	}
}

func (me clientPieceRequestOrder) less(_i, _j int) bool {
	i := me.pieces[_i]
	j := me.pieces[_j]
	ml := multiless.New()
	ml.Int(int(j.prio), int(i.prio))
	ml.Bool(j.partial, i.partial)
	ml.Int(i.availability, j.availability)
	return ml.Less()
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
	storageCapacity := make(map[*Torrent]*int64)
	for _, t := range cl.torrents {
		// TODO: We could do metainfo requests here.
		if t.haveInfo() {
			value := int64(t.usualPieceSize())
			storageCapacity[t] = &value
			requestOrder.addPieces(t, t.numPieces())
		}
		var peers []*Peer
		t.iterPeers(func(p *Peer) {
			peers = append(peers, p)
		})
		allPeers[t] = peers
	}
	requestOrder.update()
	requestOrder.sort()
	for _, p := range requestOrder.pieces {
		if p.t.ignorePieceForRequests(p.index) {
			continue
		}
		peers := allPeers[p.t]
		torrentPiece := p.t.piece(p.index)
		if left := storageCapacity[p.t]; left != nil {
			if *left < int64(torrentPiece.length()) {
				continue
			}
			*left -= int64(torrentPiece.length())
		}
		p.t.piece(p.index).iterUndirtiedChunks(func(chunk ChunkSpec) bool {
			for _, peer := range peers {
				req := Request{pp.Integer(p.index), chunk}
				_, err := peer.request(req)
				if err == nil {
					log.Printf("requested %v", req)
					break
				}
			}
			return true
		})
	}
	for _, t := range cl.torrents {
		t.iterPeers(func(p *Peer) {
			if !p.peerChoking && p.numLocalRequests() == 0 && !p.writeBufferFull() {
				p.setInterested(false)
			}
		})
	}
}

//func (requestStrategyDefaults) iterUndirtiedChunks(p requestStrategyPiece, f func(ChunkSpec) bool) bool {
//	chunkIndices := p.dirtyChunks().Copy()
//	chunkIndices.FlipRange(0, bitmap.BitIndex(p.numChunks()))
//	return iter.ForPerm(chunkIndices.Len(), func(i int) bool {
//		ci, err := chunkIndices.RB.Select(uint32(i))
//		if err != nil {
//			panic(err)
//		}
//		return f(p.chunkIndexRequest(pp.Integer(ci)).ChunkSpec)
//	})
//}

//
//func iterUnbiasedPieceRequestOrder(
//	cn requestStrategyConnection,
//	f func(piece pieceIndex) bool,
//	pieceRequestOrder []pieceIndex,
//) bool {
//	cn.torrent().sortPieceRequestOrder(pieceRequestOrder)
//	for _, i := range pieceRequestOrder {
//		if !cn.peerHasPiece(i) || cn.torrent().ignorePieceForRequests(i) {
//			continue
//		}
//		if !f(i) {
//			return false
//		}
//	}
//	return true
//}
