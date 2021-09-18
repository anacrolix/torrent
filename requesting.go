package torrent

import (
	"time"
	"unsafe"

	"github.com/anacrolix/missinggo/v2/bitmap"
	pp "github.com/anacrolix/torrent/peer_protocol"

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
			InfoHash: t.infoHash,
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
	if len(p.actualRequestState.Requests) > p.nominalMaxRequests()/2 {
		return true
	}
	type piece struct {
		index   int
		endGame bool
	}
	var pieceOrder []piece
	request_strategy.GetRequestablePieces(
		p.t.cl.getRequestStrategyInput(),
		func(t *request_strategy.Torrent, rsp *request_strategy.Piece, pieceIndex int) {
			if t.InfoHash != p.t.infoHash {
				return
			}
			if !p.peerHasPiece(pieceIndex) {
				return
			}
			pieceOrder = append(pieceOrder, piece{
				index:   pieceIndex,
				endGame: rsp.Priority == PiecePriorityNow,
			})
		},
	)
	more := true
	interested := false
	for _, endGameIter := range []bool{false, true} {
		for _, piece := range pieceOrder {
			tp := p.t.piece(piece.index)
			tp.iterUndirtiedChunks(func(cs ChunkSpec) {
				req := Request{pp.Integer(piece.index), cs}
				if !piece.endGame && !endGameIter && p.t.pendingRequests[req] > 0 {
					return
				}
				interested = true
				more = p.setInterested(true)
				if !more {
					return
				}
				if len(p.actualRequestState.Requests) >= p.nominalMaxRequests() {
					return
				}
				if p.peerChoking && !p.peerAllowedFast.Contains(bitmap.BitIndex(req.Index)) {
					return
				}
				var err error
				more, err = p.request(req)
				if err != nil {
					panic(err)
				}
			})
			if interested && len(p.actualRequestState.Requests) >= p.nominalMaxRequests() {
				break
			}
			if !more {
				break
			}
		}
		if !more {
			break
		}
	}
	if !more {
		return false
	}
	if !interested {
		p.setInterested(false)
	}
	return more
}
