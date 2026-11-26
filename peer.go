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

		// Which of a Torrent's and Client's per-type ConnStats this peer counts toward.
		kind peerKind
		// Set for peers that can be banned by address.
		bannableAddr g.Option[bannableAddr]
	}

	// The types of Peer. The zero value is a PeerConn.
	peerKind uint8

	PeerSource string

	PeerRemoteAddr interface {
		String() string
	}

	peerExtendedHandshake = peerWire.ExtendedHandshake
	peerHandshake         = peerWire.Handshake

	updateRequestReason string
)

const (
	peerKindPeerConn peerKind = iota
	peerKindWebseed
)

func (k peerKind) connStats(all *AllConnStats) *ConnStats {
	if k == peerKindWebseed {
		return &all.WebSeeds
	}
	return &all.PeerConns
}

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

// The stats every peer type has. Each type's Stats adds its own. Requires the Client lock.
func (cp *commonPeer) baseStats() (ret PeerStats) {
	ret.ConnStats = cp._stats.Copy()
	ret.DownloadRate = cp.downloadRate()
	return
}

// Records whether the peer owes us chunks, to time how long it's expected to be sending. Each peer
// type calls this when what it's waiting for changes.
func (cp *commonPeer) setExpectingChunks(expecting bool) {
	if expecting {
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
func (cn *PeerConn) bestPeerNumPieces() pieceIndex {
	if cn.t.haveInfo() {
		return cn.t.numPieces()
	}
	return cn.peerMinPieces()
}

// How many pieces we think the peer has.
func (cn *PeerConn) remotePieceCount() pieceIndex {
	have := pieceIndex(cn.peerPieces().GetCardinality())
	if all, _ := cn.peerHasAllPieces(); all {
		have = cn.bestPeerNumPieces()
	}
	return have
}

func (cn *PeerConn) completedString() string {
	return fmt.Sprintf("%d/%d", cn.remotePieceCount(), cn.bestPeerNumPieces())
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

// Writes the status every peer type has, around the lines and extra output of the type calling it.
func (cp *commonPeer) writeStatus(w io.Writer, lines []string, writeExtra func(io.Writer)) {
	// \t isn't preserved in <pre> blocks?
	if cp.closed.IsSet() {
		fmt.Fprint(w, "CLOSED: ")
	}
	fmt.Fprintln(w, strings.Join(lines, "\n"))
	if writeExtra != nil {
		writeExtra(w)
	}
	fmt.Fprintf(w,
		"%d pieces touched, good chunks: %v/%v, dr: %.1f KiB/s\n",
		len(cp.peerTouchedPieces),
		&cp._stats.ChunksReadUseful,
		&cp._stats.ChunksRead,
		cp.downloadRate()/(1<<10),
	)
	fmt.Fprintf(w, "\n")
}

// Marks the peer closed and cancels its context. Returns false if it was already closed. Each peer
// type's close starts with this, and then undoes what that type added to its Torrent.
func (cp *commonPeer) beginClose() bool {
	if !cp.closed.Set() {
		return false
	}
	// Not set until Torrent is known.
	if cp.closedCtx != nil {
		cp.closedCtxCancel()
	}
	return true
}

func (p *commonPeer) common() *commonPeer {
	return p
}

// Peer definitely has a piece, for purposes of requesting. So it's not sufficient that we think
// they do (known=true).
func peerHasPiece(p peerPieceClaims, piece pieceIndex) bool {
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
func (cp *commonPeer) modifyRelevantConnStats(f func(*ConnStats)) {
	// Every peer has basic ConnStats for now.
	f(&cp._stats)
	incAll := func(stats *ConnStats) bool {
		f(stats)
		return true
	}
	cp.upstreamConnStats()(incAll)
}

// Yields relevant upstream ConnStats. Skips Torrent if it isn't set.
func (cp *commonPeer) upstreamConnStats() iter.Seq[*ConnStats] {
	return func(yield func(*ConnStats) bool) {
		// PeerConn can be nil when it hasn't completed handshake.
		if cp.t != nil {
			cp.relevantConnStats(&cp.t.connStats)(yield)
		}
		cp.relevantConnStats(&cp.cl.connStats)(yield)
	}
}

func (cp *commonPeer) readBytes(n int64) {
	cp.modifyRelevantConnStats(add(n, func(cs *ConnStats) *Count { return &cs.BytesRead }))
}

func (c *PeerConn) lastHelpful() (ret time.Time) {
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

func (cp *commonPeer) doChunkReadStats(size int64) {
	cp.modifyRelevantConnStats(func(cs *ConnStats) { cs.receivedChunk(size) })
}

// A received chunk that has been accepted and must be written to storage. Created by
// Torrent.acceptChunk, and finished with Torrent.finishChunkWrite once write has returned. The chunk
// data is borrowed from the message, so it must stay valid until then.
type pendingChunkWrite struct {
	t *Torrent
	// Who sent the chunk.
	from  Peer
	req   RequestIndex
	piece Piece
	index int
	begin int64
	data  []byte
	// Holds the pending-write reference until the write is finished, so hashing and storage readers
	// wait for the write, and the tracking struct can't be reclaimed while the write is in flight.
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

// Receiving a chunk is driven by the peer type it arrived on, in these steps, with the Client lock
// held except for the write:
//
//  1. Torrent.validateReceivedChunk
//  2. Anything the peer type checks for itself, like whether it asked for the chunk.
//  3. Torrent.acceptChunk
//  4. pendingChunkWrite.write, without the Client lock.
//  5. Torrent.finishChunkWrite
//
// TODO: Avoid shoehorning into a pp.Message.

// Counts a received chunk and checks that it's one the Torrent could have asked for.
func (t *Torrent) validateReceivedChunk(msg *pp.Message) (ppReq Request, req RequestIndex, err error) {
	ChunksReceived.Add("total", 1)
	ppReq = newRequestFromMessage(msg)
	err = t.checkValidReceiveChunk(ppReq)
	if err != nil {
		err = log.WithLevel(log.Warning, err)
		return
	}
	req = t.requestIndexFromRequest(ppReq)
	return
}

// Takes a valid chunk from p, and updates state as though it's already written, so it isn't
// requested from anyone else while the write is in flight. intended is whether p was still wanted to
// send it. Returns nil if the chunk doesn't need writing. Called with the Client lock held. Every
// non-nil result must be passed to finishChunkWrite after its write.
func (t *Torrent) acceptChunk(p Peer, msg *pp.Message, intended bool) *pendingChunkWrite {
	cp := p.common()
	ppReq := newRequestFromMessage(msg)
	req := t.requestIndexFromRequest(ppReq)

	// This needs to occur for every valid chunk, but we try to do it when the client is unlocked
	// (in pendingChunkWrite.write). It can't be done before checking if chunks are valid because
	// they won't be deallocated from the smart ban cache by piece hashing if they're out of bounds.
	data := msg.Piece

	// Do we actually want this chunk?
	if t.haveChunk(ppReq) {
		// panic(fmt.Sprintf("%+v", ppReq))
		ChunksReceived.Add("redundant", 1)
		cp.modifyRelevantConnStats(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadWasted }))
		cp.recordBlockForSmartBan(req, data)
		return nil
	}

	piece := t.piece(ppReq.Index.Int())

	cp.modifyRelevantConnStats(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadUseful }))
	cp.modifyRelevantConnStats(add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulData }))
	if intended {
		cp.modifyRelevantConnStats(add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulIntendedData }))
	}
	for _, f := range t.cl.config.Callbacks.ReceivedUsefulData {
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
		if Peer(other) == p {
			other.logger.Slogger().Error("received chunk but still pending request", "peer", other, "req", req)
			panic("should not be pending request from conn that just received it")
		}
		other.cancel(req)
	}

	return &pendingChunkWrite{
		t:     t,
		from:  p,
		req:   req,
		piece: piece,
		index: int(msg.Index),
		begin: int64(msg.Begin),
		data:  data,
		pw:    pw,
		recordBlockForSmartBan: sync.OnceFunc(func() {
			cp.recordBlockForSmartBan(req, data)
		}),
	}
}

