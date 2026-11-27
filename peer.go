package torrent

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/RoaringBitmap/roaring/v2"
	"github.com/anacrolix/log"
	"github.com/anacrolix/multiless"

	peerWire "github.com/anacrolix/torrent/internal/peer-wire"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

type (
	// A peer of a Torrent, connected over the BitTorrent protocol. Web seeds aren't peers, see
	// WebSeed.
	Peer = PeerConn

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
func (p *PeerConn) Torrent() *Torrent {
	return p.t
}

func (cn *PeerConn) locker() *lockWithDeferreds {
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

// Deprecated: Use Peer.Stats.
func (p *PeerConn) DownloadRate() float64 {
	p.locker().RLock()
	defer p.locker().RUnlock()
	return p.downloadRate()
}

// Peer definitely has a piece, for purposes of requesting. So it's not sufficient that we think
// they do (known=true).
func (p *PeerConn) peerHasPiece(piece pieceIndex) bool {
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

// The function takes a message to be sent, and returns true if more messages
// are okay.
type messageWriter func(pp.Message) bool

// All ConnStats that include this connection. Some objects are not known until the handshake is
// complete, after which it's expected to reconcile the differences.
func (cn *PeerConn) modifyRelevantConnStats(f func(*ConnStats)) {
	cn.modifyConnStats(cn.t, cn.cl, f)
}

func (cn *PeerConn) readBytes(n int64) {
	cn.modifyRelevantConnStats(add(n, func(cs *ConnStats) *Count { return &cs.BytesRead }))
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

func (cn *PeerConn) doChunkReadStats(size int64) {
	cn.modifyRelevantConnStats(func(cs *ConnStats) { cs.receivedChunk(size) })
}

// A received chunk that has been accepted and must be written to storage. Created by
// Torrent.acceptChunk, and finished with Torrent.finishChunkWrite once write has returned. The chunk
// data is borrowed from the message, so it must stay valid until then.
type pendingChunkWrite struct {
	t *Torrent
	// Who sent the chunk.
	from  chunkProvider
	req   RequestIndex
	piece Piece
	index int
	begin int64
	data  []byte
	// Holds the pending-write reference until the write is finished, so hashing and storage readers
	// wait for the write, and the tracking struct can't be reclaimed while the write is in flight.
	pw *piecePendingWrites
	// Set by a PeerConn, to record the block for smart banning. It needs to occur for every valid
	// chunk, but we try to do it when the client is unlocked (in write). It can't be done before
	// checking if chunks are valid because they won't be deallocated from the smart ban cache by
	// piece hashing if they're out of bounds.
	recordBlockForSmartBan func()
}

// Writes the chunk to storage. Must be called without the Client lock.
func (w *pendingChunkWrite) write() error {
	// Opportunistically do this here while we aren't holding the client lock.
	if w.recordBlockForSmartBan != nil {
		w.recordBlockForSmartBan()
	}
	concurrentChunkWrites.Add(1)
	defer concurrentChunkWrites.Add(-1)
	return w.t.writeChunk(w.index, w.begin, w.data)
}

// Receiving a chunk is driven by the peer type it arrived on, in these steps, with the Client lock
// held except for the write:
//
//  1. Torrent.validateReceivedChunk
//  2. Anything the source's type checks for itself, like whether it asked for the chunk.
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

// Takes a valid chunk from the given source, and updates state as though it's already written, so
// it isn't requested from anyone else while the write is in flight. intended is whether the source
// was still wanted to send it. Returns nil if the chunk isn't wanted. Called with the Client lock
// held. Every non-nil result must be passed to finishChunkWrite after its write.
func (t *Torrent) acceptChunk(from chunkProvider, msg *pp.Message, intended bool) *pendingChunkWrite {
	cs := from.source()
	ppReq := newRequestFromMessage(msg)
	req := t.requestIndexFromRequest(ppReq)
	modify := func(f func(*ConnStats)) { cs.modifyConnStats(t, t.cl, f) }

	// Do we actually want this chunk?
	if t.haveChunk(ppReq) {
		// panic(fmt.Sprintf("%+v", ppReq))
		ChunksReceived.Add("redundant", 1)
		modify(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadWasted }))
		return nil
	}

	piece := t.piece(ppReq.Index.Int())

	modify(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadUseful }))
	modify(add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulData }))
	if intended {
		modify(add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulIntendedData }))
	}
	cs.lastUsefulChunkReceived = time.Now()

	// Record that the chunk hasn't been written yet before we do anything with it.
	pw := piece.incrementPendingWrites()
	// Record that we have the chunk, so we aren't trying to download it while waiting for it to be
	// written to storage.
	piece.unpendChunkIndex(chunkIndexFromChunkSpec(ppReq.ChunkSpec, t.chunkSize))

	// Cancel pending requests for this chunk from *other* peers.
	if other := t.requestingPeer(req); other != nil {
		if chunkProvider(other) == from {
			other.logger.Slogger().Error("received chunk but still pending request", "peer", other, "req", req)
			panic("should not be pending request from conn that just received it")
		}
		other.cancel(req)
	}

	return &pendingChunkWrite{
		t:     t,
		from:  from,
		req:   req,
		piece: piece,
		index: int(msg.Index),
		begin: int64(msg.Begin),
		data:  msg.Piece,
		pw:    pw,
	}
}

// Applies the result of writing a chunk. Called with the Client lock held.
func (t *Torrent) finishChunkWrite(w *pendingChunkWrite, err error) {
	w.pw.decrementPendingWrites()
	if err != nil {
		t.logger.WithDefaultLevel(log.Error).Printf("writing chunk %v received from %v: %v", w.req, w.from, err)
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

func (t *Torrent) onDirtiedPiece(p chunkProvider, piece pieceIndex) {
	cp := p.source()
	if cp.peerTouchedPieces == nil {
		cp.peerTouchedPieces = make(map[pieceIndex]struct{})
	}
	cp.peerTouchedPieces[piece] = struct{}{}
	ds := &t.pieces[piece].dirtiers
	if *ds == nil {
		*ds = make(map[chunkProvider]struct{})
	}
	(*ds)[p] = struct{}{}
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

func (c *PeerConn) remoteIp() net.IP {
	host, _, _ := net.SplitHostPort(c.RemoteAddr.String())
	// Link-local IPv6 addresses can carry a zone, which net.ParseIP doesn't accept.
	host, _, _ = strings.Cut(host, "%")
	return net.ParseIP(host)
}

func (c *PeerConn) remoteIpPort() IpPort {
	ipa, _ := tryIpPortFromNetAddr(c.RemoteAddr)
	return IpPort{ipa.IP, uint16(ipa.Port)}
}

func (c *PeerConn) trust() connectionTrust {
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

func (p *PeerConn) recordBlockForSmartBan(req RequestIndex, blockData []byte) {
	if p.bannableAddr.Ok {
		p.t.smartBanCache.RecordBlock(p.bannableAddr.Value, req, blockData)
	}
}
