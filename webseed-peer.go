package torrent

import (
	"context"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"math/rand"
	"sync"
	"time"

	"github.com/RoaringBitmap/roaring"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"

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
	hostKey          webseedHostKeyHandle
}

func (me *webseedPeer) isLowOnRequests() bool {
	// Updates globally instead.
	return false
}

// Webseed requests are issued globally so per-connection reasons or handling make no sense.
func (me *webseedPeer) onNeedUpdateRequests(updateRequestReason) {}

func (me *webseedPeer) expectingChunks() bool {
	return len(me.activeRequests) > 0
}

func (me *webseedPeer) checkReceivedChunk(RequestIndex, *pp.Message, Request) (bool, error) {
	return true, nil
}

func (me *webseedPeer) numRequests() int {
	// What about unassigned requests? TODO: Don't allow those.
	return len(me.activeRequests)
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

// Webseeds check the next request is wanted before reading it.
func (ws *webseedPeer) handleCancel(RequestIndex) {}

func (ws *webseedPeer) activeRequestsForIndex(r RequestIndex) iter.Seq[*webseedRequest] {
	return func(yield func(*webseedRequest) bool) {
		for wr := range ws.activeRequests {
			if r >= wr.next && r < wr.end {
				if !yield(wr) {
					return
				}
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

func (ws *webseedPeer) spawnRequest(begin, end RequestIndex) {
	extWsReq := ws.client.StartNewRequest(ws.intoSpec(begin, end))
	wsReq := webseedRequest{
		request: extWsReq,
		begin:   begin,
		next:    begin,
		end:     end,
	}
	ws.activeRequests[&wsReq] = struct{}{}
	ws.peer.updateExpectingChunks()
	panicif.Zero(ws.hostKey)
	ws.peer.t.cl.numWebSeedRequests[ws.hostKey]++
	ws.slogger().Debug(
		"starting webseed request",
		"begin", begin,
		"end", end,
		"len", end-begin,
	)
	go ws.runRequest(&wsReq)
}

func (ws *webseedPeer) runRequest(webseedRequest *webseedRequest) {
	locker := ws.locker
	err := ws.readChunks(webseedRequest)
	if webseed.PrintDebug && webseedRequest.next < webseedRequest.end {
		fmt.Printf("webseed peer stopped reading chunks early\n")
	}
	// Ensure the body reader and response are closed.
	webseedRequest.Close()
	if err != nil {
		level := slog.LevelInfo
		if webseedRequest.cancelled {
			level = slog.LevelDebug
		}
		ws.slogger().Log(context.TODO(), level, "webseed request error", "err", err)
		torrent.Add("webseed request error count", 1)
		// This used to occur only on webseed.ErrTooFast but I think it makes sense to slow down any
		// kind of error. Pausing here will starve the available requester slots which slows things
		// down.
		select {
		case <-ws.peer.closed.Done():
		case <-time.After(time.Duration(rand.Int63n(int64(10 * time.Second)))):
		}
	}
	ws.slogger().Debug("webseed request ended")
	locker.Lock()
	// Delete this entry after waiting above on an error, to prevent more requests.
	ws.deleteActiveRequest(webseedRequest)
	if err != nil {
		ws.peer.onNeedUpdateRequests("webseedPeer request errored")
	}
	ws.peer.t.cl.updateWebseedRequestsWithReason("webseedPeer request completed")
	locker.Unlock()
}

func (ws *webseedPeer) deleteActiveRequest(wr *webseedRequest) {
	g.MustDelete(ws.activeRequests, wr)
	ws.peer.t.cl.numWebSeedRequests[ws.hostKey]--
	ws.peer.updateExpectingChunks()
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
		// This is used to determine contiguity of requests.
		//sorted := slices.Sorted(ws.peer.requestState.Requests.Iterator())
		//if len(sorted) != 0 {
		//	fmt.Println("inactiveRequests", sorted)
		//}
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

func (ws *webseedPeer) onClose() {
	ws.peer.t.iterPeers(func(p *Peer) {
		if p.isLowOnRequests() {
			p.onNeedUpdateRequests("webseedPeer.onClose")
		}
	})
}

// Do we want a chunk, assuming it's valid etc.
func (ws *webseedPeer) wantChunk(ri RequestIndex) bool {
	return ws.peer.t.wantReceiveChunk(ri)
}

func (ws *webseedPeer) maxChunkDiscard() RequestIndex {
	return RequestIndex(int(intCeilDiv(webseed.MaxDiscardBytes, ws.peer.t.chunkSize)))
}

func (ws *webseedPeer) keepReading(wr *webseedRequest) bool {
	for ri := wr.next; ri < wr.end && ri < wr.next+ws.maxChunkDiscard(); ri++ {
		if ws.wantChunk(ri) {
			return true
		}
	}
	return false
}

func (ws *webseedPeer) readChunks(wr *webseedRequest) (err error) {
	t := ws.peer.t
	buf := t.getChunkBuffer()
	defer t.putChunkBuffer(buf)
	msg := pp.Message{
		Type: pp.Piece,
	}
	for {
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
		// TODO: Clean up the parameters for receiveChunk.
		msg.Piece = buf
		msg.Index = reqSpec.Index
		msg.Begin = reqSpec.Begin

		ws.peer.locker().Lock()
		// Ensure the request is pointing to the next chunk before receiving the current one. If
		// webseed requests are triggered, we want to ensure our existing request is up to date.
		wr.next++
		err = ws.peer.receiveChunk(&msg)
		stop := err != nil || !ws.keepReading(wr)
		ws.peer.locker().Unlock()

		if err != nil {
			err = fmt.Errorf("processing chunk: %w", err)
		}
		if stop {
			return
		}
	}
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

func (me *webseedPeer) slogger() *slog.Logger {
	return me.peer.slogger
}
