package torrent

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/log"

	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/webseed"
)

const (
	webseedPeerUnhandledErrorSleep   = 5 * time.Second
	webseedPeerCloseOnUnhandledError = false
)

type webseedPeer struct {
	// First field for stats alignment.
	peer              Peer
	client            webseed.Client
	activeRequests    map[Request]webseed.Request
	maxActiveRequests int // the max number of active requests for this peer
	processedRequests int // the total number of requests this peer has processed
	maxRequesters     int // the number of requester to run for this peer
	waiting           int // the number of requesters currently waiting for a signal
	requesterCond     sync.Cond
	updateRequestor   *time.Timer
	lastUnhandledErr  time.Time
}

var _ peerImpl = (*webseedPeer)(nil)

func (me *webseedPeer) peerImplStatusLines() []string {
	return []string{
		me.client.Url,
		fmt.Sprintf("last unhandled error: %v", eventAgeString(me.lastUnhandledErr)),
	}
}

func (ws *webseedPeer) String() string {
	return fmt.Sprintf("webseed peer for %q", ws.client.Url)
}

func (ws *webseedPeer) onGotInfo(info *metainfo.Info) {
	ws.client.SetInfo(info)
	// There should be probably be a callback in Client instead, so it can remove pieces at its whim
	// too.
	ws.client.Pieces.Iterate(func(x uint32) bool {
		ws.peer.t.incPieceAvailability(pieceIndex(x))
		return true
	})
}

func (ws *webseedPeer) writeInterested(interested bool) bool {
	return true
}

func (ws *webseedPeer) _cancel(r RequestIndex) bool {
	if active, ok := ws.activeRequests[ws.peer.t.requestIndexToRequest(r)]; ok {
		active.Cancel()
		// The requester is running and will handle the result.
		return true
	}
	// There should be no requester handling this, so no further events will occur.
	return false
}

func (ws *webseedPeer) intoSpec(r Request) webseed.RequestSpec {
	return webseed.RequestSpec{Start: ws.peer.t.requestOffset(r), Length: int64(r.Length)}
}

func (ws *webseedPeer) _request(r Request) bool {
	return true
}

func (cn *webseedPeer) nominalMaxRequests() maxRequests {
	interestedPeers := 1

	cn.peer.t.iterPeers(func(peer *Peer) {
		if peer == &cn.peer {
			return
		}

		if !peer.closed.IsSet() {
			if peer.connectionFlags() != "WS" {
				if !peer.peerInterested || peer.lastHelpful().IsZero() {
					return
				}
			}

			interestedPeers++
		}
	})

	activeRequestsPerPeer := cn.peer.bestPeerNumPieces() / maxInt(1, interestedPeers)
	return maxInt(1, minInt(cn.peer.PeerMaxRequests, maxInt(maxInt(8, activeRequestsPerPeer), cn.peer.peakRequests*2)))
}

func (ws *webseedPeer) doRequest(r Request) error {
	webseedRequest := ws.client.NewRequest(ws.intoSpec(r))
	ws.activeRequests[r] = webseedRequest
	if activeLen := len(ws.activeRequests); activeLen > ws.maxActiveRequests {
		ws.maxActiveRequests = activeLen
	}
	err := func() error {
		ws.requesterCond.L.Unlock()
		defer ws.requesterCond.L.Lock()
		return ws.requestResultHandler(r, webseedRequest)
	}()
	delete(ws.activeRequests, r)
	return err
}

