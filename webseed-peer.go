package torrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"runtime/pprof"
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
	peer           Peer
	logger         *slog.Logger
	client         webseed.Client
	activeRequests map[*webseedRequest]struct{}
	locker         sync.Locker
	hostKey        webseedHostKeyHandle
	// We need this to look ourselves up in the Client.activeWebseedRequests map.
	url webseedUrlKey

	// When requests are allowed to resume. If Zero, then anytime.
	penanceComplete time.Time
	lastCrime       error
}

func (me *webseedPeer) suspended() bool {
	return me.lastCrime != nil && time.Now().Before(me.penanceComplete)
}

func (me *webseedPeer) convict(err error, term time.Duration) {
	if me.suspended() {
		return
	}
	me.lastCrime = err
	me.penanceComplete = time.Now().Add(term)
}

func (*webseedPeer) allConnStatsImplField(stats *AllConnStats) *ConnStats {
	return &stats.WebSeeds
}

func (me *webseedPeer) cancelAllRequests() {
	// Is there any point to this? Won't we fail to receive a chunk and cancel anyway? Should we
	// Close requests instead?
	for req := range me.activeRequests {
		req.Cancel("all requests cancelled", me.peer.t)
	}
}

func (me *webseedPeer) peerImplWriteStatus(w io.Writer) {}

func (me *webseedPeer) isLowOnRequests() bool {
	// Updates globally instead.
	return false
}

// Webseed requests are issued globally so per-connection reasons or handling make no sense.
func (me *webseedPeer) onNeedUpdateRequests(reason updateRequestReason) {
	// Too many reasons here: Can't predictably determine when we need to rerun updates.
	// TODO: Can trigger this when we have Client-level active-requests map.
	//me.peer.cl.scheduleImmediateWebseedRequestUpdate(reason)
}

func (me *webseedPeer) expectingChunks() bool {
	return len(me.activeRequests) > 0
}

func (me *webseedPeer) checkReceivedChunk(RequestIndex, *pp.Message, Request) (bool, error) {
	return true, nil
}

func (me *webseedPeer) lastWriteUploadRate() float64 {
	// We never upload to webseeds.
	return 0
}

var _ legacyPeerImpl = (*webseedPeer)(nil)

