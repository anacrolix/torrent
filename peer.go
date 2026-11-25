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

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/anacrolix/chansync"
	g "github.com/anacrolix/generics"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/multiless"

	peerWire "github.com/anacrolix/torrent/internal/peer-wire"
	"github.com/anacrolix/torrent/mse"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

type (
	// State and behaviour shared by all peer types, embedded in PeerConn and webseedPeer.
	commonPeer struct {
		// First to ensure 64-bit alignment for atomics. See #262.
		_stats ConnStats

		cl *Client
		t  *Torrent

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
		bannableAddr    g.Option[bannableAddr]
		// True if the connection is operating over MSE obfuscation.
		headerEncrypted bool
		cryptoMethod    mse.CryptoMethod

		lastMessageReceived time.Time
		completedHandshake  time.Time
		lastChunkSent       time.Time

		// Stuff controlled by the local peer.
		needRequestUpdate    updateRequestReason
		updateRequestsTimer  *releasedTimer
		lastRequestUpdate    time.Time
		peakRequests         maxRequests
		lastBecameInterested time.Time
		priorInterest        time.Duration

		// What the peer told us in its extended handshake. Embedded so its fields, like
		// PeerExtensionIDs and PeerPrefersEncryption, keep their selectors on PeerConn and webseedPeer.
		peerExtendedHandshake
	}

	PeerSource string

	PeerRemoteAddr interface {
		String() string
	}

	peerExtendedHandshake = peerWire.ExtendedHandshake
	peerHandshake         = peerWire.Handshake

	updateRequestReason string
)

