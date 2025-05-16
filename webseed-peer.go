package torrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/RoaringBitmap/roaring"
	g "github.com/anacrolix/generics"

	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/webseed"
)

type webseedPeer struct {
	// First field for stats alignment.
	peer             Peer
	client           webseed.Client
	activeRequests   map[*webseedRequest]struct{}
	locker           sync.Locker
	lastUnhandledErr time.Time
}

func (me *webseedPeer) nominalMaxRequests() maxRequests {
	// TODO: Implement an algorithm that assigns this based on sharing chunks across peers. For now
	// we just allow 2 MiB worth of requests.
	return intCeilDiv(2<<20, me.peer.t.chunkSize.Int())
}

func (me *webseedPeer) acksCancels() bool {
	return false
}

func (me *webseedPeer) updateRequests() {
	p := &me.peer
	next := p.getDesiredRequestState()
	p.applyRequestState(next)
	p.t.cacheNextRequestIndexesForReuse(next.Requests.requestIndexes)
	// Run this after all requests applied to the peer, so they can be batched up.
	me.spawnRequests()
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

func (ws *webseedPeer) handleCancel(r RequestIndex) {
	for wr := range ws.activeRequestsForIndex(r) {
		wr.request.Cancel()
	}
}

func (ws *webseedPeer) activeRequestsForIndex(r RequestIndex) iter.Seq[*webseedRequest] {
	return func(yield func(*webseedRequest) bool) {
		for wr := range ws.activeRequests {
			if r < wr.next || r >= wr.end {
				continue
			}
			if !yield(wr) {
				return
			}
		}
	}
}

func (ws *webseedPeer) requestIndexTorrentOffset(r RequestIndex) int64 {
	return ws.peer.t.requestIndexBegin(r)
}

func (ws *webseedPeer) intoSpec(begin, end RequestIndex) webseed.RequestSpec {
	t := ws.peer.t
	start := t.requestIndexBegin(begin)
	endOff := t.requestIndexEnd(end - 1)
	return webseed.RequestSpec{start, endOff - start}
}

func (ws *webseedPeer) _request(r Request) bool {
	return true
}

func (ws *webseedPeer) spawnRequest(begin, end RequestIndex) {
	extWsReq := ws.client.StartNewRequest(ws.intoSpec(begin, end))
	wsReq := webseedRequest{
		request: extWsReq,
		begin:   begin,
		next:    begin,
		end:     end,
	}
	ws.activeRequests[&wsReq] = struct{}{}
	ws.peer.logger.Slogger().Debug(
		"starting webseed request",
		"begin", begin,
		"end", end,
		"len", end-begin,
		"avail", ws.peer.requestState.Requests.GetCardinality())
	go ws.runRequest(&wsReq)
}

func (ws *webseedPeer) runRequest(webseedRequest *webseedRequest) {
	locker := ws.locker
	err := ws.readChunks(webseedRequest)
	// Ensure the body reader and response are closed.
	webseedRequest.request.Cancel()
	locker.Lock()
	if err != nil {
		level := slog.LevelWarn
		if errors.Is(err, context.Canceled) {
			level = slog.LevelDebug
		} else {
			panic(err)
		}
		ws.slogger().Log(context.TODO(), level, "webseed request error", "err", err)
		//	// This used to occur only on webseed.ErrTooFast but I think it makes sense to slow down any
		//	// kind of error. There are maxRequests (in Torrent.addWebSeed) requesters bouncing around
		//	// it doesn't hurt to slow a few down if there are issues.
		//	select {
		//	case <-ws.peer.closed.Done():
		//	case <-time.After(time.Duration(rand.Int63n(int64(10 * time.Second)))):
		//	}
	}
	//locker.Lock()
	// Delete this entry after waiting above on an error, to prevent more requests.
	delete(ws.activeRequests, webseedRequest)
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
		end := seqLast(ws.iterConsecutiveInactiveRequests(req)).Unwrap()
		ws.spawnRequest(req, end+1)
	}
}

// Returns Some of the last item in a iter.Seq, or None if the sequence is empty.
func seqLast[V any](seq iter.Seq[V]) (last g.Option[V]) {
	for item := range seq {
		last.Set(item)
	}
	return
}

func (ws *webseedPeer) iterConsecutiveRequests(begin RequestIndex) iter.Seq[RequestIndex] {
	return func(yield func(RequestIndex) bool) {
		for {
			if !ws.peer.requestState.Requests.Contains(begin) {
				return
			}
			if !yield(begin) {
				return
			}
			begin++
		}
	}
}