func (me *webseedPeer) peerImplStatusLines() []string {
	lines := []string{
		me.client.Url,
	}
	if me.lastCrime != nil {
		lines = append(lines, fmt.Sprintf("last crime: %v", me.lastCrime))
	}
	if me.suspended() {
		lines = append(lines, fmt.Sprintf("suspended for %v more", time.Until(me.penanceComplete)))
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

func (ws *webseedPeer) requestIndexTorrentOffset(r RequestIndex) int64 {
	return ws.peer.t.requestIndexBegin(r)
}

func (ws *webseedPeer) intoSpec(begin, end RequestIndex) webseed.RequestSpec {
	t := ws.peer.t
	start := t.requestIndexBegin(begin)
	endOff := t.requestIndexEnd(end - 1)
	return webseed.RequestSpec{start, endOff - start}
}

func (ws *webseedPeer) spawnRequest(begin, end RequestIndex, logger *slog.Logger) {
	extWsReq := ws.client.StartNewRequest(ws.peer.closedCtx, ws.intoSpec(begin, end), logger)
	wsReq := webseedRequest{
		logger:  logger,
		request: extWsReq,
		begin:   begin,
		next:    begin,
		end:     end,
	}
	if ws.hasOverlappingRequests(begin, end) {
		if webseed.PrintDebug {
			logger.Warn(
				"webseedPeer.spawnRequest: request overlaps existing",
				"new", &wsReq,
				"torrent", ws.peer.t)
		}
		ws.peer.t.cl.dumpCurrentWebseedRequests()
	}
	t := ws.peer.t
	cl := t.cl
	ws.activeRequests[&wsReq] = struct{}{}
	t.deferUpdateRegularTrackerAnnouncing()
	g.MakeMapIfNil(&cl.activeWebseedRequests)
	g.MapMustAssignNew(cl.activeWebseedRequests, ws.getRequestKey(&wsReq), &wsReq)
	ws.peer.updateExpectingChunks()
	panicif.Zero(ws.hostKey)
	ws.peer.t.cl.numWebSeedRequests[ws.hostKey]++
	ws.slogger().Debug(
		"starting webseed request",
		"begin", begin,
		"end", end,
		"len", end-begin,
	)
	go ws.sliceProcessor(&wsReq)
}

func (me *webseedPeer) getRequestKey(wr *webseedRequest) webseedUniqueRequestKey {
	// This is used to find the request in the Client's active requests map.
	return webseedUniqueRequestKey{
		url:        me.url,
		t:          me.peer.t,
		sliceIndex: me.peer.t.requestIndexToWebseedSliceIndex(wr.begin),
	}
}

func (me *webseedPeer) hasOverlappingRequests(begin, end RequestIndex) bool {
	for req := range me.activeRequests {
		if req.cancelled.Load() {
			continue
		}
		if begin < req.end && end > req.begin {
			return true
		}
	}
	return false
}

func (ws *webseedPeer) readChunksErrorLevel(err error, req *webseedRequest) slog.Level {
	if req.cancelled.Load() {
		return slog.LevelDebug
	}
	if ws.peer.closedCtx.Err() != nil {
		return slog.LevelDebug
	}
	var h2e http2.GoAwayError
	if errors.As(err, &h2e) {
		if h2e.ErrCode == http2.ErrCodeEnhanceYourCalm {
			// It's fine, we'll sleep for a bit. But it's still interesting.
			return slog.LevelInfo
		}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return slog.LevelInfo
	}
	if errors.Is(err, webseed.ErrStatusOkForRangeRequest{}) {
		// This can happen for uncached results, and we get passed directly to origin. We should be
		// coming back later and then only warning if it happens for a long time.
		return slog.LevelDebug
	}
	// Error if we aren't also using and/or have peers...?
	return slog.LevelWarn
}

// Reads chunks from the responses for the webseed slice.
func (ws *webseedPeer) sliceProcessor(webseedRequest *webseedRequest) {
	// Detach cost association from webseed update requests routine.
	pprof.SetGoroutineLabels(context.Background())
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
		level := ws.readChunksErrorLevel(err, webseedRequest)
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
	cl := ws.peer.cl
	if err == nil && cl.numWebSeedRequests[ws.hostKey] == webseedHostRequestConcurrency/2 {
		cl.updateWebseedRequestsWithReason("webseedPeer.runRequest low water")
	} else if cl.numWebSeedRequests[ws.hostKey] == 0 {
		cl.updateWebseedRequestsWithReason("webseedPeer.runRequest zero requests")
	}
	locker.Unlock()
}

func (ws *webseedPeer) deleteActiveRequest(wr *webseedRequest) {
	g.MustDelete(ws.activeRequests, wr)
	if len(ws.activeRequests) == 0 {
		ws.peer.t.deferUpdateRegularTrackerAnnouncing()
	}
	cl := ws.peer.cl
	cl.numWebSeedRequests[ws.hostKey]--
	g.MustDelete(cl.activeWebseedRequests, ws.getRequestKey(wr))
	ws.peer.updateExpectingChunks()
}

func (ws *webseedPeer) connectionFlags() string {
	return "WS"
}

// Maybe this should drop all existing connections, or something like that.
func (ws *webseedPeer) drop() {}

func (cn *webseedPeer) providedBadData() {
	cn.convict(errors.New("provided bad data"), time.Minute)
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

func (ws *webseedPeer) wantedChunksInDiscardWindow(wr *webseedRequest) bool {
	// Shouldn't call this if request is at the end already.
	panicif.GreaterThanOrEqual(wr.next, wr.end)
	windowEnd := wr.next + ws.maxChunkDiscard()
	panicif.LessThan(windowEnd, wr.next)
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
		reqCtxErr := context.Cause(wr.request.Context())
		if errors.Is(err, reqCtxErr) {
			err = reqCtxErr
		}
		if webseed.PrintDebug && wr.cancelled.Load() {
			fmt.Printf("webseed read %v after cancellation: %v\n", n, err)
		}
		// We need this early for the convict call.
		ws.peer.locker().Lock()
		if err != nil {
			// TODO: Pick out missing files or associate error with file. See also
			// webseed.ReadRequestPartError.
			if !wr.cancelled.Load() {
				ws.convict(err, time.Minute)
			}
			ws.peer.locker().Unlock()
			err = fmt.Errorf("reading chunk: %w", err)
			return
		}
		// TODO: This happens outside Client lock, and stats can be written out of sync with each
		// other. Why even bother with atomics? This needs to happen after the err check above.
		ws.peer.doChunkReadStats(int64(n))
		// TODO: Clean up the parameters for receiveChunk.
		msg.Piece = buf
		msg.Index = reqSpec.Index
		msg.Begin = reqSpec.Begin

		// Ensure the request is pointing to the next chunk before receiving the current one. If
		// webseed requests are triggered, we want to ensure our existing request is up to date.
		wr.next++
		err = ws.peer.receiveChunk(&msg)
		stop := err != nil || wr.next >= wr.end
		if !stop {
			if !ws.wantedChunksInDiscardWindow(wr) {
				// This cancels the stream, but we don't stop su--reading to make the most of the
				// buffered body.
				wr.Cancel("no wanted chunks in discard window", ws.peer.t)
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
