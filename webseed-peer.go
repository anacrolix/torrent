package torrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"sync"
	"time"

	"sync/atomic"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/log"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/common"
	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/storage"
	"github.com/anacrolix/torrent/webseed"
)

const (
	webseedPeerUnhandledErrorSleep   = 5 * time.Second
	webseedPeerCloseOnUnhandledError = false
)

type webseedPeer struct {
	// First field for stats alignment.
	peer                 Peer
	client               webseed.Client
	activeRequests       map[Request]webseed.Request
	maxActiveRequests    int // the max number of active requests for this peer
	processedRequests    int // the total number of requests this peer has processed
	maxRequesters        int // the number of requester to run for this peer
	waiting              int // the number of requesters currently waiting for a signal
	receiving            atomic.Int64
	persisting           atomic.Int64
	requesterCond        sync.Cond
	updateRequestor      *time.Timer
	lastUnhandledErr     time.Time
	unchokeTimerDuration time.Duration
	logProcessor         *time.Timer
	requestRateLimiter   *rate.Limiter
}

var _ peerImpl = (*webseedPeer)(nil)

func (me *webseedPeer) peerImplStatusLines(lock bool) []string {
	if lock {
		me.peer.mu.RLock()
		defer me.peer.mu.RUnlock()
	}

	return []string{
		me.client.Url,
		fmt.Sprintf("last unhandled error: %v", eventAgeString(me.lastUnhandledErr)),
	}
}

func (ws *webseedPeer) String() string {
	return fmt.Sprintf("webseed peer for %q", ws.client.Url)
}

func (ws *webseedPeer) onGotInfo(info *metainfo.Info, lock bool, lockTorrent bool) {
	ws.client.SetInfo(info)
	// There should be probably be a callback in Client instead, so it can remove pieces at its whim
	// too.
	ws.client.Pieces.Iterate(func(x uint32) bool {
		ws.peer.t.incPieceAvailability(pieceIndex(x), lockTorrent)
		return true
	})

	ws.peer.updateRequests("info", lock, lockTorrent)
}

func (ws *webseedPeer) writeInterested(interested bool, lock bool) bool {
	return true
}

