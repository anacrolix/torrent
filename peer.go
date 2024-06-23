package torrent

import (
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/chansync"
	. "github.com/anacrolix/generics"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/iter"
	"github.com/anacrolix/missinggo/v2/bitmap"
	"github.com/anacrolix/multiless"

	"github.com/anacrolix/torrent/internal/alloclim"
	"github.com/anacrolix/torrent/mse"
	pp "github.com/anacrolix/torrent/peer_protocol"
	request_strategy "github.com/anacrolix/torrent/request-strategy"
	typedRoaring "github.com/anacrolix/torrent/typed-roaring"
)

type (
	Peer struct {
		// First to ensure 64-bit alignment for atomics. See #262.
		_stats ConnStats
		mu     sync.RWMutex

		t *Torrent

		peerImpl
		callbacks *Callbacks

		outgoing   bool
		Network    string
		RemoteAddr PeerRemoteAddr
		// The local address as observed by the remote peer. WebRTC seems to get this right without needing hints from the
		// config.
		localPublicAddr peerLocalPublicAddr
		bannableAddr    Option[bannableAddr]
		// True if the connection is operating over MSE obfuscation.
		headerEncrypted bool
		cryptoMethod    mse.CryptoMethod
		Discovery       PeerSource
		trusted         bool
		closed          chansync.SetOnce
		// Set true after we've added our ConnStats generated during handshake to
		// other ConnStat instances as determined when the *Torrent became known.
		reconciledHandshakeStats bool

		lastMessageReceived     time.Time
		completedHandshake      time.Time
		lastUsefulChunkReceived time.Time
		lastChunkSent           time.Time

		// Stuff controlled by the local peer.
		needRequestUpdate    string
		requestState         request_strategy.PeerRequestState
		updateRequestsTimer  *time.Timer
		lastRequestUpdate    time.Time
		peakRequests         maxRequests
		lastBecameInterested time.Time
		priorInterest        time.Duration

		lastStartedExpectingToReceiveChunks time.Time
		cumulativeExpectedToReceiveChunks   time.Duration
		_chunksReceivedWhileExpecting       int64

		choking                                bool
		piecesReceivedSinceLastRequestUpdate   maxRequests
		maxPiecesReceivedBetweenRequestUpdates maxRequests
		// Chunks that we might reasonably expect to receive from the peer. Due to latency, buffering,
		// and implementation differences, we may receive chunks that are no longer in the set of
		// requests actually want. This could use a roaring.BSI if the memory use becomes noticeable.
		validReceiveChunks map[RequestIndex]int
		// Indexed by metadata piece, set to true if posted and pending a
		// response.
		metadataRequests []bool
		sentHaves        bitmap.Bitmap

		// Stuff controlled by the remote peer.
		peerInterested        bool
		peerChoking           bool
		peerRequests          map[Request]*peerRequestState
		PeerPrefersEncryption bool // as indicated by 'e' field in extension handshake
		// The highest possible number of pieces the torrent could have based on
		// communication with the peer. Generally only useful until we have the
		// torrent info.
		peerMinPieces pieceIndex
		// Pieces we've accepted chunks for from the peer.
		peerTouchedPieces map[pieceIndex]struct{}
		peerAllowedFast   typedRoaring.Bitmap[pieceIndex]
		// cached value for logging and calculation to avoid repeated calls
		// to getDesiredRequestState
		desiredRequestLen int
		PeerMaxRequests   maxRequests // Maximum pending requests the peer allows.

		logger log.Logger
	}

	PeerSource string

	peerRequestState struct {
		data             []byte
		allocReservation *alloclim.Reservation
	}

	PeerRemoteAddr interface {
		String() string
	}

	peerRequests = orderedBitmap[RequestIndex]
)

const (
	PeerSourceUtHolepunch     = "C"
	PeerSourceTracker         = "Tr"
	PeerSourceIncoming        = "I"
	PeerSourceDhtGetPeers     = "Hg" // Peers we found by searching a DHT.
	PeerSourceDhtAnnouncePeer = "Ha" // Peers that were announced to us by a DHT.
	PeerSourcePex             = "X"
	// The peer was given directly, such as through a magnet link.
	PeerSourceDirect = "M"
)

// Returns the Torrent a Peer belongs to. Shouldn't change for the lifetime of the Peer. May be nil
// if we are the receiving end of a connection and the handshake hasn't been received or accepted
// yet.
func (p *Peer) Torrent() *Torrent {
	return p.t
}

