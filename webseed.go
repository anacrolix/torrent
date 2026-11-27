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
	"syscall"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
	"golang.org/x/net/http2"

	"github.com/anacrolix/torrent/metainfo"
	pp "github.com/anacrolix/torrent/peer_protocol"
	"github.com/anacrolix/torrent/webseed"
)

// A web seed (BEP 19) of a Torrent. It isn't a peer: it has no connection, choking or interest, and
// its requests are planned across the Client (see webseed-requesting.go). What it has in common
// with a PeerConn is that chunks are received from it, which is chunkSourceState.
type WebSeed struct {
	// First field for stats alignment.
	chunkSourceState
	closedState
	cl             *Client
	t              *Torrent
	_slogger       *slog.Logger
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

func (me *WebSeed) suspended() bool {
	return me.lastCrime != nil && time.Now().Before(me.penanceComplete)
}

func (me *WebSeed) convict(err error, term time.Duration) {
	if me.suspended() {
		return
	}
	me.lastCrime = err
	me.penanceComplete = time.Now().Add(term)
}

// webseedFileUnavailable reports whether an HTTP status code means this webseed
// does not (and will not) serve the requested file. Pieces for such files should
// be removed from the webseed's bitmap rather than convicting the whole peer.
func webseedFileUnavailable(statusCode int) bool {
	switch statusCode {
	case 403, // Forbidden
		404, // Not Found
		410, // Gone
		451: // Unavailable For Legal Reasons
		return true
	}
	return false
}

// removeFilePieces removes pieces for the given file index from this webseed's bitmap.
// Must be called with the client lock held.
// TODO: Re-add pieces after an interval in case the file becomes available later.
func (ws *WebSeed) removeFilePieces(fi int) {
	for pi := range ws.t.Files()[fi].PieceIndices() {
		if ws.client.Pieces.CheckedRemove(uint32(pi)) {
			ws.t.decPieceAvailability(pieceIndex(pi))
		}
	}
	ws.cl.updateWebseedRequestsWithReason("file unavailable on webseed")
}

func (me *WebSeed) cancelAllRequests() {
	// Is there any point to this? Won't we fail to receive a chunk and cancel anyway? Should we
	// Close requests instead?
	for req := range me.activeRequests {
		req.Cancel("all requests cancelled", me.t)
	}
}

// Trigger the webseed request scheduler if this webseed's URL is under-utilized.
func (me *WebSeed) onNeedUpdateRequests(reason updateRequestReason) {
	if me.cl.webseedHostLowOnRequests(me.hostKey) {
		me.cl.updateWebseedRequestsWithReason(reason)
	}
}

func (me *WebSeed) updateExpectingChunks() {
	me.setExpectingChunks(len(me.activeRequests) > 0)
}

func (ws *WebSeed) Stats() (ret PeerStats) {
	ws.cl.locker().RLock()
	defer ws.cl.locker().RUnlock()
	ret = ws.baseStats()
	// Without the info, how many pieces there are isn't known.
	if ws.t.haveInfo() {
		ret.RemotePieceCount = pieceIndex(ws.client.Pieces.GetCardinality())
	}
	return
}

func (ws *WebSeed) Close() error {
	ws.cl.locker().Lock()
	defer ws.cl.locker().Unlock()
	ws.close()
	return nil
}

// A web seed isn't a PeerConn. See ChunkSource.
func (ws *WebSeed) TryAsPeerConn() (*PeerConn, bool) { return nil, false }

// The Torrent the web seed belongs to.
func (ws *WebSeed) Torrent() *Torrent {
	return ws.t
}

func (ws *WebSeed) writeStatus(w io.Writer) {
	if ws.closed.IsSet() {
		fmt.Fprint(w, "CLOSED: ")
	}
	fmt.Fprintln(w, strings.Join(ws.statusLines(), "\n"))
	ws.chunkSourceState.writeStatus(w)
}

func (ws *WebSeed) source() *chunkSourceState {
	return &ws.chunkSourceState
}

// Web seeds are never implicitly trusted.
func (ws *WebSeed) trust() connectionTrust {
	return connectionTrust{NetGoodPiecesDirted: ws.netGoodPiecesDirtied()}
}

func (ws *WebSeed) modifyRelevantConnStats(f func(*ConnStats)) {
	ws.modifyConnStats(ws.t, ws.cl, f)
}

var _ chunkProvider = (*WebSeed)(nil)

func (me *WebSeed) statusLines() []string {
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

func (ws *WebSeed) String() string {
	return fmt.Sprintf("webseed peer for %q", ws.client.Url)
}

func (ws *WebSeed) onGotInfo(info *metainfo.Info) {
	ws.client.SetInfo(info, ws.t.fileSegmentsIndex.UnwrapPtr())
	// There should be probably be a callback in Client instead, so it can remove pieces at its whim
	// too.
	ws.client.Pieces.Iterate(func(x uint32) bool {
		ws.t.incPieceAvailability(pieceIndex(x))
		return true
	})
}

func (ws *WebSeed) intoSpec(begin, end RequestIndex) webseed.RequestSpec {
	t := ws.t
	start := t.requestIndexBegin(begin)
	endOff := t.requestIndexEnd(end - 1)
	return webseed.RequestSpec{Start: start, Length: endOff - start}
}

func (ws *WebSeed) spawnRequest(begin, end RequestIndex, logger *slog.Logger) {
	extWsReq := ws.client.StartNewRequest(ws.closedCtx, ws.intoSpec(begin, end), logger)
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
				"WebSeed.spawnRequest: request overlaps existing",
				"new", &wsReq,
				"torrent", ws.t)
		}
		ws.t.cl.dumpCurrentWebseedRequests()
	}
	t := ws.t
	cl := t.cl
	ws.activeRequests[&wsReq] = struct{}{}
	t.deferUpdateRegularTrackerAnnouncing()
	g.MakeMapIfNil(&cl.activeWebseedRequests)
	g.MapMustAssignNew(cl.activeWebseedRequests, ws.getRequestKey(&wsReq), &wsReq)
	ws.updateExpectingChunks()
	panicif.Zero(ws.hostKey)
	ws.t.cl.numWebSeedRequests[ws.hostKey]++
	ws.slogger().Debug(
		"starting webseed request",
		"begin", begin,
		"end", end,
		"len", end-begin,
	)
	go ws.sliceProcessor(&wsReq)
}

