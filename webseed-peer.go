package torrent

import (
	"context"
	"errors"
	"fmt"
	"iter"
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
	webseedPeerCloseOnUnhandledError = false
)

type webseedPeer struct {
	// First field for stats alignment.
	peer             Peer
	client           webseed.Client
	activeRequests   map[Request]webseed.Request
	locker           sync.Locker
	lastUnhandledErr time.Time
}

func (me *webseedPeer) updateRequests() {
	//TODO implement me
	panic("implement me")
}

func (me *webseedPeer) lastWriteUploadRate() float64 {
	// We never upload to webseeds.
	return 0
}

var _ legacyPeerImpl = (*webseedPeer)(nil)

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
	return webseed.RequestSpec{ws.peer.t.requestOffset(r), int64(r.Length)}
}

func (ws *webseedPeer) _request(r Request) bool {
	ws.spawnRequests()
	return true
}

func (ws *webseedPeer) spawnReq(r Request) {
	webseedRequest := ws.client.StartNewRequest(ws.intoSpec(r))
	ws.activeRequests[r] = webseedRequest
	go ws.doRequest(r, webseedRequest)
}

func (ws *webseedPeer) doRequest(r Request, webseedRequest webseed.Request) {
	locker := ws.locker
	err := ws.requestResultHandler(r, webseedRequest)
	if err != nil {
		level := log.Warning
		if errors.Is(err, context.Canceled) {
			level = log.Debug
		}
		ws.peer.logger.Levelf(level, "error doing webseed request %v: %v", r, err)
		// This used to occur only on webseed.ErrTooFast but I think it makes sense to slow down any
		// kind of error. There are maxRequests (in Torrent.addWebSeed) requesters bouncing around
		// it doesn't hurt to slow a few down if there are issues.
		select {
		case <-ws.peer.closed.Done():
		case <-time.After(time.Duration(rand.Int63n(int64(10 * time.Second)))):
		}
	}
	locker.Lock()
	// Delete this entry after waiting above on an error, to prevent more requests.
	delete(ws.activeRequests, r)
	if err != nil {
		ws.peer.onNeedUpdateRequests("webseedPeer request errored")
	}
	ws.spawnRequests()
	locker.Unlock()
}

func (ws *webseedPeer) spawnRequests() {
	next, stop := iter.Pull(ws.inactiveRequests())
	defer stop()
	for len(ws.activeRequests) <= ws.client.MaxRequests {
		req, ok := next()
		if !ok {
			break
		}
		ws.spawnReq(req)
	}
}

func (ws *webseedPeer) inactiveRequests() iter.Seq[Request] {
	return func(yield func(Request) bool) {
		for reqIndex := range ws.peer.requestState.Requests.Iterator() {
			r := ws.peer.t.requestIndexToRequest(reqIndex)
			_, ok := ws.activeRequests[r]
			if !ok {
				if !yield(r) {
					return
				}
			}
		}
	}
}

func (ws *webseedPeer) connectionFlags() string {
	return "WS"
}

// Maybe this should drop all existing connections, or something like that.
func (ws *webseedPeer) drop() {}

func (cn *webseedPeer) ban() {
	cn.peer.close()
}

func (ws *webseedPeer) handleOnNeedUpdateRequests() {
	// Because this is synchronous, webseed peers seem to get first dibs on newly prioritized
	// pieces.
	go func() {
		ws.peer.t.cl.lock()
		defer ws.peer.t.cl.unlock()
		ws.peer.maybeUpdateActualRequestState()
	}()
}

func (ws *webseedPeer) onClose() {
	ws.peer.logger.Levelf(log.Debug, "closing")
	// Just deleting them means we would have to manually cancel active requests.
	ws.peer.cancelAllRequests()
	ws.peer.t.iterPeers(func(p *Peer) {
		if p.isLowOnRequests() {
			p.onNeedUpdateRequests("webseedPeer.onClose")
		}
	})
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
				log.Printf("closing %v", ws)
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