func (p *Peer) initRequestState() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.requestState.Requests = &peerRequests{}
}

func (cn *Peer) updateExpectingChunks(lockTorrent bool) {
	if cn.expectingChunks(lockTorrent) {
		if cn.lastStartedExpectingToReceiveChunks.IsZero() {
			cn.lastStartedExpectingToReceiveChunks = time.Now()
		}
	} else {
		if !cn.lastStartedExpectingToReceiveChunks.IsZero() {
			cn.cumulativeExpectedToReceiveChunks += time.Since(cn.lastStartedExpectingToReceiveChunks)
			cn.lastStartedExpectingToReceiveChunks = time.Time{}
		}
	}
}

func (cn *Peer) expectingChunks(lockTorrent bool) bool {
	if cn.requestState.Requests.IsEmpty() {
		return false
	}
	if !cn.requestState.Interested {
		return false
	}
	if !cn.peerChoking {
		return true
	}
	haveAllowedFastRequests := false

	if lockTorrent {
		cn.t.mu.RLock()
		defer cn.t.mu.RUnlock()
	}

	cn.peerAllowedFast.Iterate(func(i pieceIndex) bool {
		haveAllowedFastRequests = roaringBitmapRangeCardinality[RequestIndex](
			cn.requestState.Requests,
			cn.t.pieceRequestIndexOffset(i, false),
			cn.t.pieceRequestIndexOffset(i+1, false),
		) == 0
		return !haveAllowedFastRequests
	})
	return haveAllowedFastRequests
}

func (cn *Peer) remoteChokingPiece(piece pieceIndex, lock bool) bool {
	if lock {
		cn.mu.RLock()
		defer cn.mu.RUnlock()
	}

	return cn.peerChoking && !cn.peerAllowedFast.Contains(piece)
}

func (cn *Peer) cumInterest(lock bool) time.Duration {
	if lock {
		cn.mu.RLock()
		defer cn.mu.RUnlock()
	}

	ret := cn.priorInterest
	if cn.requestState.Interested {
		ret += time.Since(cn.lastBecameInterested)
	}
	return ret
}

func (cn *PeerConn) supportsExtension(ext pp.ExtensionName, lock bool) bool {
	if lock {
		cn.mu.RLock()
		defer cn.mu.RUnlock()
	}

	_, ok := cn.PeerExtensionIDs[ext]
	return ok
}

// The best guess at number of pieces in the torrent for this peer.
func (cn *Peer) bestPeerNumPieces(lock bool, lockTorrent bool) pieceIndex {
	if cn.t.haveInfo(lockTorrent) {
		return cn.t.numPieces()
	}

	if lock {
		cn.mu.RLock()
		defer cn.mu.RUnlock()
	}

	return cn.peerMinPieces
}

func (cn *Peer) completedString(lock bool, lockTorrent bool) string {
	have := pieceIndex(cn.peerPieces(lock).GetCardinality())
	best := cn.bestPeerNumPieces(lock, lockTorrent)
	if all, _ := cn.peerHasAllPieces(lock, lockTorrent); all {
		have = best
	}
	return fmt.Sprintf("%d/%d", have, best)
}

func eventAgeString(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return fmt.Sprintf("%.2fs ago", time.Since(t).Seconds())
}

// Inspired by https://github.com/transmission/transmission/wiki/Peer-Status-Text.
func (cn *Peer) statusFlags() (ret string) {
	c := func(b byte) {
		ret += string([]byte{b})
	}
	if cn.requestState.Interested {
		c('i')
	}
	if cn.choking {
		c('c')
	}
	c('-')
	ret += cn.connectionFlags()
	c('-')
	if cn.peerInterested {
		c('i')
	}
	if cn.peerChoking {
		c('c')
	}
	return
}

func (cn *Peer) downloadRate() float64 {
	num := cn._stats.BytesReadUsefulData.Int64()
	if num == 0 {
		return 0
	}
	return float64(num) / cn.totalExpectingTime().Seconds()
}

func (p *Peer) DownloadRate() float64 {
	p.mu.RLock()
	defer p.mu.RUnlock()

	return p.downloadRate()
}

func (cn *Peer) iterContiguousPieceRequests(f func(piece pieceIndex, count int), lockTorrent bool) {
	var last Option[pieceIndex]
	var count int
	next := func(item Option[pieceIndex]) {
		if item == last {
			count++
		} else {
			if count != 0 {
				f(last.Value, count)
			}
			last = item
			count = 1
		}
	}

	cn.requestState.Requests.Iterate(func(requestIndex request_strategy.RequestIndex) bool {
		next(Some(cn.t.pieceIndexOfRequestIndex(requestIndex, lockTorrent)))
		return true
	})
	next(None[pieceIndex]())
}

