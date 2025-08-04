package torrent

import (
	"context"
	"fmt"
	"io"
	"iter"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/chansync"
	. "github.com/anacrolix/generics"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2/bitmap"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/multiless"

	"github.com/anacrolix/torrent/mse"
	pp "github.com/anacrolix/torrent/peer_protocol"
	typedRoaring "github.com/anacrolix/torrent/typed-roaring"
)

type (
	// Generic Peer-like fields. Could be WebSeed, BitTorrent over TCP, uTP or WebRTC.
	Peer struct {
		// First to ensure 64-bit alignment for atomics. See #262.
		_stats ConnStats

		cl *Client
		t  *Torrent

		legacyPeerImpl
		peerImpl  newHotPeerImpl
		callbacks *Callbacks

		RemoteAddr              PeerRemoteAddr
		Discovery               PeerSource
		trusted                 bool
		closed                  chansync.SetOnce
		closedCtx               context.Context
		closedCtxCancel         context.CancelFunc
		lastUsefulChunkReceived time.Time

		lastStartedExpectingToReceiveChunks time.Time
		cumulativeExpectedToReceiveChunks   time.Duration
		// Pieces we've accepted chunks for from the peer.
		peerTouchedPieces map[pieceIndex]struct{}

		logger  log.Logger
		slogger *slog.Logger

		// Belongs in PeerConn:

		outgoing bool
		Network  string
		// The local address as observed by the remote peer. WebRTC seems to get this right without needing hints from the
		// config.
		localPublicAddr peerLocalPublicAddr
		bannableAddr    Option[bannableAddr]
		// True if the connection is operating over MSE obfuscation.
		headerEncrypted bool
		cryptoMethod    mse.CryptoMethod

		lastMessageReceived time.Time
		completedHandshake  time.Time
		lastChunkSent       time.Time

		// Stuff controlled by the local peer.
		needRequestUpdate    updateRequestReason
		updateRequestsTimer  *time.Timer
		lastRequestUpdate    time.Time
		peakRequests         maxRequests
		lastBecameInterested time.Time
		priorInterest        time.Duration

		choking bool

		// Stuff controlled by the remote peer.
		peerInterested        bool
		peerChoking           bool
		PeerPrefersEncryption bool // as indicated by 'e' field in extension handshake
		// The highest possible number of pieces the torrent could have based on
		// communication with the peer. Generally only useful until we have the
		// torrent info.
		peerMinPieces pieceIndex

		peerAllowedFast typedRoaring.Bitmap[pieceIndex]
	}

	PeerSource string

	PeerRemoteAddr interface {
		String() string
	}

	peerRequests = orderedBitmap[RequestIndex]

	updateRequestReason string
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

// These are grouped because we might vary update request behaviour depending on the reason. I'm not
// sure about the fact that multiple reasons can be triggered before an update runs, and only the
// first will count. Possibly we should be signalling what behaviours are appropriate in the next
// update instead.
const (
	peerUpdateRequestsPeerCancelReason   updateRequestReason = "Peer.cancel"
	peerUpdateRequestsRemoteRejectReason updateRequestReason = "Peer.remoteRejectedRequest"
)

// Returns the Torrent a Peer belongs to. Shouldn't change for the lifetime of the Peer. May be nil
// if we are the receiving end of a connection and the handshake hasn't been received or accepted
// yet.
func (p *Peer) Torrent() *Torrent {
	return p.t
}

func (p *Peer) Stats() (ret PeerStats) {
	p.locker().RLock()
	defer p.locker().RUnlock()
	ret.ConnStats = p._stats.Copy()
	ret.DownloadRate = p.downloadRate()
	ret.LastWriteUploadRate = p.peerImpl.lastWriteUploadRate()
	ret.RemotePieceCount = p.remotePieceCount()
	return
}

func (cn *Peer) updateExpectingChunks() {
	if cn.peerImpl.expectingChunks() {
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

func (cn *Peer) locker() *lockWithDeferreds {
	return cn.t.cl.locker()
}

// The best guess at number of pieces in the torrent for this peer.
func (cn *Peer) bestPeerNumPieces() pieceIndex {
	if cn.t.haveInfo() {
		return cn.t.numPieces()
	}
	return cn.peerMinPieces
}

// How many pieces we think the peer has.
func (cn *Peer) remotePieceCount() pieceIndex {
	have := pieceIndex(cn.peerPieces().GetCardinality())
	if all, _ := cn.peerHasAllPieces(); all {
		have = cn.bestPeerNumPieces()
	}
	return have
}

func (cn *Peer) completedString() string {
	return fmt.Sprintf("%d/%d", cn.remotePieceCount(), cn.bestPeerNumPieces())
}

func eventAgeString(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return fmt.Sprintf("%.2fs ago", time.Since(t).Seconds())
}

func (cn *Peer) downloadRate() float64 {
	num := cn._stats.BytesReadUsefulData.Int64()
	if num == 0 {
		return 0
	}
	return float64(num) / cn.totalExpectingTime().Seconds()
}

// Deprecated: Use Peer.Stats.
func (p *Peer) DownloadRate() float64 {
	return p.Stats().DownloadRate
}

func (cn *Peer) writeStatus(w io.Writer) {
	// \t isn't preserved in <pre> blocks?
	if cn.closed.IsSet() {
		fmt.Fprint(w, "CLOSED: ")
	}
	fmt.Fprintln(w, strings.Join(cn.peerImplStatusLines(), "\n"))
	cn.peerImplWriteStatus(w)
	fmt.Fprintf(w,
		"%d pieces touched, good chunks: %v/%v, dr: %.1f KiB/s\n",
		len(cn.peerTouchedPieces),
		&cn._stats.ChunksReadUseful,
		&cn._stats.ChunksRead,
		cn.downloadRate()/(1<<10),
	)
	fmt.Fprintf(w, "\n")
}

func (p *Peer) close() {
	if !p.closed.Set() {
		return
	}
	// Not set until Torrent is known.
	if p.closedCtx != nil {
		p.closedCtxCancel()
	}
	if p.updateRequestsTimer != nil {
		p.updateRequestsTimer.Stop()
	}
	p.legacyPeerImpl.onClose()
	if p.t != nil {
		p.t.decPeerPieceAvailability(p)
	}
	for _, f := range p.callbacks.PeerClosed {
		f(p)
	}
}

func (p *Peer) Close() error {
	p.locker().Lock()
	defer p.locker().Unlock()
	p.close()
	return nil
}

// Peer definitely has a piece, for purposes of requesting. So it's not sufficient that we think
// they do (known=true).
func (cn *Peer) peerHasPiece(piece pieceIndex) bool {
	if all, known := cn.peerHasAllPieces(); all && known {
		return true
	}
	return cn.peerPieces().ContainsInt(piece)
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

func (cn *Peer) totalExpectingTime() (ret time.Duration) {
	ret = cn.cumulativeExpectedToReceiveChunks
	if !cn.lastStartedExpectingToReceiveChunks.IsZero() {
		ret += time.Since(cn.lastStartedExpectingToReceiveChunks)
	}
	return
}

// The function takes a message to be sent, and returns true if more messages
// are okay.
type messageWriter func(pp.Message) bool

// All ConnStats that include this connection. Some objects are not known until the handshake is
// complete, after which it's expected to reconcile the differences.
func (cn *Peer) modifyRelevantConnStats(f func(*ConnStats)) {
	// Every peer has basic ConnStats for now.
	f(&cn._stats)
	incAll := func(stats *ConnStats) bool {
		f(stats)
		return true
	}
	cn.upstreamConnStats()(incAll)
}

// Yields relevant upstream ConnStats. Skips Torrent if it isn't set.
func (cn *Peer) upstreamConnStats() iter.Seq[*ConnStats] {
	return func(yield func(*ConnStats) bool) {
		// PeerConn can be nil when it hasn't completed handshake.
		if cn.t != nil {
			cn.relevantConnStats(&cn.t.connStats)(yield)
		}
		cn.relevantConnStats(&cn.cl.connStats)(yield)
	}
}

func (cn *Peer) readBytes(n int64) {
	cn.modifyRelevantConnStats(add(n, func(cs *ConnStats) *Count { return &cs.BytesRead }))
}

func (c *Peer) lastHelpful() (ret time.Time) {
	ret = c.lastUsefulChunkReceived
	if c.t.seeding() && c.lastChunkSent.After(ret) {
		ret = c.lastChunkSent
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

func (c *Peer) doChunkReadStats(size int64) {
	c.modifyRelevantConnStats(func(cs *ConnStats) { cs.receivedChunk(size) })
}

// Handle a received chunk from a peer. TODO: Break this out into non-wire protocol specific
// handling. Avoid shoehorning into a pp.Message.
func (c *Peer) receiveChunk(msg *pp.Message) error {
	ChunksReceived.Add("total", 1)

	ppReq := newRequestFromMessage(msg)
	t := c.t
	err := t.checkValidReceiveChunk(ppReq)
	if err != nil {
		err = log.WithLevel(log.Warning, err)
		return err
	}
	req := c.t.requestIndexFromRequest(ppReq)

	recordBlockForSmartBan := sync.OnceFunc(func() {
		c.recordBlockForSmartBan(req, msg.Piece)
	})
	// This needs to occur before we return, but we try to do it when the client is unlocked. It
	// can't be done before checking if chunks are valid because they won't be deallocated from the
	// smart ban cache by piece hashing if they're out of bounds.
	defer recordBlockForSmartBan()

	if c.peerChoking {
		ChunksReceived.Add("while choked", 1)
	}

	intended, err := c.peerImpl.checkReceivedChunk(req, msg, ppReq)
	if err != nil {
		return err
	}

	cl := t.cl

	// Do we actually want this chunk?
	if t.haveChunk(ppReq) {
		// panic(fmt.Sprintf("%+v", ppReq))
		ChunksReceived.Add("redundant", 1)
		c.modifyRelevantConnStats(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadWasted }))
		return nil
	}

	piece := t.piece(ppReq.Index.Int())

	c.modifyRelevantConnStats(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadUseful }))
	c.modifyRelevantConnStats(add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulData }))
	if intended {
		c.modifyRelevantConnStats(add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulIntendedData }))
	}
	for _, f := range c.t.cl.config.Callbacks.ReceivedUsefulData {
		f(ReceivedUsefulDataEvent{c, msg})
	}
	c.lastUsefulChunkReceived = time.Now()

	// Need to record that it hasn't been written yet, before we attempt to do
	// anything with it.
	piece.incrementPendingWrites()
	// Record that we have the chunk, so we aren't trying to download it while
	// waiting for it to be written to storage.
	piece.unpendChunkIndex(chunkIndexFromChunkSpec(ppReq.ChunkSpec, t.chunkSize))

	// Cancel pending requests for this chunk from *other* peers.
	if p := t.requestingPeer(req); p != nil {
		if p.peerPtr() == c {
			p.logger.Slogger().Error("received chunk but still pending request", "peer", p, "req", req)
			panic("should not be pending request from conn that just received it")
		}
		p.cancel(req)
	}

	err = func() error {
		cl.unlock()
		defer cl.lock()
		// Opportunistically do this here while we aren't holding the client lock.
		recordBlockForSmartBan()
		concurrentChunkWrites.Add(1)
		defer concurrentChunkWrites.Add(-1)
		// Write the chunk out. Note that the upper bound on chunk writing concurrency will be the
		// number of connections. We write inline with receiving the chunk (with this lock dance),
		// because we want to handle errors synchronously and I haven't thought of a nice way to
		// defer any concurrency to the storage and have that notify the client of errors. TODO: Do
		// that instead.
		return t.writeChunk(int(msg.Index), int64(msg.Begin), msg.Piece)
	}()

	piece.decrementPendingWrites()

	if err != nil {
		c.logger.WithDefaultLevel(log.Error).Printf("writing received chunk %v: %v", req, err)
		t.pendRequest(req)
		// Necessary to pass TestReceiveChunkStorageFailureSeederFastExtensionDisabled. I think a
		// request update runs while we're writing the chunk that just failed. Then we never do a
		// fresh update after pending the failed request.
		c.onNeedUpdateRequests("Peer.receiveChunk error writing chunk")
		t.onWriteChunkErr(err)
		return nil
	}

	c.onDirtiedPiece(pieceIndex(ppReq.Index))

	// We need to ensure the piece is only queued once, so only the last chunk writer gets this job.
	if t.pieceAllDirty(pieceIndex(ppReq.Index)) && piece.pendingWrites == 0 {
		t.queuePieceCheck(pieceIndex(ppReq.Index))
		// We don't pend all chunks here anymore because we don't want code dependent on the dirty
		// chunk status (such as the haveChunk call above) to have to check all the various other
		// piece states like queued for hash, hashing etc. This does mean that we need to be sure
		// that chunk pieces are pended at an appropriate time later however.
	}

	cl.event.Broadcast()
	// We do this because we've written a chunk, and may change PieceState.Partial.
	t.deferPublishPieceStateChange(pieceIndex(ppReq.Index))

	return nil
}