const (
	PeerSourceUtHolepunch     = "C"
	PeerSourceTracker         = "Tr"
	PeerSourceIncoming        = "I"
	PeerSourceDhtGetPeers     = "Hg" // Peers we found by searching a DHT.
	PeerSourceDhtAnnouncePeer = "Ha" // Peers that were announced to us by a DHT.
	PeerSourcePex             = "X"
	PeerSourceLPD             = "L" // Peer discovered through local peer discovery
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
func (p *commonPeer) Torrent() *Torrent {
	return p.t
}

func peerStats(p Peer) (ret PeerStats) {
	cp := p.common()
	cp.locker().RLock()
	defer cp.locker().RUnlock()
	ret.ConnStats = cp._stats.Copy()
	ret.DownloadRate = cp.downloadRate()
	ret.LastWriteUploadRate = p.lastWriteUploadRate()
	ret.RemotePieceCount = remotePieceCount(p)
	return
}

func updateExpectingChunks(p Peer) {
	cp := p.common()
	if p.expectingChunks() {
		if cp.lastStartedExpectingToReceiveChunks.IsZero() {
			cp.lastStartedExpectingToReceiveChunks = time.Now()
		}
	} else {
		if !cp.lastStartedExpectingToReceiveChunks.IsZero() {
			cp.cumulativeExpectedToReceiveChunks += time.Since(cp.lastStartedExpectingToReceiveChunks)
			cp.lastStartedExpectingToReceiveChunks = time.Time{}
		}
	}
}

func (cn *commonPeer) locker() *lockWithDeferreds {
	return cn.t.cl.locker()
}

// The best guess at number of pieces in the torrent for this peer.
func bestPeerNumPieces(p Peer) pieceIndex {
	cp := p.common()
	if cp.t.haveInfo() {
		return cp.t.numPieces()
	}
	return p.peerMinPieces()
}

// How many pieces we think the peer has.
func remotePieceCount(p Peer) pieceIndex {
	have := pieceIndex(p.peerPieces().GetCardinality())
	if all, _ := p.peerHasAllPieces(); all {
		have = bestPeerNumPieces(p)
	}
	return have
}

func (cn *PeerConn) completedString() string {
	return fmt.Sprintf("%d/%d", remotePieceCount(cn), bestPeerNumPieces(cn))
}

func eventAgeString(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return fmt.Sprintf("%.2fs ago", time.Since(t).Seconds())
}

func (cn *commonPeer) downloadRate() float64 {
	num := cn._stats.BytesReadUsefulData.Int64()
	if num == 0 {
		return 0
	}
	return float64(num) / cn.totalExpectingTime().Seconds()
}

// Deprecated: Use Peer.Stats.
func (p *commonPeer) DownloadRate() float64 {
	p.locker().RLock()
	defer p.locker().RUnlock()
	return p.downloadRate()
}

func writePeerStatus(p Peer, w io.Writer) {
	cp := p.common()
	// \t isn't preserved in <pre> blocks?
	if cp.closed.IsSet() {
		fmt.Fprint(w, "CLOSED: ")
	}
	fmt.Fprintln(w, strings.Join(p.peerImplStatusLines(), "\n"))
	p.peerImplWriteStatus(w)
	fmt.Fprintf(w,
		"%d pieces touched, good chunks: %v/%v, dr: %.1f KiB/s\n",
		len(cp.peerTouchedPieces),
		&cp._stats.ChunksReadUseful,
		&cp._stats.ChunksRead,
		cp.downloadRate()/(1<<10),
	)
	fmt.Fprintf(w, "\n")
}

func closePeer(p Peer) {
	cp := p.common()
	if !cp.closed.Set() {
		return
	}
	// Not set until Torrent is known.
	if cp.closedCtx != nil {
		cp.closedCtxCancel()
	}
	if cp.updateRequestsTimer != nil {
		cp.updateRequestsTimer.Stop()
	}
	// Before onClose, which drops a PeerConn's wireState and removes it from the Torrent.
	if cp.t != nil {
		cp.t.decPeerPieceAvailability(p)
	}
	p.onClose()
	for _, f := range cp.callbacks.PeerClosed {
		f(p)
	}
}

func closePeerLocked(p Peer) error {
	cp := p.common()
	cp.locker().Lock()
	defer cp.locker().Unlock()
	closePeer(p)
	return nil
}

func (p *commonPeer) common() *commonPeer {
	return p
}

// Peer definitely has a piece, for purposes of requesting. So it's not sufficient that we think
// they do (known=true).
func peerHasPiece(p Peer, piece pieceIndex) bool {
	if all, known := p.peerHasAllPieces(); all && known {
		return true
	}
	return p.peerPieces().ContainsInt(piece)
}

// 64KiB, but temporarily less to work around an issue with WebRTC. TODO: Update when
// https://github.com/pion/datachannel/issues/59 is fixed.
const (
	writeBufferHighWaterLen = 1 << 15
	writeBufferLowWaterLen  = writeBufferHighWaterLen / 2
	// The read loop stops reading while more than this is waiting to be written. Replies to peer
	// messages are buffered from the read loop, so this bounds what a peer that doesn't read can
	// make us hold.
	maxReadLoopWriteBufferLen = 4 * writeBufferHighWaterLen
)

var (
	interestedMsgLen = len(pp.Message{Type: pp.Interested}.MustMarshalBinary())
	requestMsgLen    = len(pp.Message{Type: pp.Request}.MustMarshalBinary())
	// This is the maximum request count that could fit in the write buffer if it's at or below the
	// low water mark when we run maybeUpdateActualRequestState.
	maxLocalToRemoteRequests = (writeBufferHighWaterLen - writeBufferLowWaterLen - interestedMsgLen) / requestMsgLen
)

func (cn *commonPeer) totalExpectingTime() (ret time.Duration) {
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
func modifyRelevantConnStats(p Peer, f func(*ConnStats)) {
	cp := p.common()
	// Every peer has basic ConnStats for now.
	f(&cp._stats)
	incAll := func(stats *ConnStats) bool {
		f(stats)
		return true
	}
	upstreamConnStats(p)(incAll)
}

// Yields relevant upstream ConnStats. Skips Torrent if it isn't set.
func upstreamConnStats(p Peer) iter.Seq[*ConnStats] {
	cp := p.common()
	return func(yield func(*ConnStats) bool) {
		// PeerConn can be nil when it hasn't completed handshake.
		if cp.t != nil {
			relevantConnStats(p, &cp.t.connStats)(yield)
		}
		relevantConnStats(p, &cp.cl.connStats)(yield)
	}
}

func readBytes(p Peer, n int64) {
	modifyRelevantConnStats(p, add(n, func(cs *ConnStats) *Count { return &cs.BytesRead }))
}

func (c *commonPeer) lastHelpful() (ret time.Time) {
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

func doChunkReadStats(p Peer, size int64) {
	modifyRelevantConnStats(p, func(cs *ConnStats) { cs.receivedChunk(size) })
}

// Handle a received chunk from a peer. TODO: Break this out into non-wire protocol specific
// handling. Avoid shoehorning into a pp.Message. Called with the Client lock held, which is released
// while the chunk is written to storage.
func receiveChunk(p Peer, msg *pp.Message) error {
	cp := p.common()
	w, err := receiveChunkPrologue(p, msg)
	if err != nil || w == nil {
		return err
	}
	cl := cp.t.cl
	cl.unlock()
	writeErr := w.write()
	cl.lock()
	receiveChunkEpilogue(p, w, writeErr)
	return nil
}

// A received chunk that has been accepted and must be written to storage. Created by
// receiveChunkPrologue, and finished with receiveChunkEpilogue once write has returned. The chunk
// data is borrowed from the message, so it must stay valid until the epilogue.
type pendingChunkWrite struct {
	t     *Torrent
	req   RequestIndex
	piece Piece
	index int
	begin int64
	data  []byte
	// Holds the pending-write reference until the epilogue, so hashing and storage readers wait for
	// the write, and the tracking struct can't be reclaimed while the write is in flight.
	pw                     *piecePendingWrites
	recordBlockForSmartBan func()
}

// Writes the chunk to storage. Must be called without the Client lock.
func (w *pendingChunkWrite) write() error {
	// Opportunistically do this here while we aren't holding the client lock.
	w.recordBlockForSmartBan()
	concurrentChunkWrites.Add(1)
	defer concurrentChunkWrites.Add(-1)
	return w.t.writeChunk(w.index, w.begin, w.data)
}

// Validates a received chunk and updates state as though it's already written, so it isn't
// requested from anyone else while the write is in flight. Returns nil if the chunk doesn't need
// writing. Called with the Client lock held. Every non-nil result must be passed to
// receiveChunkEpilogue after its write.
func receiveChunkPrologue(p Peer, msg *pp.Message) (_ *pendingChunkWrite, err error) {
	cp := p.common()
	ChunksReceived.Add("total", 1)

	ppReq := newRequestFromMessage(msg)
	t := cp.t
	err = t.checkValidReceiveChunk(ppReq)
	if err != nil {
		err = log.WithLevel(log.Warning, err)
		return
	}
	req := cp.t.requestIndexFromRequest(ppReq)

	// This needs to occur for every valid chunk, but we try to do it when the client is unlocked
	// (in pendingChunkWrite.write). It can't be done before checking if chunks are valid because
	// they won't be deallocated from the smart ban cache by piece hashing if they're out of bounds.
	data := msg.Piece
	recordBlockForSmartBan := sync.OnceFunc(func() {
		cp.recordBlockForSmartBan(req, data)
	})

	intended, err := p.checkReceivedChunk(req, msg, ppReq)
	if err != nil {
		recordBlockForSmartBan()
		return
	}

	// Do we actually want this chunk?
	if t.haveChunk(ppReq) {
		// panic(fmt.Sprintf("%+v", ppReq))
		ChunksReceived.Add("redundant", 1)
		modifyRelevantConnStats(p, add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadWasted }))
		recordBlockForSmartBan()
		return
	}

	piece := t.piece(ppReq.Index.Int())

	modifyRelevantConnStats(p, add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadUseful }))
	modifyRelevantConnStats(p, add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulData }))
	if intended {
		modifyRelevantConnStats(p, add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulIntendedData }))
	}
	for _, f := range cp.t.cl.config.Callbacks.ReceivedUsefulData {
		f(ReceivedUsefulDataEvent{p, msg})
	}
	cp.lastUsefulChunkReceived = time.Now()

	// Record that the chunk hasn't been written yet before we do anything with it.
	pw := piece.incrementPendingWrites()
	// Record that we have the chunk, so we aren't trying to download it while waiting for it to be
	// written to storage.
	piece.unpendChunkIndex(chunkIndexFromChunkSpec(ppReq.ChunkSpec, t.chunkSize))

	// Cancel pending requests for this chunk from *other* peers.
	if other := t.requestingPeer(req); other != nil {
		if other.peerPtr() == p {
			other.logger.Slogger().Error("received chunk but still pending request", "peer", other, "req", req)
			panic("should not be pending request from conn that just received it")
		}
		other.cancel(req)
	}

	return &pendingChunkWrite{
		t:                      t,
		req:                    req,
		piece:                  piece,
		index:                  int(msg.Index),
		begin:                  int64(msg.Begin),
		data:                   data,
		pw:                     pw,
		recordBlockForSmartBan: recordBlockForSmartBan,
	}, nil
}