func (cn *Peer) writeStatus(w io.Writer, lock bool, lockTorrent bool) {

	if lockTorrent {
		cn.t.mu.RLock()
		defer cn.t.mu.RUnlock()
	}

	// do this before taking the peer lock to avoid lock ordering issues
	nominalMaxRequests := cn.peerImpl.nominalMaxRequests(lock, false)

	if lock {
		cn.mu.RLock()
		defer cn.mu.RUnlock()
	}

	// \t isn't preserved in <pre> blocks?
	if cn.closed.IsSet() {
		fmt.Fprint(w, "CLOSED: ")
	}
	fmt.Fprintln(w, strings.Join(cn.peerImplStatusLines(false), "\n"))
	prio, err := cn.peerPriority()
	prioStr := fmt.Sprintf("%08x", prio)
	if err != nil {
		prioStr += ": " + err.Error()
	}
	fmt.Fprintf(w, "bep40-prio: %v\n", prioStr)
	fmt.Fprintf(w, "last msg: %s, connected: %s, last helpful: %s, itime: %s, etime: %s\n",
		eventAgeString(cn.lastMessageReceived),
		eventAgeString(cn.completedHandshake),
		eventAgeString(cn.lastHelpful(false, false)),
		cn.cumInterest(false),
		cn.totalExpectingTime(),
	)

	lenPeerTouchedPieces := len(cn.peerTouchedPieces)

	fmt.Fprintf(w,
		"%s completed, %d pieces touched, good chunks: %v/%v:%v reqq: %d+%v/(%d/%d):%d/%d, flags: %s, dr: %.1f KiB/s\n",
		cn.completedString(false, false),
		lenPeerTouchedPieces,
		&cn._stats.ChunksReadUseful,
		&cn._stats.ChunksRead,
		&cn._stats.ChunksWritten,
		cn.requestState.Requests.GetCardinality(),
		cn.requestState.Cancelled.GetCardinality(),
		nominalMaxRequests,
		cn.PeerMaxRequests,
		len(cn.peerRequests),
		localClientReqq,
		cn.statusFlags(),
		cn.downloadRate()/(1<<10),
	)
	fmt.Fprintf(w, "requested pieces:")
	cn.iterContiguousPieceRequests(func(piece pieceIndex, count int) {
		fmt.Fprintf(w, " %v(%v)", piece, count)
	}, false)
	fmt.Fprintf(w, "\n")
}

func (p *Peer) close(lock bool, lockTorrent bool) {
	if lockTorrent && p.t != nil {
		p.t.mu.Lock()
		defer p.t.mu.Unlock()
	}

	if lock {
		p.mu.RLock()
		defer p.mu.RUnlock()
	}

	if !p.closed.Set() {
		return
	}
	if p.updateRequestsTimer != nil {
		p.updateRequestsTimer.Stop()
	}
	for _, prs := range p.peerRequests {
		prs.allocReservation.Drop()
	}
	p.peerImpl.onClose(false)
	if p.t != nil {
		p.t.decPeerPieceAvailability(p, false, false)
	}
	for _, f := range p.callbacks.PeerClosed {
		f(p)
	}
}

// Peer definitely has a piece, for purposes of requesting. So it's not sufficient that we think
// they do (known=true).
func (cn *Peer) peerHasPiece(piece pieceIndex, lock bool, lockTorrent bool) bool {
	if lockTorrent {
		cn.t.mu.RLock()
		defer cn.t.mu.RUnlock()
	}

	if lock {
		cn.mu.RLock()
		defer cn.mu.RUnlock()
	}

	if all, known := cn.peerHasAllPieces(false, false); all && known {
		return true
	}

	return cn.peerPieces(false).ContainsInt(piece)
}

// 64KiB, but temporarily less to work around an issue with WebRTC. TODO: Update when
// https://github.com/pion/datachannel/issues/59 is fixed.
const (
	writeBufferHighWaterLen = 1 << 15
	writeBufferLowWaterLen  = writeBufferHighWaterLen / 2
)

