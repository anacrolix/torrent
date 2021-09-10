package torrent

import (
	"math"
	"time"
	"unsafe"
	
	"github.com/anacrolix/missinggo/v2/bitmap"

	"github.com/anacrolix/chansync"
	request_strategy "github.com/anacrolix/torrent/request-strategy"
	"github.com/anacrolix/torrent/types"
)

func (cl *Client) requester() {
	var update chansync.Signaled
	// NewTimer that can't possibly expire before we Stop() it.
	timer := time.NewTimer(math.MaxInt64)
	timer.Stop()
	for {
		select {
		case <-cl.closed.Done(): goto STOP
		default:
		}

		cl.lock()
		cl.doRequests()
		update = cl.updateRequests.Signaled()
		cl.unlock()

		timer.Reset(100*time.Millisecond)
		select {
		case <-cl.closed.Done(): goto STOP
		case <-timer.C:  // timer fired; can Reset() without Stop()
			timer.Reset(time.Second)
			select {
			case <-cl.closed.Done(): goto STOP
			case <-update:
				// Ensure timer is stopped and drained before next loop.
				// It may be possible that the timer has expired as we reach this point,
				// so we check for expiration and drain if needed. Otherwise just stop timer.
				if !timer.Stop() {
					<-timer.C
				}
			case <-timer.C:
			}
			// timer should be ready for any Resets by this point
		}
	}
STOP:
	timer.Stop()
}

func (cl *Client) tickleRequester() {
	cl.updateRequests.Broadcast()
}

func (cl *Client) doRequests() {
	ts := make([]request_strategy.Torrent, 0, len(cl.torrents))
	for _, t := range cl.torrents {
		rst := request_strategy.Torrent{
			StableId: uintptr(unsafe.Pointer(t)),
		}
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
				Id:           (*peerId)(p),
			})
		})
		ts = append(ts, rst)
	}
	nextPeerStates := request_strategy.Run(request_strategy.Input{
		Torrents:           ts,
		MaxUnverifiedBytes: cl.config.MaxUnverifiedBytes,
	})
	for p, state := range nextPeerStates {
		setPeerNextRequestState(p, state)
	}
}

type peerId Peer

func (p *peerId) Uintptr() uintptr {
	return uintptr(unsafe.Pointer(p))
}

func setPeerNextRequestState(_p request_strategy.PeerId, rp request_strategy.PeerNextRequestState) {
	p := (*Peer)(_p.(*peerId))
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