func (me *WebSeed) getRequestKey(wr *webseedRequest) webseedUniqueRequestKey {
	// This is used to find the request in the Client's active requests map.
	return webseedUniqueRequestKey{
		url:        me.url,
		t:          me.t,
		sliceIndex: me.t.requestIndexToWebseedSliceIndex(wr.begin),
	}
}

func (me *WebSeed) hasOverlappingRequests(begin, end RequestIndex) bool {
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

func (ws *WebSeed) readChunksErrorLevel(err error, req *webseedRequest) slog.Level {
	if req.cancelled.Load() {
		return slog.LevelDebug
	}
	if ws.closedCtx.Err() != nil {
		return slog.LevelDebug
	}
	if isHttp2GoAwayEnhanceYourCalm(err) {
		// It's fine, we'll sleep for a bit. But it's still interesting.
		return slog.LevelInfo
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
	if errors.Is(err, errIdleTimeout) {
		return slog.LevelDebug
	}
	if errors.Is(err, syscall.ECONNRESET) {
		return slog.LevelDebug
	}
	// Error if we aren't also using and/or have peers...?
	return slog.LevelWarn
}

// Reads chunks from the responses for the webseed slice.
func (ws *WebSeed) sliceProcessor(webseedRequest *webseedRequest) {
	// Detach cost association from webseed update requests routine.
	pprof.SetGoroutineLabels(context.Background())
	locker := ws.locker
	err := ws.readChunks(webseedRequest)
	if webseed.PrintDebug && webseedRequest.next < webseedRequest.end {
		fmt.Printf("webseed peer request %v in %v stopped reading chunks early: %v\n", webseedRequest, ws.t.name(), err)
		if err == nil {
			ws.t.cl.dumpCurrentWebseedRequests()
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
		// Only back off for errors the webseed is responsible for. A request we cancelled ourselves
		// (the usual end of a request once its remaining chunks are no longer wanted) says nothing
		// about the server, and the wait holds the slice: nobody else may request it while this
		// request sits here sleeping.
		if !ourOwnDoing(err, webseedRequest, ws.closedCtx) {
			select {
			case <-ws.closed.Done():
			case <-time.After(time.Duration(rand.Int63n(int64(10 * time.Second)))):
			}
		}
	}
	ws.slogger().Debug("webseed request ended")
	locker.Lock()
	// Delete this entry after waiting above on an error, to prevent more requests.
	ws.deleteActiveRequest(webseedRequest)
	cl := ws.cl
	if cl.webseedHostLowOnRequests(ws.hostKey) {
		cl.updateWebseedRequestsWithReason("WebSeed.runRequest low on requests")
	}
	locker.Unlock()
}

func (ws *WebSeed) deleteActiveRequest(wr *webseedRequest) {
	g.MustDelete(ws.activeRequests, wr)
	if len(ws.activeRequests) == 0 {
		ws.t.deferUpdateRegularTrackerAnnouncing()
	}
	cl := ws.cl
	cl.numWebSeedRequests[ws.hostKey]--
	g.MustDelete(cl.activeWebseedRequests, ws.getRequestKey(wr))
	ws.updateExpectingChunks()
}

func (cn *WebSeed) providedBadData() {
	cn.convict(errors.New("provided bad data"), time.Minute)
}

func (ws *WebSeed) close() {
	if !ws.beginClose() {
		return
	}
	// Undo onGotInfo.
	if ws.t.haveInfo() {
		ws.client.Pieces.Iterate(func(x uint32) bool {
			ws.t.decPieceAvailability(pieceIndex(x))
			return true
		})
	}
	ws.t.updateRequestsOfConnsLowOnRequests("WebSeed.onClose")
}

// Handle a chunk read from a response. Called with the Client lock held, which is released while
// the chunk is written to storage.
func (ws *WebSeed) receiveChunk(msg *pp.Message) error {
	t := ws.t
	if _, _, err := t.validateReceivedChunk(msg); err != nil {
		return err
	}
	// Webseed requests are for whole slices, so a chunk is never one we no longer intended to get.
	w := t.acceptChunk(ws, msg, true)
	if w == nil {
		return nil
	}
	t.cl.unlock()
	writeErr := w.write()
	t.cl.lock()
	t.finishChunkWrite(w, writeErr)
	return nil
}

// Do we want a chunk, assuming it's valid etc.
func (ws *WebSeed) wantChunk(ri RequestIndex) bool {
	return ws.t.wantReceiveChunk(ri)
}

func (ws *WebSeed) maxChunkDiscard() RequestIndex {
	return RequestIndex(int(intCeilDiv(webseed.MaxDiscardBytes, ws.t.chunkSize)))
}

func (ws *WebSeed) wantedChunksInDiscardWindow(wr *webseedRequest) bool {
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

func (ws *WebSeed) readChunks(wr *webseedRequest) (err error) {
	t := ws.t
	bufPtr := t.chunkPool.Get()
	defer t.chunkPool.Put(bufPtr)
	buf := *bufPtr
	msg := pp.Message{
		Type: pp.Piece,
	}
	for {
		reqSpec := t.requestIndexToRequest(wr.next)
		chunkLen := reqSpec.Length.Int()
		buf = buf[:chunkLen]
		var n int
		n, err = io.ReadFull(wr.request.Body, buf)
		ws.modifyRelevantConnStats(add(int64(n), func(cs *ConnStats) *Count { return &cs.BytesRead }))
		reqCtxErr := context.Cause(wr.request.Context())
		if errors.Is(err, reqCtxErr) {
			err = reqCtxErr
		}
		if webseed.PrintDebug && wr.cancelled.Load() {
			fmt.Printf("webseed read %v after cancellation: %v\n", n, err)
		}
		// We need this early for the convict call.
		ws.cl.locker().Lock()
		if err != nil {
			if !wr.cancelled.Load() {
				var rpe webseed.ReadRequestPartError
				if errors.As(err, &rpe) {
					var badResp webseed.ErrBadResponse
					if errors.As(rpe.Err, &badResp) && webseedFileUnavailable(badResp.Response.StatusCode) {
						ws.slogger().Info(
							"file not available on webseed, removing pieces",
							"fileIndex", rpe.FileIndex,
							"status", badResp.Response.StatusCode,
						)
						ws.removeFilePieces(rpe.FileIndex)
						ws.cl.locker().Unlock()
						err = nil
						return
					}
				}
				if err != nil {
					ws.convict(err, time.Minute)
				}
			}
			ws.cl.locker().Unlock()
			err = fmt.Errorf("reading chunk: %w", err)
			return
		}
		// TODO: This happens outside Client lock, and stats can be written out of sync with each
		// other. Why even bother with atomics? This needs to happen after the err check above.
		ws.modifyRelevantConnStats(func(cs *ConnStats) { cs.receivedChunk(int64(n)) })
		// TODO: Clean up the parameters for receiveChunk.
		msg.Piece = buf
		msg.Index = reqSpec.Index
		msg.Begin = reqSpec.Begin

		// Ensure the request is pointing to the next chunk before receiving the current one. If
		// webseed requests are triggered, we want to ensure our existing request is up to date.
		wr.next++
		err = ws.receiveChunk(&msg)
		stop := err != nil || wr.next >= wr.end
		if !stop {
			if !ws.wantedChunksInDiscardWindow(wr) {
				// This cancels the stream, but we don't stop su--reading to make the most of the
				// buffered body.
				wr.Cancel("no wanted chunks in discard window", ws.t)
			}
		}
		ws.cl.locker().Unlock()

		if err != nil {
			err = fmt.Errorf("processing chunk: %w", err)
		}
		if stop {
			return
		}
	}
}

// Whether the web seed can serve a piece.
func (ws *WebSeed) hasPiece(piece pieceIndex) bool {
	return ws.client.Pieces.ContainsInt(piece)
}

func (me *WebSeed) slogger() *slog.Logger {
	return me._slogger
}

// ourOwnDoing reports whether a request failed because we cancelled it, rather than because of
// anything the webseed did.
func ourOwnDoing(err error, req *webseedRequest, peerCtx context.Context) bool {
	return req.cancelled.Load() || peerCtx.Err() != nil || errors.Is(err, context.Canceled)
}

// Matches by message because with golang.org/x/net >= v0.54 on Go 1.27, http2.GoAwayError is
// internal to net/http and can't be matched with errors.As.
func isHttp2GoAwayEnhanceYourCalm(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "server sent GOAWAY") &&
		strings.Contains(s, "ErrCode="+http2.ErrCodeEnhanceYourCalm.String())
}