var (
	interestedMsgLen = len(pp.Message{Type: pp.Interested}.MustMarshalBinary())
	requestMsgLen    = len(pp.Message{Type: pp.Request}.MustMarshalBinary())
	// This is the maximum request count that could fit in the write buffer if it's at or below the
	// low water mark when we run maybeUpdateActualRequestState.
	maxLocalToRemoteRequests = (writeBufferHighWaterLen - writeBufferLowWaterLen - interestedMsgLen) / requestMsgLen
)

// The actual value to use as the maximum outbound requests.
func (cn *Peer) nominalMaxRequests(lock bool, lockTorrent bool) maxRequests {
	return maxInt(1, minInt(cn.PeerMaxRequests, cn.peakRequests*2, maxLocalToRemoteRequests))
}

func (cn *Peer) totalExpectingTime() (ret time.Duration) {
	ret = cn.cumulativeExpectedToReceiveChunks
	if !cn.lastStartedExpectingToReceiveChunks.IsZero() {
		ret += time.Since(cn.lastStartedExpectingToReceiveChunks)
	}
	return
}

func (cn *Peer) setInterested(interested bool, lock bool, lockTorrent bool) bool {
	if cn.requestState.Interested == interested {
		return true
	}
	cn.requestState.Interested = interested
	if interested {
		cn.lastBecameInterested = time.Now()
	} else if !cn.lastBecameInterested.IsZero() {
		cn.priorInterest += time.Since(cn.lastBecameInterested)
	}
	cn.updateExpectingChunks(lockTorrent)
	// log.Printf("%p: setting interest: %v", cn, interested)
	return cn.writeInterested(interested, lock)
}

// The function takes a message to be sent, and returns true if more messages
// are okay.
type messageWriter func(msg pp.Message, lock bool) bool

// This function seems to only used by Peer.request. It's all logic checks, so maybe we can no-op it
// when we want to go fast.
func (cn *Peer) shouldRequest(r RequestIndex, lockTorrent bool) error {
	if lockTorrent {
		cn.t.mu.RLock()
		defer cn.t.mu.RUnlock()
	}

	err := cn.t.checkValidReceiveChunk(cn.t.requestIndexToRequest(r, false))
	if err != nil {
		return err
	}
	pi := cn.t.pieceIndexOfRequestIndex(r, false)

	cn.mu.RLock()
	defer cn.mu.RUnlock()

	if cn.requestState.Cancelled.Contains(r) {
		return errors.New("request is cancelled and waiting acknowledgement")
	}
	if !cn.peerHasPiece(pi, true, false) {
		return errors.New("requesting piece peer doesn't have")
	}
	if !cn.t.peerIsActive(cn) {
		panic("requesting but not in active conns")
	}
	if cn.closed.IsSet() {
		panic("requesting when connection is closed")
	}
	if cn.t.hashingPiece(pi, false) {
		panic("piece is being hashed")
	}
	if cn.t.pieceQueuedForHash(pi, false) {
		panic("piece is queued for hash")
	}
	if cn.peerChoking && !cn.peerAllowedFast.Contains(pi) {
		// This could occur if we made a request with the fast extension, and then got choked and
		// haven't had the request rejected yet.
		if !cn.requestState.Requests.Contains(r) {
			panic("peer choking and piece not allowed fast")
		}
	}
	return nil
}

func (cn *Peer) mustRequest(r RequestIndex, maxRequests int, lock bool, lockTorrent bool) bool {
	more, err := cn.request(r, maxRequests, lock, lockTorrent)
	if err != nil {
		panic(err)
	}
	return more
}

func (cn *Peer) request(r RequestIndex, maxRequests int, lock bool, lockTorrent bool) (more bool, err error) {
	if lockTorrent {
		cn.t.mu.RLock()
		defer cn.t.mu.RUnlock()
	}

	if lock {
		cn.mu.Lock()
		defer cn.mu.Unlock()
	}

	//if err := cn.shouldRequest(r,false); err != nil {
	//	panic(err)
	//}
	if cn.requestState.Requests.Contains(r) {
		return true, nil
	}
	if int(cn.requestState.Requests.GetCardinality()) >= maxRequests {
		return true, errors.New("too many outstanding requests")
	}

	cn.requestState.Requests.Add(r)
	if cn.validReceiveChunks == nil {
		cn.validReceiveChunks = make(map[RequestIndex]int)
	}
	cn.validReceiveChunks[r]++
	cn.t.requestState[r] = requestState{
		peer: cn,
		when: time.Now(),
	}

	cn.updateExpectingChunks(false)
	ppReq := cn.t.requestIndexToRequest(r, false)
	for _, f := range cn.callbacks.SentRequest {
		f(PeerRequestEvent{cn, ppReq})
	}
	return cn.peerImpl._request(ppReq, false), nil
}