func (c *Peer) onDirtiedPiece(piece pieceIndex) {
	if c.peerTouchedPieces == nil {
		c.peerTouchedPieces = make(map[pieceIndex]struct{})
	}
	c.peerTouchedPieces[piece] = struct{}{}
	ds := &c.t.pieces[piece].dirtiers
	if *ds == nil {
		*ds = make(map[*Peer]struct{})
	}
	(*ds)[c] = struct{}{}
}

func (cn *Peer) netGoodPiecesDirtied() int64 {
	return cn._stats.PiecesDirtiedGood.Int64() - cn._stats.PiecesDirtiedBad.Int64()
}

func (c *Peer) peerHasWantedPieces() bool {
	if all, _ := c.peerHasAllPieces(); all {
		return !c.t.haveAllPieces() && !c.t._pendingPieces.IsEmpty()
	}
	if !c.t.haveInfo() {
		return !c.peerPieces().IsEmpty()
	}
	return c.peerPieces().Intersects(&c.t._pendingPieces)
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

func (l connectionTrust) Cmp(r connectionTrust) int {
	return multiless.New().Bool(l.Implicit, r.Implicit).Int64(l.NetGoodPiecesDirted, r.NetGoodPiecesDirted).OrderingInt()
}

// Returns a new Bitmap that includes bits for all pieces the peer could have based on their claims.
func (cn *Peer) newPeerPieces() *roaring.Bitmap {
	// TODO: Can we use copy on write?
	ret := cn.peerPieces().Clone()
	if all, _ := cn.peerHasAllPieces(); all {
		if cn.t.haveInfo() {
			ret.AddRange(0, bitmap.BitRange(cn.t.numPieces()))
		} else {
			ret.AddRange(0, bitmap.ToEnd)
		}
	}
	return ret
}

func (p *Peer) TryAsPeerConn() (*PeerConn, bool) {
	pc, ok := p.legacyPeerImpl.(*PeerConn)
	return pc, ok
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

func (p *Peer) initClosedCtx() {
	panicif.NotNil(p.closedCtx)
	p.closedCtx, p.closedCtxCancel = context.WithCancel(p.t.closedCtx)
}

// Iterates base and peer-impl specific ConnStats from all.
func (p *Peer) relevantConnStats(all *AllConnStats) iter.Seq[*ConnStats] {
	return func(yield func(*ConnStats) bool) {
		yield(&all.ConnStats)
		yield(p.peerImpl.allConnStatsImplField(all))
	}
}
