package torrent

import (
	"time"
	"unsafe"

	request_strategy "github.com/anacrolix/torrent/request-strategy"
	"github.com/anacrolix/torrent/types"
)

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

func (cl *Client) doRequests() {
	ts := make([]*request_strategy.Torrent, 0, len(cl.torrents))
	for _, t := range cl.torrents {
		rst := &request_strategy.Torrent{}
		if t.storage != nil {
			rst.Capacity = t.storage.Capacity
		}
		for i := range t.pieces {
			p := &t.pieces[i]
			rst.Pieces = append(rst.Pieces, request_strategy.Piece{
				Request:          !t.ignorePieceForRequests(i),
				Priority:         p.purePriority(),
				Partial:          t.piecePartiallyDownloaded(i),
				Availability:     p.availability,
				Length:           int64(p.length()),
				NumPendingChunks: int(t.pieceNumPendingChunks(i)),
				IterPendingChunks: func(f func(types.ChunkSpec)) {
					p.iterUndirtiedChunks(func(cs ChunkSpec) bool {
						f(cs)
						return true
					})
				},
			})
		}
		t.iterPeers(func(p *Peer) {
			if p.closed.IsSet() {
				return
			}
			rst.Peers = append(rst.Peers, request_strategy.Peer{
				HasPiece:    p.peerHasPiece,
				MaxRequests: p.nominalMaxRequests(),
				HasExistingRequest: func(r request_strategy.Request) bool {
					_, ok := p.requests[r]
					return ok
				},
				Choking: p.peerChoking,
				PieceAllowedFast: func(i pieceIndex) bool {
					return p.peerAllowedFast.Contains(i)
				},
				DownloadRate: p.downloadRate(),
				Age:          time.Since(p.completedHandshake),
				Id:           (*peerId)(p),
			})
		})
		ts = append(ts, rst)
	}
	nextPeerStates := cl.pieceRequestOrder.DoRequests(ts)
	for p, state := range nextPeerStates {
		applyPeerNextRequestState(p, state)
	}
}

type peerId Peer

func (p *peerId) Uintptr() uintptr {
	return uintptr(unsafe.Pointer(p))
}

func applyPeerNextRequestState(_p request_strategy.PeerId, rp request_strategy.PeerNextRequestState) {
	p := (*Peer)(_p.(*peerId))
	p.setInterested(rp.Interested)
	for req := range p.requests {
		if _, ok := rp.Requests[req]; !ok {
			p.cancel(req)
		}
	}
	for req := range rp.Requests {
		err := p.request(req)
		if err != nil {
			panic(err)
		} else {
			//log.Print(req)
		}
	}
}