func (me *Peer) cancel(r RequestIndex, updateRequests bool, lock bool, lockTorrent bool) {
	if !me.deleteRequest(r, lock, lockTorrent) {
		panic("request not existing should have been guarded")
	}
	if me._cancel(r, lock, lockTorrent) {
		fmt.Println("CAD", me.t.InfoHash(), r)
		// Record that we expect to get a cancel ack.
		if !me.requestState.Cancelled.CheckedAdd(r) {
			panic(fmt.Sprintf("request %d: already cancelled for hash: %s", r, me.t.InfoHash()))
		}
	}
	me.decPeakRequests()
	if updateRequests && me.isLowOnRequests(lock, lockTorrent) {
		me.updateRequests("Peer.cancel", lock, lockTorrent)
	}
}

// Sets a reason to update requests, and if there wasn't already one, handle it.
func (cn *Peer) updateRequests(reason string, lock bool, lockTorrent bool) {
	needUpdate := func() bool {
		if lock {
			cn.mu.Lock()
			defer cn.mu.Unlock()
		}

		if cn.needRequestUpdate != "" {
			return false
		}

		cn.needRequestUpdate = reason
		return true
	}()

	if needUpdate {
		cn.handleUpdateRequests(lock, lockTorrent)
	}
}

// Emits the indices in the Bitmaps bms in order, never repeating any index.
// skip is mutated during execution, and its initial values will never be
// emitted.
func iterBitmapsDistinct(skip *bitmap.Bitmap, bms ...bitmap.Bitmap) iter.Func {
	return func(cb iter.Callback) {
		for _, bm := range bms {
			if !iter.All(
				func(_i interface{}) bool {
					i := _i.(int)
					if skip.Contains(bitmap.BitIndex(i)) {
						return true
					}
					skip.Add(bitmap.BitIndex(i))
					return cb(i)
				},
				bm.Iter,
			) {
				return
			}
		}
	}
}

// After handshake, we know what Torrent and Client stats to include for a
// connection.
func (cn *Peer) postHandshakeStats(f func(*ConnStats)) {
	t := cn.t
	f(&t.connStats)
	f(&t.cl.connStats)
}

// All ConnStats that include this connection. Some objects are not known
// until the handshake is complete, after which it's expected to reconcile the
// differences.
func (cn *Peer) allStats(f func(*ConnStats)) {
	f(&cn._stats)
	if cn.reconciledHandshakeStats {
		cn.postHandshakeStats(f)
	}
}

func (cn *Peer) readBytes(n int64) {
	cn.allStats(add(n, func(cs *ConnStats) *Count { return &cs.BytesRead }))
}

func (c *Peer) lastHelpful(lock bool, lockTorrent bool) (ret time.Time) {
	var lastChunkSent time.Time

	func() {
		if lock {
			c.mu.RLock()
			defer c.mu.RUnlock()
		}
		ret = c.lastUsefulChunkReceived
		lastChunkSent = c.lastChunkSent
	}()

	if c.t.seeding(lockTorrent) && lastChunkSent.After(ret) {
		ret = lastChunkSent
	}
	return
}

// Returns whether any part of the chunk would lie outside a piece of the given length.
func chunkOverflowsPiece(cs ChunkSpec, pieceLength pp.Integer) bool {
	switch {
	default:
		return false
	case cs.Begin+cs.Length > pieceLength:
	// Check for integer overflow
	case cs.Begin > pp.IntegerMax-cs.Length:
	}
	return true
}

func runSafeExtraneous(f func()) {
	if true {
		go f()
	} else {
		f()
	}
}

// Returns true if it was valid to reject the request.
func (c *Peer) remoteRejectedRequest(r RequestIndex) bool {
	if c.deleteRequest(r, true, true) {
		c.decPeakRequests()
	} else {
		c.mu.RLock()
		removed := c.requestState.Cancelled.CheckedRemove(r)
		c.mu.RUnlock()

		if !removed {
			return false
		}
	}
	if c.isLowOnRequests(true, true) {
		c.updateRequests("Peer.remoteRejectedRequest", true, true)
	}
	c.decExpectedChunkReceive(r)
	return true
}

