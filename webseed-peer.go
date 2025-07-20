package torrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/RoaringBitmap/roaring"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
	"golang.org/x/net/http2"

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

func (me *webseedPeer) cancelAllRequests() {
	// Is there any point to this? Won't we fail to receive a chunk and cancel anyway? Should we
	// Close requests instead?
	for req := range me.activeRequests {
		req.Cancel()
	}
}

func (me *webseedPeer) peerImplWriteStatus(w io.Writer) {}

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
	lines := []string{
		me.client.Url,
		fmt.Sprintf("last unhandled error: %v", eventAgeString(me.lastUnhandledErr)),
	}
	if len(me.activeRequests) > 0 {
		elems := make([]string, 0, len(me.activeRequests))
		for wr := range me.activeRequests {
			elems = append(elems, fmt.Sprintf("%v of [%v-%v)", wr.next, wr.begin, wr.end))
		}
		lines = append(lines, "active requests: "+strings.Join(elems, ", "))
	}
	return lines
}

func (ws *webseedPeer) String() string {
	return fmt.Sprintf("webseed peer for %q", ws.client.Url)
}

func (ws *webseedPeer) onGotInfo(info *metainfo.Info) {
	ws.client.SetInfo(info, ws.peer.t.fileSegmentsIndex.UnwrapPtr())
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
	if ws.hasOverlappingRequests(begin, end) {
		if webseed.PrintDebug {
			fmt.Printf("webseedPeer.spawnRequest: overlapping request for %v[%v-%v)\n", ws.peer.t.name(), begin, end)
		}
		ws.peer.t.cl.dumpCurrentWebseedRequests()
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

func (me *webseedPeer) hasOverlappingRequests(begin, end RequestIndex) bool {
	for req := range me.activeRequests {
		if req.cancelled.Load() {
			continue
		}
		if begin < req.end && end >= req.begin {
			return true
		}
	}
	return false
}

func readChunksErrorLevel(err error, req *webseedRequest) slog.Level {
	if req.cancelled.Load() {
		return slog.LevelDebug
	}
	var h2e http2.GoAwayError
	if errors.As(err, &h2e) {
		if h2e.ErrCode == http2.ErrCodeEnhanceYourCalm {
			// It's fine, we'll sleep for a bit. But it's still interesting.
			return slog.LevelInfo
		}
	}
	// Error if we aren't also using and/or have peers...?
	return slog.LevelWarn
}

func (ws *webseedPeer) runRequest(webseedRequest *webseedRequest) {
	locker := ws.locker
	err := ws.readChunks(webseedRequest)
	if webseed.PrintDebug && webseedRequest.next < webseedRequest.end {
		fmt.Printf("webseed peer request %v in %v stopped reading chunks early: %v\n", webseedRequest, ws.peer.t.name(), err)
		if err == nil {
			ws.peer.t.cl.dumpCurrentWebseedRequests()
		}
	}
	// Ensure the body reader and response are closed.
	webseedRequest.Close()
	if err != nil {
		level := readChunksErrorLevel(err, webseedRequest)
		ws.slogger().Log(context.TODO(), level, "webseed request error", "err", err)
		torrent.Add("webseed request error count", 1)
		// This used to occur only on webseed.ErrTooFast but I think it makes sense to slow down any
		// kind of error. Pausing here will starve the available requester slots which slows things
		// down. TODO: Use the Retry-After implementation from Erigon.
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

func (ws *webseedPeer) inactiveRequestIndex(index RequestIndex) bool {
	for range ws.activeRequestsForIndex(index) {
		return false
	}
	return true
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
	for ri := wr.next; ri < wr.end && ri <= wr.next+ws.maxChunkDiscard(); ri++ {
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
		if webseed.PrintDebug && wr.cancelled.Load() {
			fmt.Printf("webseed read %v after cancellation: %v\n", n, err)
		}
		if err != nil {
			err = fmt.Errorf("reading chunk: %w", err)
			return
		}
		ws.peer.doChunkReadStats(int64(n))
		// TODO: Clean up the parameters for receiveChunk.
		msg.Piece = buf
		msg.Index = reqSpec.Index
		msg.Begin = reqSpec.Begin

		ws.peer.locker().Lock()
		// Ensure the request is pointing to the next chunk before receiving the current one. If
		// webseed requests are triggered, we want to ensure our existing request is up to date.
		wr.next++
		err = ws.peer.receiveChunk(&msg)
		stop := err != nil || wr.next >= wr.end
		if !stop {
			if !ws.keepReading(wr) {
				wr.Cancel()
			}
		}
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