// Applies the result of writing a chunk. Called with the Client lock held.
func receiveChunkEpilogue(p Peer, w *pendingChunkWrite, err error) {
	cp := p.common()
	t := w.t
	w.pw.decrementPendingWrites()
	if err != nil {
		cp.logger.WithDefaultLevel(log.Error).Printf("writing received chunk %v: %v", w.req, err)
		t.pendRequest(w.req)
		// Necessary to pass TestReceiveChunkStorageFailureSeederFastExtensionDisabled. I think a
		// request update runs while we're writing the chunk that just failed. Then we never do a
		// fresh update after pending the failed request.
		p.onNeedUpdateRequests("Peer.receiveChunk error writing chunk")
		t.onWriteChunkErr(err)
		return
	}

	onDirtiedPiece(p, w.index)

	// We need to ensure the piece is only queued once, so only the last chunk writer gets this job.
	if t.pieceAllDirty(w.index) && w.piece.pendingWritesCount() == 0 {
		t.queueDownloadedPieceCheck(w.index)
		// We don't pend all chunks here anymore because we don't want code dependent on the dirty
		// chunk status (such as the haveChunk call above) to have to check all the various other
		// piece states like queued for hash, hashing etc. This does mean that we need to be sure
		// that chunk pieces are pended at an appropriate time later however.
	}

	t.cl.event.Broadcast()
	// We do this because we've written a chunk, and may change PieceState.Partial.
	t.deferPublishPieceStateChange(w.index)
}