func (ws *webseedPeer) _cancel(r RequestIndex, lock bool, lockTorrent bool) bool {
	if active, ok := func() (active webseed.Request, ok bool) {
		req := ws.peer.t.requestIndexToRequest(r, lockTorrent)

		if lock {
			ws.peer.mu.RLock()
			defer ws.peer.mu.RUnlock()
		}

		active, ok = ws.activeRequests[req]
		return
	}(); ok {
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

func (ws *webseedPeer) _request(r Request, lock bool) bool {
	return true
}

func (cn *webseedPeer) nominalMaxRequests(lock bool, lockTorrent bool) maxRequests {
	interestedPeers := 1

	if lockTorrent {
		cn.peer.t.mu.RLock()
		defer cn.peer.t.mu.RUnlock()
	}

	seeding := cn.peer.t.seeding(false)

	cn.peer.t.iterPeers(func(peer *Peer) {
		if peer == &cn.peer {
			return
		}

		// note we're ignoring lock as the peer here is not
		// this peer (see above)
		peer.mu.RLock()
		defer peer.mu.RUnlock()

		if !peer.closed.IsSet() {
			if peer.connectionFlags() != "WS" {
				if !peer.peerInterested || peer.lastHelpful(false, seeding).IsZero() {
					return
				}
			}

			interestedPeers++
		}
	}, false)

	if lock {
		cn.peer.mu.RLock()
		defer cn.peer.mu.RUnlock()
	}

	activeRequestsPerPeer := cn.peer.bestPeerNumPieces(false) / maxInt(1, interestedPeers)
	return maxInt(1, minInt(cn.peer.PeerMaxRequests, maxInt(maxInt(8, activeRequestsPerPeer), cn.peer.peakRequests*2)))
}

// TODO this should be config also per client ?
var limitedBuffPool = storage.NewLimitedBufferPool(bufPool, 5_000_000_000)

func (ws *webseedPeer) doRequest(r Request) error {
	webseedRequest := ws.client.NewRequest(ws.intoSpec(r), limitedBuffPool, ws.requestRateLimiter, &ws.receiving)

	ws.peer.mu.Lock()
	ws.activeRequests[r] = webseedRequest
	activeLen := len(ws.activeRequests)

	if activeLen > ws.maxActiveRequests {
		ws.maxActiveRequests = activeLen
	}
	ws.peer.mu.Unlock()

	err := func() error {
		ws.requesterCond.L.Unlock()
		defer ws.requesterCond.L.Lock()
		return ws.requestResultHandler(r, webseedRequest)
	}()

	ws.peer.mu.Lock()
	delete(ws.activeRequests, r)
	ws.peer.mu.Unlock()

	return err
}

func (ws *webseedPeer) requester(i int) {
	ws.requesterCond.L.Lock()
	defer ws.requesterCond.L.Unlock()

	for !ws.peer.closed.IsSet() && i < ws.maxRequesters {
		// Restart is set if we don't need to wait for the requestCond before trying again.
		restart := false

		var request *Request

		func() {
			ws.peer.t.mu.RLock()
			defer ws.peer.t.mu.RUnlock()
			ws.peer.mu.RLock()
			defer ws.peer.mu.RUnlock()

			ws.peer.requestState.Requests.Iterate(func(x RequestIndex) bool {
				r := ws.peer.t.requestIndexToRequest(x, false)

				if _, ok := ws.activeRequests[r]; ok {
					return true
				}

				request = &r
				return false
			})
		}()

		if request != nil {
			// note doRequest unlocks ws.requesterCond.L which free the
			// condition to allow other requestors to receive in parallel it
			// will lock again before it returns so the remainder of the code
			// here can assume it has a lock
			err := ws.doRequest(*request)

			if err == nil {
				ws.processedRequests++
				ws.peer.mu.RLock()
				restart = ws.peer.requestState.Requests.GetCardinality() > 0
				ws.peer.mu.RUnlock()
			} else {
				if !errors.Is(err, context.Canceled) {
					ws.peer.logger.Levelf(log.Debug, "requester %v: error doing webseed request %v: %v", i, request, err)
				}

				func() {
					ws.requesterCond.L.Unlock()
					defer ws.requesterCond.L.Lock()

					if errors.Is(err, webseed.ErrTooFast) {
						time.Sleep(time.Duration(rand.Int63n(int64(10 * time.Second))))
					}
					// Demeter is throwing a tantrum on Mount Olympus for this
					ws.peer.mu.RLock()
					duration := time.Until(ws.lastUnhandledErr.Add(webseedPeerUnhandledErrorSleep))
					ws.peer.mu.RUnlock()
					time.Sleep(duration)
					ws.peer.mu.RLock()
					restart = ws.peer.requestState.Requests.GetCardinality() > 0
					ws.peer.mu.RUnlock()
				}()
			}
		}

		var pendingRequests int

		func() {
			ws.peer.t.mu.RLock()
			defer ws.peer.t.mu.RUnlock()
			ws.peer.mu.RLock()
			defer ws.peer.mu.RUnlock()

			pendingRequests = int(ws.peer.requestState.Requests.GetCardinality())

			if !(ws.peer.t.dataDownloadDisallowed.Bool() || ws.peer.t.info == nil) {
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
		}()

		if restart {
			// if there are more than one requests in the queue and we don't
			// have all of the responders activated yet we need to kick the other requestors
			// into life, otherwise the umber of parallel requests will stay below the max
			// unless some external action happens
			if pendingRequests > 1 {
				ws.peer.mu.RLock()
				activeCount := len(ws.activeRequests)
				ws.peer.mu.RUnlock()

				if activeCount < pendingRequests {
					ws.requesterCond.Broadcast()
				}
			}
		} else {
			func() {
				ws.peer.t.mu.RLock()
				defer ws.peer.t.mu.RUnlock()

				if ws.peer.t.haveInfo(true) && !(ws.peer.t.dataDownloadDisallowed.Bool() || ws.peer.t.Complete.Bool()) {
					if ws.updateRequestor == nil {
						if ws.unchokeTimerDuration == 0 {
							// Don't wait for small files
							if ws.peer.t.NumPieces() > 1 || ws.peer.requestState.Requests.GetCardinality() > 0 {
								ws.unchokeTimerDuration = webpeerUnchokeTimerDuration
							}
						}

						ws.updateRequestor = time.AfterFunc(ws.unchokeTimerDuration, func() { requestUpdate(ws) })
					}
				}
			}()

			ws.peer.mu.Lock()
			if ws.logProcessor == nil {
				ws.logProcessor = time.AfterFunc(5*time.Second, func() {
					defer func() {
						ws.peer.mu.Lock()
						ws.logProcessor = nil
						ws.peer.mu.Unlock()
					}()
					ws.requesterCond.L.Lock()
					defer ws.requesterCond.L.Unlock()
					ws.logProgress("requests", true)
				})
			}
			ws.waiting++
			ws.peer.mu.Unlock()

			ws.requesterCond.Wait()

			ws.peer.mu.Lock()
			ws.waiting--
			ws.peer.mu.Unlock()

			if ws.updateRequestor != nil {
				ws.updateRequestor.Stop()
				ws.updateRequestor = nil
			}

			if i >= ws.maxRequesters && ws.waiting >= ws.maxRequesters {
				// we've been woken by a signal - if we are going to exit
				// instead of processing we need to pass the signal on
				ws.requesterCond.Signal()
			}
		}
	}
}

var webpeerUnchokeTimerDuration = 15 * time.Second

func (ws *webseedPeer) logProgress(label string, lockTorrent bool) {
	t := ws.peer.t

	if lockTorrent {
		t.mu.RLock()
		defer t.mu.RUnlock()
	}

	if !t.haveInfo(true) {
		return
	}

	// do this before taking the peer lock to avoid lock ordering issues
	nominalMaxRequests := ws.nominalMaxRequests(true, false)

	ws.peer.mu.RLock()
	defer ws.peer.mu.RUnlock()

	desiredRequests := ws.peer.desiredRequests(false)
	pendingRequests := ws.peer.uncancelledRequests(false)
	receiving := ws.receiving.Load()
	persisting := ws.persisting.Load()
	activeCount := len(ws.activeRequests)

	ws.peer.logger.Levelf(log.Debug, "%s %d (p=%d,d=%d,n=%d) active(c=%d,r=%d,p=%d,m=%d,w=%d) hashing(q=%d,a=%d,h=%d,r=%d) complete(%d/%d) lastUpdate=%s",
		label, ws.processedRequests, pendingRequests, desiredRequests, nominalMaxRequests,
		activeCount-int(receiving)-int(persisting), receiving, persisting, ws.maxActiveRequests, ws.waiting,
		t.numQueuedForHash(false), t.activePieceHashes.Load(), t.hashing.Load(), len(t.hashResults),
		t.numPiecesCompleted(false), t.NumPieces(), time.Since(ws.peer.lastRequestUpdate))
}

func requestUpdate(ws *webseedPeer) {
	if ws != nil {
		ws.requesterCond.L.Lock()
		defer ws.requesterCond.L.Unlock()
		ws.peer.t.mu.RLock()
		defer ws.peer.t.mu.RUnlock()

		ws.updateRequestor = nil

		ws.logProgress("requestUpdate", false)

		if !ws.peer.closed.IsSet() {
			numPieces := ws.peer.t.NumPieces()
			numCompleted := ws.peer.t.numPiecesCompleted(false)

			if numCompleted < numPieces {
				// Don't wait for small files
				if ws.peer.isLowOnRequests(true, false) && (numPieces == 1 || time.Since(ws.peer.lastRequestUpdate) > webpeerUnchokeTimerDuration) {
					// if the number of incomplete pieces is less than five adjust this peers
					// lastUsefulChunkReceived to ensure that it can steal from non web peers
					// this is to help ensure completion - we may want to add a head call
					// before doing this to ensure the peer has the file
					if numPieces-numCompleted < 16 {
						lastExistingUseful := ws.peer.lastUsefulChunkReceived

						for piece := pieceIndex(0); piece < pieceIndex(numPieces); piece++ {
							if ws.peer.t.pieceComplete(piece, false) {
								continue
							}

							if existing := ws.peer.t.requestingPeer(RequestIndex(piece), false); existing != nil {
								if existing.connectionFlags() == "WS" {
									continue
								}

								ws.peer.logger.Levelf(log.Debug, "update request chunk received: %s, %s", time.Since(existing.lastUsefulChunkReceived), webpeerUnchokeTimerDuration)

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

					peerInfo := []string{}
					ws.peer.t.iterPeers(func(p *Peer) {
						p.mu.RLock()
						defer p.mu.RUnlock()

						rate := p.downloadRate()
						pieces := p.uncancelledRequests(false)
						desired := p.desiredRequests(false)

						this := ""
						if p == &ws.peer {
							this = "*"
						}
						flags := p.connectionFlags()
						peerInfo = append(peerInfo, fmt.Sprintf("%s%s:p=%d,d=%d: %f", this, flags, pieces, desired, rate))

					}, false)

					ws.peer.logger.Levelf(log.Debug, "unchoke processed=%d, complete(%d/%d) maxRequesters=%d, waiting=%d, (%s): peers(%d): %v",
						ws.processedRequests, numCompleted, numPieces, ws.maxRequesters,
						ws.waiting, time.Since(ws.peer.lastUsefulChunkReceived), len(peerInfo), peerInfo)

					func() {
						// update requests may require a write lock on the
						// torrent so free our read lock first
						ws.peer.t.mu.RUnlock()
						defer ws.peer.t.mu.RLock()
						ws.peer.updateRequests("unchoked", true, true)
					}()

					ws.logProgress("unchoked", false)

					// if the initial unchoke didn't yield a request (for small files) - don't immediately
					// retry it means its being handled by a prioritized peer
					if ws.unchokeTimerDuration == 0 && int(ws.peer.requestState.Requests.GetCardinality()) == 0 {
						ws.unchokeTimerDuration = webpeerUnchokeTimerDuration
					}

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

func (ws *webseedPeer) drop(lock bool, lockTorrent bool) {
	ws.peer.cancelAllRequests(lockTorrent)
}

func (cn *webseedPeer) ban() {
	cn.peer.drop(true, true)
}

func (cn *webseedPeer) isLowOnRequests(lock bool, lockTorrent bool) bool {
	// do this before taking the peer lock to avoid lock ordering issues
	nominalMaxRequests := cn.nominalMaxRequests(lock, lockTorrent)

	if lock {
		cn.peer.mu.RLock()
		defer cn.peer.mu.RUnlock()
	}

	return cn.peer.requestState.Requests.IsEmpty() && cn.peer.requestState.Cancelled.IsEmpty() ||
		len(cn.activeRequests) <= nominalMaxRequests
}

func (ws *webseedPeer) handleUpdateRequests(lock bool, lockTorrent bool) {
	// Because this is synchronous, webseed peers seem to get first dibs on newly prioritized
	// pieces.
	ws.peer.maybeUpdateActualRequestState(lock, lockTorrent)
	ws.requesterCond.Signal()
}

func (ws *webseedPeer) onClose(lockTorrent bool) {
	ws.peer.logger.Levelf(log.Debug, "closing")
	// Just deleting them means we would have to manually cancel active requests.
	ws.peer.cancelAllRequests(lockTorrent)
	ws.peer.t.iterPeers(func(p *Peer) {
		if p.isLowOnRequests(true, lockTorrent) {
			p.updateRequests("webseedPeer.onClose", true, lockTorrent)
		}
	}, true)
	ws.requesterCond.Broadcast()
}

func (ws *webseedPeer) requestResultHandler(r Request, webseedRequest webseed.Request) error {
	result := <-webseedRequest.Result
	close(webseedRequest.Result) // one-shot

	defer func() {
		for _, reader := range result.Readers {
			reader.Close()
		}
	}()

	ws.persisting.Add(1)
	defer ws.persisting.Add(-1)

	var piece []byte

	if len(result.Readers) == 1 {
		if buff, ok := result.Readers[0].(interface{ Bytes() []byte }); ok {
			piece = buff.Bytes()
		}
	}

	if piece == nil && len(result.Readers) > 0 {
		piece, _ = io.ReadAll(common.MultiReadCloser(result.Readers...))
	}

	// We do this here rather than inside receiveChunk, since we want to count errors too. I'm not
	// sure if we can divine which errors indicate cancellation on our end without hitting the
	// network though.
	if len(piece) != 0 || result.Err == nil {
		// Increment ChunksRead and friends
		ws.peer.doChunkReadStats(int64(len(piece)))
	}
	ws.peer.readBytes(int64(len(piece)))

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
				ws.peer.close(true, true)
			} else {
				ws.peer.mu.Lock()
				ws.lastUnhandledErr = time.Now()
				ws.peer.mu.Unlock()
			}
		}

		index := ws.peer.t.requestIndexFromRequest(r, true)

		if !ws.peer.remoteRejectedRequest(index) {
			panic(fmt.Sprintf(`invalid reject "%s" for: %d`, err, index))
		}

		return err
	}

	// the call may have been cancelled while it
	// was in transit (via the chan)
	if result.Ctx.Err() != nil {
		ws.peer.remoteRejectedRequest(ws.peer.t.requestIndexFromRequest(r, true))
		return result.Ctx.Err()
	}

	fmt.Println("Received piece:", r.Index, "len", len(piece), "from:", ws.peer.String())

	err = ws.peer.receiveChunk(&pp.Message{
		Type:  pp.Piece,
		Index: r.Index,
		Begin: r.Begin,
		Piece: piece,
	})

	if err != nil {
		panic(err)
	}

	return err
}

func (me *webseedPeer) peerPieces(lock bool) *roaring.Bitmap {
	if lock {
		me.peer.mu.RLock()
		defer me.peer.mu.RUnlock()
	}

	return &me.client.Pieces
}

func (cn *webseedPeer) peerHasAllPieces(lock bool, lockTorrentInfo bool) (all, known bool) {
	if !cn.peer.t.haveInfo(lockTorrentInfo) {
		return true, false
	}

	if lock {
		cn.peer.mu.RLock()
		defer cn.peer.mu.RUnlock()
	}

	return cn.client.Pieces.GetCardinality() == uint64(cn.peer.t.numPieces(lockTorrentInfo)), true
}