func (ws *webseedPeer) requester(i int) {
	ws.requesterCond.L.Lock()
	defer ws.requesterCond.L.Unlock()

	for !ws.peer.closed.IsSet() && i < ws.maxRequesters {
		// Restart is set if we don't need to wait for the requestCond before trying again.
		restart := false

		ws.peer.requestState.Requests.Iterate(func(x RequestIndex) bool {
			r := ws.peer.t.requestIndexToRequest(x)
			if _, ok := ws.activeRequests[r]; ok {
				return true
			}

			// note doRequest unlocks ws.requesterCond.L which free the
			// condition to allow other requestors to receive in parallel it
			// will lock again before it returns so the remainder of the code
			// here can assume it has a lock
			err := ws.doRequest(r)

			if err == nil {
				ws.processedRequests++
				restart = ws.peer.requestState.Requests.GetCardinality() > 0
				return false
			}

			if !errors.Is(err, context.Canceled) {
				ws.peer.logger.Levelf(log.Debug, "requester %v: error doing webseed request %v: %v", i, r, err)
			}

			ws.requesterCond.L.Unlock()
			if errors.Is(err, webseed.ErrTooFast) {
				time.Sleep(time.Duration(rand.Int63n(int64(10 * time.Second))))
			}
			// Demeter is throwing a tantrum on Mount Olympus for this
			ws.peer.t.cl.locker().RLock()
			duration := time.Until(ws.lastUnhandledErr.Add(webseedPeerUnhandledErrorSleep))
			ws.peer.t.cl.locker().RUnlock()
			time.Sleep(duration)
			ws.requesterCond.L.Lock()
			restart = ws.peer.requestState.Requests.GetCardinality() > 0
			return false
		})

		if !(ws.peer.t.dataDownloadDisallowed.Bool() || ws.peer.t.info == nil) {
			desiredRequests := len(ws.peer.getDesiredRequestState().Requests.requestIndexes)
			pendingRequests := int(ws.peer.requestState.Requests.GetCardinality())

			ws.peer.logger.Levelf(log.Debug, "%d: requests %d (p=%d,d=%d,n=%d) active(c=%d,m=%d,w=%d) complete(%d/%d) restart(%v)",
				i, ws.processedRequests, pendingRequests, desiredRequests, ws.nominalMaxRequests(),
				len(ws.activeRequests), ws.maxActiveRequests, ws.waiting, ws.peer.t.numPiecesCompleted(), ws.peer.t.NumPieces(), restart)

			if pendingRequests > ws.maxRequesters {
				if pendingRequests > ws.peer.PeerMaxRequests {
					pendingRequests = ws.peer.PeerMaxRequests
				}

				for i := ws.maxRequesters; i < pendingRequests; i++ {
					go ws.requester(i)
					ws.maxRequesters++
				}
			} else {
				if pendingRequests < 16 {
					pendingRequests = 16
				}

				if ws.maxRequesters != pendingRequests {
					ws.maxRequesters = pendingRequests
					ws.requesterCond.Broadcast()
				}
			}

		}

		if !restart {
			if !(ws.peer.t.dataDownloadDisallowed.Bool() || ws.peer.t.Complete.Bool()) {
				if ws.updateRequestor == nil {
					timerDuration := webpeerUnchokeTimerDuration

					// Don't wait for small files
					if ws.peer.t.NumPieces() == 1 && ws.peer.requestState.Requests.GetCardinality() == 0 {
						timerDuration = 0
					}

					ws.updateRequestor = time.AfterFunc(timerDuration, func() { requestUpdate(ws) })
				}
			}

			ws.waiting++
			ws.requesterCond.Wait()
			ws.waiting--

			if ws.updateRequestor != nil {
				ws.updateRequestor.Stop()
				ws.updateRequestor = nil
			}
		} else {
			// if there are more than one requests in the queue and we don't
			// have all of the responders activated yet we need to
			// kick the other requestors into life - otherwise the max parallel
			// requests will stay below the max  - unless some external action happens
			if pendingRequests := int(ws.peer.requestState.Requests.GetCardinality()); pendingRequests > 1 {
				activeCount := len(ws.activeRequests)

				if activeCount < pendingRequests {
					ws.requesterCond.Broadcast()
				}
			}
		}
	}
}

var webpeerUnchokeTimerDuration = 15 * time.Second

func requestUpdate(ws *webseedPeer) {
	if ws != nil {
		ws.requesterCond.L.Lock()
		defer ws.requesterCond.L.Unlock()

		ws.updateRequestor = nil

		ws.peer.logger.Levelf(log.Debug, "requestUpdate %d (p=%d,d=%d,n=%d) active(c=%d,m=%d,w=%d) complete(%d/%d)",
			ws.processedRequests, int(ws.peer.requestState.Requests.GetCardinality()), len(ws.peer.getDesiredRequestState().Requests.requestIndexes),
			ws.nominalMaxRequests(), len(ws.activeRequests), ws.maxActiveRequests, ws.waiting, ws.peer.t.numPiecesCompleted(), ws.peer.t.NumPieces())

		if !ws.peer.closed.IsSet() {
			numPieces := uint64(ws.peer.t.NumPieces())
			numCompleted := ws.peer.t._completedPieces.GetCardinality()

			if numCompleted < numPieces {
				// Don't wait for small files
				if ws.peer.isLowOnRequests() && (numPieces == 1 || time.Since(ws.peer.lastRequestUpdate) > webpeerUnchokeTimerDuration) {
					// if the number of incomplete pieces is less than five adjust this peers
					// lastUsefulChunkReceived to ensure that it can steal from non web peers
					// this is to help ensure completion - we may want to add a head call
					// before doing this to ensure the peer has the file
					if numPieces-numCompleted < 16 {
						lastExistingUseful := ws.peer.lastUsefulChunkReceived

						for piece := pieceIndex(0); piece < pieceIndex(numPieces); piece++ {
							if ws.peer.t._completedPieces.Contains(uint32(piece)) {
								continue
							}

							if existing := ws.peer.t.requestingPeer(RequestIndex(piece)); existing != nil {
								if existing.connectionFlags() == "WS" {
									continue
								}

								// if the existing client looks like its not producing timely chunks then
								// adjust our lastUsefulChunkReceived value to make sure we can steal the
								// piece from it
								if time.Since(existing.lastUsefulChunkReceived) > webpeerUnchokeTimerDuration {
									if !lastExistingUseful.After(existing.lastUsefulChunkReceived) {
										lastExistingUseful = existing.lastUsefulChunkReceived.Add(time.Minute)
									}
								}
							}
						}

						ws.peer.lastUsefulChunkReceived = lastExistingUseful
					}

					ws.peer.logger.Levelf(log.Debug, "unchoke %d/%d maxRequesters=%d, waiting=%d, (%s)", ws.processedRequests, ws.peer.t.NumPieces(), ws.maxRequesters, ws.waiting, ws.peer.lastUsefulChunkReceived)

					ws.peer.updateRequests("unchoked")
					return
				}
			}

			ws.requesterCond.Signal()
		}
	}
}