func (c *Peer) decExpectedChunkReceive(r RequestIndex) {
	c.mu.Lock()
	defer c.mu.Unlock()

	count := c.validReceiveChunks[r]
	if count == 1 {
		delete(c.validReceiveChunks, r)
	} else if count > 1 {
		c.validReceiveChunks[r] = count - 1
	} else {
		panic(r)
	}
}

func (c *Peer) doChunkReadStats(size int64) {
	c.allStats(func(cs *ConnStats) { cs.receivedChunk(size) })
}

// Handle a received chunk from a peer.
func (c *Peer) receiveChunk(msg *pp.Message) error {
	chunksReceived.Add("total", 1)

	ppReq := newRequestFromMessage(msg)
	t := c.t

	err := t.checkValidReceiveChunk(ppReq)
	if err != nil {
		err = log.WithLevel(log.Warning, err)
		return err
	}

	req := c.t.requestIndexFromRequest(ppReq, true)

	recordBlockForSmartBan := sync.OnceFunc(func() {
		c.recordBlockForSmartBan(req, msg.Piece)
	})
	// This needs to occur before we return, but we try to do it when the client is unlocked. It
	// can't be done before checking if chunks are valid because they won't be deallocated by piece
	// hashing if they're out of bounds.
	defer recordBlockForSmartBan()

	cl := t.cl

	piece, intended, err := func() (*Piece, bool, error) {
		t.mu.Lock()
		defer t.mu.Unlock()

		c.mu.RLock()
		peerChoking := c.peerChoking
		validReceiveChunks := c.validReceiveChunks[req]
		allowedFast := c.peerAllowedFast.Contains(pieceIndex(ppReq.Index))
		receivedRequest := c.requestState.Requests.Contains(req)
		c.mu.RUnlock()

		if peerChoking {
			chunksReceived.Add("while choked", 1)
		}

		if validReceiveChunks <= 0 {
			chunksReceived.Add("unexpected", 1)
			return nil, false, errors.New("received unexpected chunk")
		}

		c.decExpectedChunkReceive(req)

		if peerChoking && allowedFast {
			chunksReceived.Add("due to allowed fast", 1)
		}

		// The request needs to be deleted immediately to prevent cancels occurring asynchronously when
		// have actually already received the piece, while we have the Client unlocked to write the data
		// out.
		intended := false
		{
			if receivedRequest {
				for _, f := range c.callbacks.ReceivedRequested {
					f(PeerMessageEvent{c, msg})
				}
			}

			checkRemove := func() bool {
				c.mu.Lock()
				defer c.mu.Unlock()
				return c.requestState.Cancelled.CheckedRemove(req)
			}

			// Request has been satisfied.
			if c.deleteRequest(req, true, false) || checkRemove() {
				intended = true
				if !c.peerChoking {
					c._chunksReceivedWhileExpecting++
				}
			} else {
				chunksReceived.Add("unintended", 1)
			}
		}

		// Do we actually want this chunk?
		if t.haveChunk(ppReq, false) {
			// panic(fmt.Sprintf("%+v", ppReq))
			chunksReceived.Add("redundant", 1)
			c.allStats(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadWasted }))
			return nil, false, nil
		}

		piece := &t.pieces[ppReq.Index]

		c.allStats(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadUseful }))
		c.allStats(add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulData }))
		if intended {
			c.mu.Lock()
			c.piecesReceivedSinceLastRequestUpdate++
			c.mu.Unlock()
			c.allStats(add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulIntendedData }))
		}
		for _, f := range cl.config.Callbacks.ReceivedUsefulData {
			f(ReceivedUsefulDataEvent{c, msg})
		}

		c.mu.Lock()
		c.lastUsefulChunkReceived = time.Now()
		c.mu.Unlock()

		// Need to record that it hasn't been written yet, before we attempt to do
		// anything with it.
		piece.incrementPendingWrites()
		// Record that we have the chunk, so we aren't trying to download it while
		// waiting for it to be written to storage.
		piece.unpendChunkIndex(chunkIndexFromChunkSpec(ppReq.ChunkSpec, t.chunkSize), false)

		// Cancel pending requests for this chunk from *other* peers.
		if p := t.requestingPeer(req, false); p != nil {
			if p == c {
				panic("should not be pending request from conn that just received it")
			}
			fmt.Println("p.receiveChunk", t.InfoHash(), req)
			p.cancel(req, true, true, false)
		}

		return piece, intended, err
	}()

	if piece == nil {
		return err
	}

	// Opportunistically do this here while we aren't holding the client lock.
	recordBlockForSmartBan()

	concurrentChunkWrites.Add(1)
	defer concurrentChunkWrites.Add(-1)

	// Write the chunk out. This is done with no locks waiting on io
	// Note that the upper bound on chunk writing concurrency will be the
	// number of connections. We write inline with receiving the chunk, because we want to handle errors
	// synchronously and I haven't thought of a nice way to defer any concurrency to the storage and have
	// that notify the client of errors. TODO: Do that instead.
	err = t.writeChunk(int(msg.Index), int64(msg.Begin), msg.Piece, true)

	t.mu.Lock()
	defer t.mu.Unlock()

	piece.decrementPendingWrites()

	if err != nil {
		c.logger.WithDefaultLevel(log.Error).Printf("writing received chunk %v: %v", req, err)
		t.pendRequest(req)
		// Necessary to pass TestReceiveChunkStorageFailureSeederFastExtensionDisabled. I think a
		// request update runs while we're writing the chunk that just failed. Then we never do a
		// fresh update after pending the failed request.
		c.updateRequests("Peer.receiveChunk error writing chunk", true, false)
		t.onWriteChunkErr(err)
		return nil
	}

	c.onDirtiedPiece(pieceIndex(ppReq.Index))
	//fmt.Println("RC10")

	// We need to ensure the piece is only queued once, so only the last chunk writer gets this job.
	if t.pieceAllDirty(pieceIndex(ppReq.Index), false) && piece.pendingWrites == 0 {
		t.queuePieceCheck(pieceIndex(ppReq.Index), false)
		// We don't pend all chunks here anymore because we don't want code dependent on the dirty
		// chunk status (such as the haveChunk call above) to have to check all the various other
		// piece states like queued for hash, hashing etc. This does mean that we need to be sure
		// that chunk pieces are pended at an appropriate time later however.
	}

	cl.event.Broadcast()
	// We do this because we've written a chunk, and may change PieceState.Partial.
	t.publishPieceStateChange(pieceIndex(ppReq.Index), false)

	// this is moved after all processing to avoid request rehere because as we no longer have a
	if intended {
		if c.isLowOnRequests(true, false) {
			c.updateRequests("Peer.receiveChunk deleted request", true, false)
		}
	}

	return nil
}

