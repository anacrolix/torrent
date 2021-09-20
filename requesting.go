package torrent

import (
	"time"
	"unsafe"

	"github.com/anacrolix/missinggo/v2/bitmap"

	"github.com/anacrolix/chansync"
	request_strategy "github.com/anacrolix/torrent/request-strategy"
)

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
		rst := request_strategy.Torrent{
			StableId: uintptr(unsafe.Pointer(t)),
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
				IterPendingChunks: p.iterUndirtiedChunks,
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
				HasPiece:    p.peerHasPiece,
				MaxRequests: p.nominalMaxRequests(),
				HasExistingRequest: func(r request_strategy.Request) bool {
					_, ok := p.actualRequestState.Requests[r]
					return ok
				},
				Choking: p.peerChoking,
				PieceAllowedFast: func(i pieceIndex) bool {
					return p.peerAllowedFast.Contains(bitmap.BitIndex(i))
				},
				DownloadRate: p.downloadRate(),
				Age:          time.Since(p.completedHandshake),
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
	nextPeerStates := request_strategy.Run(cl.getRequestStrategyInput())
	for p, state := range nextPeerStates {
		setPeerNextRequestState(p, state)
	}
}

type peerId struct {
	*Peer
	ptr uintptr
}

func (p peerId) Uintptr() uintptr {
	return p.ptr
}

func setPeerNextRequestState(_p request_strategy.PeerId, rp request_strategy.PeerNextRequestState) {
	p := _p.(peerId).Peer
	p.nextRequestState = rp
	p.onNextRequestStateChanged()
}

func (p *Peer) applyNextRequestState() bool {
	next := p.nextRequestState
	current := p.actualRequestState
	if !p.setInterested(next.Interested) {
		return false
	}
	for req := range current.Requests {
		if _, ok := next.Requests[req]; !ok {
			if !p.cancel(req) {
				return false
			}
		}
	}
	for req := range next.Requests {
		// This could happen if the peer chokes us between the next state being generated, and us
		// trying to transmit the state.
		if p.peerChoking && !p.peerAllowedFast.Contains(bitmap.BitIndex(req.Index)) {
			continue
		}
		more, err := p.request(req)
		if err != nil {
			panic(err)
		} /* else {
			log.Print(req)
		} */
		if !more {
			return false
		}
	}
	return true
}