func (ws *webseedPeer) connectionFlags() string {
	return "WS"
}

func (ws *webseedPeer) drop() {
	ws.peer.cancelAllRequests()
}

func (cn *webseedPeer) ban() {
	cn.peer.drop()
}

func (ws *webseedPeer) handleUpdateRequests() {
	// Because this is synchronous, webseed peers seem to get first dibs on newly prioritized
	// pieces.
	go func() {
		ws.peer.t.cl.lock()
		defer ws.peer.t.cl.unlock()
		ws.peer.maybeUpdateActualRequestState()
		ws.requesterCond.Signal()
	}()
}

func (ws *webseedPeer) onClose() {
	ws.peer.logger.Levelf(log.Debug, "closing")
	// Just deleting them means we would have to manually cancel active requests.
	ws.peer.cancelAllRequests()
	ws.peer.t.iterPeers(func(p *Peer) {
		if p.isLowOnRequests() {
			p.updateRequests("webseedPeer.onClose")
		}
	})
	ws.requesterCond.Broadcast()
}

func (ws *webseedPeer) requestResultHandler(r Request, webseedRequest webseed.Request) error {
	result := <-webseedRequest.Result
	close(webseedRequest.Result) // one-shot
	// We do this here rather than inside receiveChunk, since we want to count errors too. I'm not
	// sure if we can divine which errors indicate cancellation on our end without hitting the
	// network though.
	if len(result.Bytes) != 0 || result.Err == nil {
		// Increment ChunksRead and friends
		ws.peer.doChunkReadStats(int64(len(result.Bytes)))
	}
	ws.peer.readBytes(int64(len(result.Bytes)))
	ws.peer.t.cl.lock()
	defer ws.peer.t.cl.unlock()
	if ws.peer.t.closed.IsSet() {
		return nil
	}
	err := result.Err
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
		case errors.Is(err, webseed.ErrTooFast):
		case ws.peer.closed.IsSet():
		default:
			ws.peer.logger.Printf("Request %v rejected: %v", r, result.Err)
			// // Here lies my attempt to extract something concrete from Go's error system. RIP.
			// cfg := spew.NewDefaultConfig()
			// cfg.DisableMethods = true
			// cfg.Dump(result.Err)

			if webseedPeerCloseOnUnhandledError {
				log.Levelf(log.Debug, "closing %v", ws)
				ws.peer.close()
			} else {
				ws.lastUnhandledErr = time.Now()
			}
		}
		if !ws.peer.remoteRejectedRequest(ws.peer.t.requestIndexFromRequest(r)) {
			panic("invalid reject")
		}
		return err
	}

	err = ws.peer.receiveChunk(&pp.Message{
		Type:  pp.Piece,
		Index: r.Index,
		Begin: r.Begin,
		Piece: result.Bytes,
	})
	if err != nil {
		panic(err)
	}
	return err
}

func (me *webseedPeer) peerPieces() *roaring.Bitmap {
	return &me.client.Pieces
}

func (cn *webseedPeer) peerHasAllPieces() (all, known bool) {
	if !cn.peer.t.haveInfo() {
		return true, false
	}
	return cn.client.Pieces.GetCardinality() == uint64(cn.peer.t.numPieces()), true
}