func (ws *webseedPeer) iterConsecutiveInactiveRequests(begin RequestIndex) iter.Seq[RequestIndex] {
	return func(yield func(RequestIndex) bool) {
		for req := range ws.iterConsecutiveRequests(begin) {
			if !ws.inactiveRequestIndex(req) {
				return
			}
			if !yield(req) {
				return
			}
		}
	}
}

func (ws *webseedPeer) inactiveRequestIndex(index RequestIndex) bool {
	for range ws.activeRequestsForIndex(index) {
		return false
	}
	return true
}

func (ws *webseedPeer) inactiveRequests() iter.Seq[RequestIndex] {
	return func(yield func(RequestIndex) bool) {
		sorted := slices.Sorted(ws.peer.requestState.Requests.Iterator())
		if len(sorted) != 0 {
			fmt.Println("inactiveRequests", sorted)
		}
		for reqIndex := range ws.peer.requestState.Requests.Iterator() {
			if !ws.inactiveRequestIndex(reqIndex) {
				continue
			}
			if !yield(reqIndex) {
				return
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
	// Just deleting them means we would have to manually cancel active requests.
	ws.peer.cancelAllRequests()
	ws.peer.t.iterPeers(func(p *Peer) {
		if p.isLowOnRequests() {
			p.onNeedUpdateRequests("webseedPeer.onClose")
		}
	})
}

func (ws *webseedPeer) readChunks(wr *webseedRequest) (err error) {
	t := ws.peer.t
	buf := t.getChunkBuffer()
	defer t.putChunkBuffer(buf)
	for ; wr.next < wr.end; wr.next++ {
		reqSpec := t.requestIndexToRequest(wr.next)
		chunkLen := reqSpec.Length.Int()
		buf = buf[:chunkLen]
		var n int
		n, err = io.ReadFull(wr.request.Body, buf)
		ws.peer.readBytes(int64(n))
		if err != nil {
			err = fmt.Errorf("reading chunk: %w", err)
			return
		}
		ws.peer.doChunkReadStats(int64(chunkLen))
		ws.peer.locker().Lock()
		err = ws.peer.receiveChunk(&pp.Message{
			Type:  pp.Piece,
			Piece: buf,
			Index: reqSpec.Index,
			Begin: reqSpec.Begin,
		})
		ws.peer.locker().Unlock()
		if err != nil {
			err = fmt.Errorf("processing chunk: %w", err)
			return
		}
	}
	return
}

//
//func (ws *webseedPeer) requestResultHandler(wr *webseedRequest) (err error) {
//	err = ws.readChunks(wr)
//	switch {
//	case err == nil:
//	case ws.peer.closed.IsSet():
//	case errors.Is(err, context.Canceled):
//	case errors.Is(err, webseed.ErrTooFast):
//	default:
//
//	}
//	ws.peer.t.cl.lock()
//	defer ws.peer.t.cl.unlock()
//	if ws.peer.t.closed.IsSet() {
//		return nil
//	}
//	if err != nil {
//		switch {
//		case errors.Is(err, context.Canceled):
//		case errors.Is(err, webseed.ErrTooFast):
//		case ws.peer.closed.IsSet():
//		default:
//			ws.peer.logger.Printf("Request %v rejected: %v", r, result.Err)
//			// // Here lies my attempt to extract something concrete from Go's error system. RIP.
//			// cfg := spew.NewDefaultConfig()
//			// cfg.DisableMethods = true
//			// cfg.Dump(result.Err)
//
//			if webseedPeerCloseOnUnhandledError {
//				log.Printf("closing %v", ws)
//				ws.peer.close()
//			} else {
//				ws.lastUnhandledErr = time.Now()
//			}
//		}
//		if !ws.peer.remoteRejectedRequest(ws.peer.t.requestIndexFromRequest(r)) {
//			panic("invalid reject")
//		}
//		return err
//	}
//	return err
//}

func (me *webseedPeer) peerPieces() *roaring.Bitmap {
	return &me.client.Pieces
}

func (cn *webseedPeer) peerHasAllPieces() (all, known bool) {
	if !cn.peer.t.haveInfo() {
		return true, false
	}
	return cn.client.Pieces.GetCardinality() == uint64(cn.peer.t.numPieces()), true
}

func (me *webseedPeer) slogger() *slog.Logger {
	return me.peer.logger.Slogger()
}