func (c *Peer) onDirtiedPiece(piece pieceIndex) {
	c.mu.Lock()
	if c.peerTouchedPieces == nil {
		c.peerTouchedPieces = make(map[pieceIndex]struct{})
	}
	c.peerTouchedPieces[piece] = struct{}{}
	c.mu.Unlock()

	p := &c.t.pieces[piece]
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dirtiers == nil {
		p.dirtiers = make(map[*Peer]struct{})
	}
	p.dirtiers[c] = struct{}{}
}

func (cn *Peer) netGoodPiecesDirtied() int64 {
	return cn._stats.PiecesDirtiedGood.Int64() - cn._stats.PiecesDirtiedBad.Int64()
}

func (c *Peer) peerHasWantedPieces(lock bool, lockTorrent bool) bool {
	if lockTorrent {
		c.t.mu.RLock()
		defer c.t.mu.RUnlock()
	}

	if lock {
		c.mu.RLock()
		defer c.mu.RUnlock()
	}

	if all, _ := c.peerHasAllPieces(false, false); all {
		isEmpty := c.t._pendingPieces.IsEmpty()

		return !c.t.haveAllPieces(false) && !isEmpty
	}
	if !c.t.haveInfo(false) {
		return !c.peerPieces(false).IsEmpty()
	}

	return c.peerPieces(false).Intersects(&c.t._pendingPieces)
}

// Returns true if an outstanding request is removed. Cancelled requests should be handled
// separately.
func (c *Peer) deleteRequest(r RequestIndex, lock bool, lockTorrent bool) bool {
	if lockTorrent {
		c.t.mu.Lock()
		defer c.t.mu.Unlock()
	}

	if !func() bool {
		if lock {
			c.mu.Lock()
			defer c.mu.Unlock()
		}

		if !c.requestState.Requests.CheckedRemove(r) {
			return false
		}

		for _, f := range c.callbacks.DeletedRequest {
			f(PeerRequestEvent{c, c.t.requestIndexToRequest(r, false)})
		}
		c.updateExpectingChunks(false)
		if c.t.requestingPeer(r, false) != c {
			panic("only one peer should have a given request at a time")
		}
		return true
	}() {
		return false
	}

	delete(c.t.requestState, r)

	// c.t.iterPeers(func(p *Peer) {
	// 	if p.isLowOnRequests() {
	// 		p.updateRequests("Peer.deleteRequest")
	// 	}
	// })
	return true
}