func onDirtiedPiece(p Peer, piece pieceIndex) {
	cp := p.common()
	if cp.peerTouchedPieces == nil {
		cp.peerTouchedPieces = make(map[pieceIndex]struct{})
	}
	cp.peerTouchedPieces[piece] = struct{}{}
	ds := &cp.t.pieces[piece].dirtiers
	if *ds == nil {
		*ds = make(map[Peer]struct{})
	}
	(*ds)[p] = struct{}{}
}

func (cn *commonPeer) netGoodPiecesDirtied() int64 {
	return cn._stats.PiecesDirtiedGood.Int64() - cn._stats.PiecesDirtiedBad.Int64()
}

func (c *PeerConn) peerHasWantedPieces() bool {
	if all, _ := c.peerHasAllPieces(); all {
		return !c.t.haveAllPieces() && !c.t._pendingPieces.IsEmpty()
	}
	if !c.t.haveInfo() {
		return !c.peerPieces().IsEmpty()
	}
	return c.peerPieces().Intersects(&c.t._pendingPieces.Bitmap)
}

func (c *commonPeer) peerPriority() (peerPriority, error) {
	return bep40Priority(c.remoteIpPort(), c.localPublicAddr)
}

func (c *commonPeer) remoteIp() net.IP {
	host, _, _ := net.SplitHostPort(c.RemoteAddr.String())
	// Link-local IPv6 addresses can carry a zone, which net.ParseIP doesn't accept.
	host, _, _ = strings.Cut(host, "%")
	return net.ParseIP(host)
}

func (c *commonPeer) remoteIpPort() IpPort {
	ipa, _ := tryIpPortFromNetAddr(c.RemoteAddr)
	return IpPort{ipa.IP, uint16(ipa.Port)}
}

func (c *commonPeer) trust() connectionTrust {
	return connectionTrust{c.trusted, c.netGoodPiecesDirtied()}
}

type connectionTrust struct {
	Implicit            bool
	NetGoodPiecesDirted int64
}

func (l connectionTrust) Cmp(r connectionTrust) int {
	return multiless.New().Bool(l.Implicit, r.Implicit).Compare(l.NetGoodPiecesDirted, r.NetGoodPiecesDirted).OrderingInt()
}

// Returns a new Bitmap that includes bits for all pieces the peer could have based on their claims.
func (cn *PeerConn) newPeerPieces() *roaring.Bitmap {
	// TODO: Can we use copy on write?
	ret := cn.peerPieces().Clone()
	if all, _ := cn.peerHasAllPieces(); all {
		if cn.t.haveInfo() {
			ret.AddRange(0, uint64(cn.t.numPieces()))
		} else {
			ret.AddRange(0, roaring.MaxRange)
		}
	}
	return ret
}

type peerLocalPublicAddr = IpPort

func (p *commonPeer) decPeakRequests() {
	// // This can occur when peak requests are altered by the update request timer to be lower than
	// // the actual number of outstanding requests. Let's let it go negative and see what happens. I
	// // wonder what happens if maxRequests is not signed.
	// if p.peakRequests < 1 {
	// 	panic(p.peakRequests)
	// }
	p.peakRequests--
}

func (p *commonPeer) recordBlockForSmartBan(req RequestIndex, blockData []byte) {
	if p.bannableAddr.Ok {
		p.t.smartBanCache.RecordBlock(p.bannableAddr.Value, req, blockData)
	}
}

func (p *commonPeer) initClosedCtx() {
	panicif.NotNil(p.closedCtx)
	p.closedCtx, p.closedCtxCancel = context.WithCancel(p.t.closedCtx)
}

// Iterates base and peer-impl specific ConnStats from all.
func relevantConnStats(p Peer, all *AllConnStats) iter.Seq[*ConnStats] {
	return func(yield func(*ConnStats) bool) {
		yield(&all.ConnStats)
		yield(p.allConnStatsImplField(all))
	}
}