// Applies the result of writing a chunk. Called with the Client lock held.
func (t *Torrent) finishChunkWrite(w *pendingChunkWrite, err error) {
	w.pw.decrementPendingWrites()
	if err != nil {
		w.from.common().logger.WithDefaultLevel(log.Error).Printf("writing received chunk %v: %v", w.req, err)
		t.pendRequest(w.req)
		// Necessary to pass TestReceiveChunkStorageFailureSeederFastExtensionDisabled. I think a
		// request update runs while we're writing the chunk that just failed. Then we never do a
		// fresh update after pending the failed request.
		w.from.onNeedUpdateRequests("Peer.receiveChunk error writing chunk")
		t.onWriteChunkErr(err)
		return
	}

	t.onDirtiedPiece(w.from, w.index)

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

func (t *Torrent) onDirtiedPiece(p Peer, piece pieceIndex) {
	cp := p.common()
	if cp.peerTouchedPieces == nil {
		cp.peerTouchedPieces = make(map[pieceIndex]struct{})
	}
	cp.peerTouchedPieces[piece] = struct{}{}
	ds := &t.pieces[piece].dirtiers
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

func (c *PeerConn) peerPriority() (peerPriority, error) {
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

func (p *PeerConn) decPeakRequests() {
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
func (cp *commonPeer) relevantConnStats(all *AllConnStats) iter.Seq[*ConnStats] {
	return func(yield func(*ConnStats) bool) {
		yield(&all.ConnStats)
		yield(cp.kind.connStats(all))
	}
}