func (c *Peer) deleteAllRequests(reason string, lock bool, lockTorrent bool) {
	if lockTorrent {
		c.t.mu.Lock()
		defer c.t.mu.Unlock()
	}

	if lock {
		c.mu.Lock()
		defer c.mu.Unlock()
	}

	if c.requestState.Requests.IsEmpty() {
		return
	}
	c.requestState.Requests.IterateSnapshot(func(x RequestIndex) bool {
		if !c.deleteRequest(x, false, false) {
			panic("request should exist")
		}
		return true
	})
	c.assertNoRequests()
	c.t.iterPeers(func(p *Peer) {
		if p.isLowOnRequests(false, false) {
			p.updateRequests(reason, false, false)
		}
	}, false)
}

func (c *Peer) assertNoRequests() {
	if !c.requestState.Requests.IsEmpty() {
		panic(c.requestState.Requests.GetCardinality())
	}
}

func (c *Peer) cancelAllRequests(lockTorrent bool) {
	if lockTorrent {
		c.t.mu.Lock()
		defer c.t.mu.Unlock()
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	fmt.Println("p.cancelAllRequests")
	c.requestState.Requests.IterateSnapshot(func(x RequestIndex) bool {
		c.cancel(x, false, false, false)
		return true
	})
	c.assertNoRequests()
}

func (c *Peer) peerPriority() (peerPriority, error) {
	return bep40Priority(c.remoteIpPort(), c.localPublicAddr)
}

func (c *Peer) remoteIp() net.IP {
	host, _, _ := net.SplitHostPort(c.RemoteAddr.String())
	return net.ParseIP(host)
}

func (c *Peer) remoteIpPort() IpPort {
	ipa, _ := tryIpPortFromNetAddr(c.RemoteAddr)
	return IpPort{ipa.IP, uint16(ipa.Port)}
}

func (c *Peer) trust() connectionTrust {
	return connectionTrust{c.trusted, c.netGoodPiecesDirtied()}
}

type connectionTrust struct {
	Implicit            bool
	NetGoodPiecesDirted int64
}

func (l connectionTrust) Less(r connectionTrust) bool {
	return multiless.New().Bool(l.Implicit, r.Implicit).Int64(l.NetGoodPiecesDirted, r.NetGoodPiecesDirted).Less()
}

// Returns a new Bitmap that includes bits for all pieces the peer could have based on their claims.
func (cn *Peer) newPeerPieces(lock bool, lockTorrent bool) (ret *roaring.Bitmap) {
	// TODO: Can we use copy on write?
	func() {
		if lock {
			cn.mu.RLock()
			defer cn.mu.RUnlock()
		}
		ret = cn.peerPieces(false).Clone()
	}()

	if all, _ := cn.peerHasAllPieces(lock, lockTorrent); all {
		if cn.t.haveInfo(true) {
			ret.AddRange(0, bitmap.BitRange(cn.t.numPieces()))
		} else {
			ret.AddRange(0, bitmap.ToEnd)
		}
	}
	return ret
}

func (cn *Peer) stats() *ConnStats {
	return &cn._stats
}

func (p *Peer) TryAsPeerConn() (*PeerConn, bool) {
	pc, ok := p.peerImpl.(*PeerConn)
	return pc, ok
}

func (p *Peer) uncancelledRequests(lock bool) uint64 {
	if lock {
		p.mu.RLock()
		defer p.mu.RUnlock()
	}
	return p.requestState.Requests.GetCardinality()
}

func (p *Peer) desiredRequests(lock bool) int {
	if lock {
		p.mu.RLock()
		defer p.mu.RUnlock()
	}
	return p.desiredRequestLen
}

type peerLocalPublicAddr = IpPort

func (p *Peer) decPeakRequests() {
	// // This can occur when peak requests are altered by the update request timer to be lower than
	// // the actual number of outstanding requests. Let's let it go negative and see what happens. I
	// // wonder what happens if maxRequests is not signed.
	// if p.peakRequests < 1 {
	// 	panic(p.peakRequests)
	// }
	p.peakRequests--
}

func (p *Peer) recordBlockForSmartBan(req RequestIndex, blockData []byte) {
	if p.bannableAddr.Ok {
		p.t.smartBanCache.RecordBlock(p.bannableAddr.Value, req, blockData)
	}
}
