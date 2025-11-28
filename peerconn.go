package torrent

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"weak"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/chansync"
	"github.com/anacrolix/generics"
	. "github.com/anacrolix/generics"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2/bitmap"
	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/multiless"

	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/bencode"
	requestStrategy "github.com/anacrolix/torrent/internal/request-strategy"

	"github.com/anacrolix/torrent/merkle"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/mse"
	pp "github.com/anacrolix/torrent/peer_protocol"
	utHolepunch "github.com/anacrolix/torrent/peer_protocol/ut-holepunch"
)

type PeerStatus struct {
	Id  PeerID
	Ok  bool
	Err string // see https://github.com/golang/go/issues/5161
}

// Maintains the state of a BitTorrent-protocol based connection with a peer.
type PeerConn struct {
	Peer

	// Indexed by metadata piece, set to true if posted and pending a response.
	metadataRequests []bool
	sentHaves        bitmap.Bitmap
	// Chunks that we might reasonably expect to receive from the peer. Due to latency, buffering,
	// and implementation differences, we may receive chunks that are no longer in the set of
	// requests actually want. This could use a roaring.BSI if the memory use becomes noticeable.
	validReceiveChunks map[RequestIndex]int
	PeerMaxRequests    maxRequests // Maximum pending requests the peer allows.

	// Move to PeerConn?
	protocolLogger log.Logger

	// BEP 52
	v2 bool

	// A string that should identify the PeerConn's net.Conn endpoints. The net.Conn could
	// be wrapping WebRTC, uTP, or TCP etc. Used in writing the conn status for peers.
	connString string

	// See BEP 3 etc.
	PeerID             PeerID
	PeerExtensionBytes pp.PeerExtensionBits
	PeerListenPort     int

	// The local extended protocols to advertise in the extended handshake, and to support receiving
	// from the peer. This will point to the Client default when the PeerConnAdded callback is
	// invoked. Do not modify this, point it to your own instance. Do not modify the destination
	// after returning from the callback.
	LocalLtepProtocolMap *LocalLtepProtocolMap

	// The actual Conn, used for closing, and setting socket options. Do not use methods on this
	// while holding any mutexes.
	conn net.Conn
	// The Reader and Writer for this Conn, with hooks installed for stats,
	// limiting, deadlines etc.
	w io.Writer
	r io.Reader

	messageWriter peerConnMsgWriter

	// The peer's extension map, as sent in their extended handshake.
	PeerExtensionIDs map[pp.ExtensionName]pp.ExtensionNumber
	PeerClientName   atomic.Value
	uploadTimer      *time.Timer
	pex              pexConnState

	// The pieces the peer has claimed to have.
	_peerPieces roaring.Bitmap
	// The peer has everything. This can occur due to a special message, when
	// we may not even know the number of pieces in the torrent yet.
	peerSentHaveAll bool

	requestState requestStrategy.PeerRequestState

	outstandingHolepunchingRendezvous map[netip.AddrPort]struct{}

	// Hash requests sent to the peer. If there's an issue we probably don't want to reissue these,
	// because I haven't implemented it smart enough yet.
	sentHashRequests map[hashRequest]struct{}
	// Hash pieces received from the peer, mapped from pieces root to piece layer hashes. This way
	// we can verify all the pieces for a file when they're all arrived before submitting them to
	// the torrent.
	receivedHashPieces map[[32]byte][][32]byte

	// Requests from the peer that haven't yet been read from storage for upload.
	unreadPeerRequests map[Request]struct{}
	// Peer request data that's ready to be uploaded.
	readyPeerRequests map[Request][]byte
	// Total peer request data buffered has decreased, so the server can read more.
	peerRequestDataAllocDecreased chansync.BroadcastCond
	// A routine is handling buffering peer request data.
	peerRequestServerRunning bool

	// Set true after we've added our ConnStats generated during handshake to other ConnStat
	// instances as determined when the *Torrent became known.
	reconciledHandshakeStats bool
}

func (*PeerConn) allConnStatsImplField(stats *AllConnStats) *ConnStats {
	return &stats.PeerConns
}

func (cn *PeerConn) lastWriteUploadRate() float64 {
	cn.messageWriter.mu.Lock()
	defer cn.messageWriter.mu.Unlock()
	return cn.messageWriter.dataUploadRate
}

func (cn *PeerConn) pexStatus() string {
	if !cn.bitExtensionEnabled(pp.ExtensionBitLtep) {
		return "extended protocol disabled"
	}
	if cn.PeerExtensionIDs == nil {
		return "pending extended handshake"
	}
	if !cn.supportsExtension(pp.ExtensionNamePex) {
		return "unsupported"
	}
	return fmt.Sprintf(
		"%v conns, %v unsent events",
		len(cn.pex.remoteLiveConns),
		cn.pex.numPending(),
	)
}

func (cn *PeerConn) peerImplStatusLines() []string {
	return []string{
		cn.connString,
		fmt.Sprintf("peer id: %+q", cn.PeerID),
		fmt.Sprintf("extensions: %v", cn.PeerExtensionBytes),
		fmt.Sprintf("ltep extensions: %v", cn.PeerExtensionIDs),
		fmt.Sprintf("pex: %s", cn.pexStatus()),
		fmt.Sprintf(
			"reqq: %d+%v/(%d/%d):%d/%d, flags: %s",
			cn.requestState.Requests.GetCardinality(),
			cn.requestState.Cancelled.GetCardinality(),
			cn.nominalMaxRequests(),
			cn.PeerMaxRequests,
			cn.numPeerRequests(),
			localClientReqq,
			cn.statusFlags(),
		),
	}
}

// Returns true if the connection is over IPv6.
func (cn *PeerConn) ipv6() bool {
	ip := cn.remoteIp()
	if ip.To4() != nil {
		return false
	}
	return len(ip) == net.IPv6len
}

// Returns true the if the dialer/initiator has the higher client peer ID. See
// https://github.com/arvidn/libtorrent/blame/272828e1cc37b042dfbbafa539222d8533e99755/src/bt_peer_connection.cpp#L3536-L3557.
// As far as I can tell, Transmission just keeps the oldest connection.
func (cn *PeerConn) isPreferredDirection() bool {
	// True if our client peer ID is higher than the remote's peer ID.
	return bytes.Compare(cn.PeerID[:], cn.t.cl.peerID[:]) < 0 == cn.outgoing
}

// Returns whether the left connection should be preferred over the right one,
// considering only their networking properties. If ok is false, we can't
// decide.
func (l *PeerConn) hasPreferredNetworkOver(r *PeerConn) bool {
	var ml multiless.Computation
	ml = ml.Bool(r.isPreferredDirection(), l.isPreferredDirection())
	ml = ml.Bool(l.utp(), r.utp())
	ml = ml.Bool(r.ipv6(), l.ipv6())
	return ml.Less()
}

func (cn *PeerConn) peerHasAllPieces() (all, known bool) {
	if cn.peerSentHaveAll {
		return true, true
	}
	if !cn.t.haveInfo() {
		return false, false
	}
	return cn._peerPieces.GetCardinality() == uint64(cn.t.numPieces()), true
}

func (cn *PeerConn) onGotInfo(info *metainfo.Info) {
	cn.setNumPieces(info.NumPieces())
}

// Correct the PeerPieces slice length. Return false if the existing slice is invalid, such as by
// receiving badly sized BITFIELD, or invalid HAVE messages.
func (cn *PeerConn) setNumPieces(num pieceIndex) {
	cn._peerPieces.RemoveRange(bitmap.BitRange(num), bitmap.ToEnd)
	cn.peerPiecesChanged()
}

func (cn *PeerConn) peerPieces() *roaring.Bitmap {
	return &cn._peerPieces
}

func (cn *PeerConn) connectionFlags() string {
	var sb strings.Builder
	add := func(s string) {
		if sb.Len() > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(s)
	}
	// From first relevant to last.
	add(string(cn.Discovery))
	if cn.utp() {
		add("U")
	}
	if cn.cryptoMethod == mse.CryptoMethodRC4 {
		add("E")
	} else if cn.headerEncrypted {
		add("e")
	}
	if cn.v2 {
		add("v2")
	} else {
		add("v1")
	}
	return sb.String()
}

func (cn *PeerConn) utp() bool {
	return parseNetworkString(cn.Network).Udp
}

func (cn *PeerConn) onClose() {
	if cn.pex.IsEnabled() {
		cn.pex.Close()
	}
	cn.tickleWriter()
	if cn.conn != nil {
		go cn.conn.Close()
	}
	if cb := cn.callbacks.PeerConnClosed; cb != nil {
		cb(cn)
	}
}

// Writes a message into the write buffer. Returns whether it's okay to keep writing. Writing is
// done asynchronously, so it may be that we're not able to honour backpressure from this method.
func (cn *PeerConn) write(msg pp.Message) bool {
	torrent.Add(fmt.Sprintf("messages written of type %s", msg.Type.String()), 1)
	// We don't need to track bytes here because the connection's Writer has that behaviour injected
	// (although there's some delay between us buffering the message, and the connection writer
	// flushing it out.).
	notFull := cn.messageWriter.write(msg)
	// Last I checked only Piece messages affect stats, and we don't write those.
	cn.wroteMsg(&msg)
	cn.tickleWriter()
	return notFull
}

func (cn *PeerConn) requestMetadataPiece(index int) {
	eID := cn.PeerExtensionIDs[pp.ExtensionNameMetadata]
	if eID == pp.ExtensionDeleteNumber {
		return
	}
	if index < len(cn.metadataRequests) && cn.metadataRequests[index] {
		return
	}
	cn.protocolLogger.WithDefaultLevel(log.Debug).Printf("requesting metadata piece %d", index)
	cn.write(pp.MetadataExtensionRequestMsg(eID, index))
	for index >= len(cn.metadataRequests) {
		cn.metadataRequests = append(cn.metadataRequests, false)
	}
	cn.metadataRequests[index] = true
}

func (cn *PeerConn) requestedMetadataPiece(index int) bool {
	return index < len(cn.metadataRequests) && cn.metadataRequests[index]
}

func (cn *PeerConn) onPeerSentCancel(r Request) {
	if !cn.havePeerRequest(r) {
		torrent.Add("unexpected cancels received", 1)
		return
	}
	if cn.fastEnabled() {
		cn.reject(r)
	} else {
		cn.deletePeerRequest(r)
	}
}

func (me *PeerConn) deletePeerRequest(r Request) {
	delete(me.unreadPeerRequests, r)
	me.deleteReadyPeerRequest(r)
}

func (me *PeerConn) havePeerRequest(r Request) bool {
	return MapContains(me.unreadPeerRequests, r) || MapContains(me.readyPeerRequests, r)
}

func (cn *PeerConn) choke(msg messageWriter) (more bool) {
	if cn.choking {
		return true
	}
	cn.choking = true
	more = msg(pp.Message{
		Type: pp.Choke,
	})
	if !cn.fastEnabled() {
		cn.deleteAllPeerRequests()
	}
	return
}

func (cn *PeerConn) deleteAllPeerRequests() {
	clear(cn.unreadPeerRequests)
	clear(cn.readyPeerRequests)
}

func (cn *PeerConn) unchoke(msg func(pp.Message) bool) bool {
	if !cn.choking {
		return true
	}
	cn.choking = false
	return msg(pp.Message{
		Type: pp.Unchoke,
	})
}

func (pc *PeerConn) writeInterested(interested bool) bool {
	return pc.write(pp.Message{
		Type: func() pp.MessageType {
			if interested {
				return pp.Interested
			} else {
				return pp.NotInterested
			}
		}(),
	})
}

// The final piece to actually commit to a request. Typically, this sends or begins handling the
// request.
func (me *PeerConn) _request(r Request) bool {
	return me.write(pp.Message{
		Type:   pp.Request,
		Index:  r.Index,
		Begin:  r.Begin,
		Length: r.Length,
	})
}

func (me *PeerConn) handleCancel(r RequestIndex) {
	me.write(makeCancelMessage(me.t.requestIndexToRequest(r)))
	if me.remoteRejectsCancels() {
		// Record that we expect to get a cancel ack.
		if !me.requestState.Cancelled.CheckedAdd(r) {
			panic("request already cancelled")
		}
	}
}

// Whether we should expect a reject message after sending a cancel.
func (me *PeerConn) remoteRejectsCancels() bool {
	if !me.fastEnabled() {
		return false
	}
	if me.remoteIsTransmission() {
		// Transmission did not send rejects for received cancels. See
		// https://github.com/transmission/transmission/pull/2275. Fixed in 4.0.0-beta.1 onward in
		// https://github.com/transmission/transmission/commit/76719bf34c255da4fca991c2ad3fa4b65d2154b1.
		// Peer ID prefix scheme described
		// https://github.com/transmission/transmission/blob/7ec7607bbcf0fa99bd4b157b9b0f0c411d59f45d/CMakeLists.txt#L128-L149.
		return me.PeerID[3] >= '4'
	}
	return true
}

func (cn *PeerConn) fillWriteBuffer() {
	if cn.messageWriter.writeBuffer.Len() > writeBufferLowWaterLen {
		// Fully committing to our max requests requires sufficient space (see
		// maxLocalToRemoteRequests). Flush what we have instead. We also prefer always to make
		// requests than to do PEX or upload, so we short-circuit before handling those. Any update
		// request reason will not be cleared, so we'll come right back here when there's space. We
		// can't do this in maybeUpdateActualRequestState because it's a method on Peer and has no
		// knowledge of write buffers.
		return
	}
	cn.requestMissingHashes()
	cn.maybeUpdateActualRequestState()
	if cn.pex.IsEnabled() {
		if flow := cn.pex.Share(cn.write); !flow {
			return
		}
	}
	cn.upload(cn.write)
}

func (cn *PeerConn) have(piece pieceIndex) {
	if cn.sentHaves.Get(bitmap.BitIndex(piece)) {
		return
	}
	cn.write(pp.Message{
		Type:  pp.Have,
		Index: pp.Integer(piece),
	})
	cn.sentHaves.Add(bitmap.BitIndex(piece))
}

func (cn *PeerConn) postBitfield() {
	if cn.sentHaves.Len() != 0 {
		panic("bitfield must be first have-related message sent")
	}
	if !cn.t.haveAnyPieces() {
		return
	}
	cn.write(pp.Message{
		Type:     pp.Bitfield,
		Bitfield: cn.t.bitfield(),
	})
	cn.sentHaves = bitmap.Bitmap{cn.t._completedPieces.Clone()}
}

func (cn *PeerConn) handleOnNeedUpdateRequests() {
	// The writer determines the request state as needed when it can write.
	cn.tickleWriter()
}

func (cn *PeerConn) raisePeerMinPieces(newMin pieceIndex) {
	if newMin > cn.peerMinPieces {
		cn.peerMinPieces = newMin
	}
}

func (cn *PeerConn) peerSentHave(piece pieceIndex) error {
	if cn.t.haveInfo() && piece >= cn.t.numPieces() || piece < 0 {
		return errors.New("invalid piece")
	}
	if cn.peerHasPiece(piece) {
		return nil
	}
	cn.raisePeerMinPieces(piece + 1)
	if !cn.peerHasPiece(piece) {
		cn.t.incPieceAvailability(piece)
	}
	cn._peerPieces.Add(uint32(piece))
	if cn.t.wantPieceIndex(piece) {
		cn.onNeedUpdateRequests("have")
	}
	cn.peerPiecesChanged()
	return nil
}

func (cn *PeerConn) peerSentBitfield(bf []bool) error {
	if len(bf)%8 != 0 {
		panic("expected bitfield length divisible by 8")
	}
	// We know that the last byte means that at most the last 7 bits are wasted.
	cn.raisePeerMinPieces(pieceIndex(len(bf) - 7))
	if cn.t.haveInfo() && len(bf) > int(cn.t.numPieces()) {
		// Ignore known excess pieces.
		bf = bf[:cn.t.numPieces()]
	}
	bm := boolSliceToBitmap(bf)
	if cn.t.haveInfo() && pieceIndex(bm.GetCardinality()) == cn.t.numPieces() {
		cn.onPeerHasAllPieces()
		return nil
	}
	if !bm.IsEmpty() {
		cn.raisePeerMinPieces(pieceIndex(bm.Maximum()) + 1)
	}
	shouldUpdateRequests := false
	if cn.peerSentHaveAll {
		if !cn.t.deleteConnWithAllPieces(&cn.Peer) {
			panic(cn)
		}
		cn.peerSentHaveAll = false
		if !cn._peerPieces.IsEmpty() {
			panic("if peer has all, we expect no individual peer pieces to be set")
		}
	} else {
		bm.Xor(&cn._peerPieces)
	}
	cn.peerSentHaveAll = false
	// bm is now 'on' for pieces that are changing
	bm.Iterate(func(x uint32) bool {
		pi := pieceIndex(x)
		if cn._peerPieces.Contains(x) {
			// Then we must be losing this piece
			cn.t.decPieceAvailability(pi)
		} else {
			if !shouldUpdateRequests && cn.t.wantPieceIndex(pieceIndex(x)) {
				shouldUpdateRequests = true
			}
			// We must be gaining this piece
			cn.t.incPieceAvailability(pieceIndex(x))
		}
		return true
	})
	// Apply the changes. If we had everything previously, this should be empty, so xor is the same
	// as or.
	cn._peerPieces.Xor(&bm)
	if shouldUpdateRequests {
		cn.onNeedUpdateRequests("bitfield")
	}
	// We didn't guard this before, I see no reason to do it now.
	cn.peerPiecesChanged()
	return nil
}

func (cn *PeerConn) onPeerHasAllPiecesNoTriggers() {
	t := cn.t
	if t.haveInfo() {
		cn._peerPieces.Iterate(func(x uint32) bool {
			t.decPieceAvailability(pieceIndex(x))
			return true
		})
	}
	t.addConnWithAllPieces(&cn.Peer)
	cn.peerSentHaveAll = true
	cn._peerPieces.Clear()
}

func (cn *PeerConn) onPeerHasAllPieces() {
	cn.onPeerHasAllPiecesNoTriggers()
	cn.peerHasAllPiecesTriggers()
}

func (cn *PeerConn) peerHasAllPiecesTriggers() {
	if !cn.t._pendingPieces.IsEmpty() {
		cn.onNeedUpdateRequests("Peer.onPeerHasAllPieces")
	}
	cn.peerPiecesChanged()
}

func (cn *PeerConn) onPeerSentHaveAll() error {
	cn.onPeerHasAllPieces()
	return nil
}

func (cn *PeerConn) peerSentHaveNone() error {
	if !cn.peerSentHaveAll {
		cn.t.decPeerPieceAvailability(&cn.Peer)
	}
	cn._peerPieces.Clear()
	cn.peerSentHaveAll = false
	cn.peerPiecesChanged()
	return nil
}

func (c *PeerConn) requestPendingMetadata() {
	if c.t.haveInfo() {
		return
	}
	if c.PeerExtensionIDs[pp.ExtensionNameMetadata] == 0 {
		// Peer doesn't support this.
		return
	}
	// Request metadata pieces that we don't have in a random order.
	var pending []int
	for index := 0; index < c.t.metadataPieceCount(); index++ {
		if !c.t.haveMetadataPiece(index) && !c.requestedMetadataPiece(index) {
			pending = append(pending, index)
		}
	}
	rand.Shuffle(len(pending), func(i, j int) { pending[i], pending[j] = pending[j], pending[i] })
	for _, i := range pending {
		c.requestMetadataPiece(i)
	}
}

func (cn *PeerConn) wroteMsg(msg *pp.Message) {
	torrent.Add(fmt.Sprintf("messages written of type %s", msg.Type.String()), 1)
	if msg.Type == pp.Extended {
		for name, id := range cn.PeerExtensionIDs {
			if id != msg.ExtendedID {
				continue
			}
			torrent.Add(fmt.Sprintf("Extended messages written for protocol %q", name), 1)
		}
	}
	cn.modifyRelevantConnStats(func(cs *ConnStats) { cs.wroteMsg(msg) })
}

func (cn *PeerConn) wroteBytes(n int64) {
	cn.modifyRelevantConnStats(add(n, func(cs *ConnStats) *Count { return &cs.BytesWritten }))
}

func (c *PeerConn) fastEnabled() bool {
	return c.PeerExtensionBytes.SupportsFast() && c.t.cl.config.Extensions.SupportsFast()
}

func (c *PeerConn) reject(r Request) {
	if !c.fastEnabled() {
		panic("fast not enabled")
	}
	c.write(r.ToMsg(pp.Reject))
	// It is possible to reject a request before it is added to peer requests due to being invalid.
	c.deletePeerRequest(r)
}

func (c *PeerConn) maximumPeerRequestChunkLength() (_ Option[int]) {
	uploadRateLimiter := c.t.cl.config.UploadRateLimiter
	if uploadRateLimiter.Limit() == rate.Inf {
		return
	}
	return Some(uploadRateLimiter.Burst())
}

func (me *PeerConn) numPeerRequests() int {
	return len(me.unreadPeerRequests) + len(me.readyPeerRequests)
}

// startFetch is for testing purposes currently.
func (c *PeerConn) onReadRequest(r Request, startFetch bool) error {
	requestedChunkLengths.Add(strconv.FormatUint(r.Length.Uint64(), 10), 1)
	if c.havePeerRequest(r) {
		torrent.Add("duplicate requests received", 1)
		if c.fastEnabled() {
			return errors.New("received duplicate request with fast enabled")
		}
		return nil
	}
	if c.choking {
		torrent.Add("requests received while choking", 1)
		if c.fastEnabled() {
			torrent.Add("requests rejected while choking", 1)
			c.reject(r)
		}
		return nil
	}
	// TODO: What if they've already requested this?
	if c.numPeerRequests() >= localClientReqq {
		torrent.Add("requests received while queue full", 1)
		if c.fastEnabled() {
			c.reject(r)
		}
		// BEP 6 says we may close here if we choose.
		return nil
	}
	if opt := c.maximumPeerRequestChunkLength(); opt.Ok && int(r.Length) > opt.Value {
		err := fmt.Errorf("peer requested chunk too long (%v)", r.Length)
		// Brother ewwww...
		c.protocolLogger.Levelf(log.Warning, "%v", err.Error())
		if c.fastEnabled() {
			c.reject(r)
			return nil
		} else {
			return err
		}
	}
	if !c.t.havePiece(pieceIndex(r.Index)) {
		// TODO: Tell the peer we don't have the piece, and reject this request.
		requestsReceivedForMissingPieces.Add(1)
		return fmt.Errorf("peer requested piece we don't have: %v", r.Index.Int())
	}
	pieceLength := c.t.pieceLength(pieceIndex(r.Index))
	// Check this after we know we have the piece, so that the piece length will be known.
	if chunkOverflowsPiece(r.ChunkSpec, pieceLength) {
		torrent.Add("bad requests received", 1)
		return errors.New("chunk overflows piece")
	}
	MakeMapIfNilWithCap(&c.unreadPeerRequests, localClientReqq)
	c.unreadPeerRequests[r] = struct{}{}
	if startFetch {
		c.startPeerRequestServer()
	}
	return nil
}

func (c *PeerConn) startPeerRequestServer() {
	if !c.peerRequestServerRunning {
		go c.peerRequestServer()
		c.peerRequestServerRunning = true
	}
}

func (c *PeerConn) peerRequestServer() {
	c.locker().Lock()
again:
	if !c.closed.IsSet() {
		for r := range c.unreadPeerRequests {
			c.servePeerRequest(r)
			goto again
		}
	}
	panicif.False(c.peerRequestServerRunning)
	c.peerRequestServerRunning = false
	c.locker().Unlock()
}

func (c *PeerConn) peerRequestDataBuffered() (n int) {
	// TODO: Should we include a limit to the number of individual requests to keep N small, or keep
	// a counter elsewhere?
	for r := range c.readyPeerRequests {
		n += r.Length.Int()
	}
	return
}

func (c *PeerConn) waitForDataAlloc(size int) bool {
	maxAlloc := c.t.cl.config.MaxAllocPeerRequestDataPerConn
	locker := c.locker()
	for {
		if size > maxAlloc {
			c.slogger.Warn("peer request length exceeds MaxAllocPeerRequestDataPerConn",
				"requested", size,
				"max", maxAlloc)
			return false
		}
		if c.peerRequestDataBuffered()+size <= maxAlloc {
			return true
		}
		allocDecreased := c.peerRequestDataAllocDecreased.Signaled()
		locker.Unlock()
		select {
		case <-c.closedCtx.Done():
			locker.Lock()
			return false
		case <-allocDecreased:
		}
		c.locker().Lock()
	}
}

func (me *PeerConn) deleteReadyPeerRequest(r Request) {
	v, ok := me.readyPeerRequests[r]
	if !ok {
		return
	}
	delete(me.readyPeerRequests, r)
	if len(v) > 0 {
		me.peerRequestDataAllocDecreased.Broadcast()
	}
}

// Handles an outstanding peer request. It's either rejected, or buffered for the writer.
func (c *PeerConn) servePeerRequest(r Request) {
	defer func() {
		// Prevent caller from stalling. It's either rejected or buffered.
		panicif.True(MapContains(c.unreadPeerRequests, r))
	}()
	if !c.waitForDataAlloc(r.Length.Int()) {
		// Might have been removed while unlocked.
		if MapContains(c.unreadPeerRequests, r) {
			c.useBestReject(r)
		}
		return
	}
	c.locker().Unlock()
	b, err := c.readPeerRequestData(r)
	c.locker().Lock()
	if err != nil {
		c.peerRequestDataReadFailed(err, r)
		return
	}
	if !MapContains(c.unreadPeerRequests, r) {
		c.slogger.Debug("read data for peer request but no longer wanted", "request", r)
		return
	}
	MustDelete(c.unreadPeerRequests, r)
	MakeMapIfNil(&c.readyPeerRequests)
	c.readyPeerRequests[r] = b
	c.tickleWriter()
}

// If this is maintained correctly, we might be able to support optional synchronous reading for
// chunk sending, the way it used to work.
func (c *PeerConn) peerRequestDataReadFailed(err error, r Request) {
	torrent.Add("peer request data read failures", 1)
	logLevel := log.Warning
	if c.t.hasStorageCap() || c.t.closed.IsSet() {
		// It's expected that pieces might drop. See
		// https://github.com/anacrolix/torrent/issues/702#issuecomment-1000953313. Also the torrent
		// may have been Dropped, and the user expects to own the files, see
		// https://github.com/anacrolix/torrent/issues/980.
		logLevel = log.Debug
	}
	c.logger.Levelf(logLevel, "error reading chunk for peer Request %v: %v", r, err)
	if c.t.closed.IsSet() {
		return
	}
	i := pieceIndex(r.Index)
	if c.t.pieceComplete(i) {
		// There used to be more code here that just duplicated the following break. Piece
		// completions are currently cached, so I'm not sure how helpful this update is, except to
		// pull any completion changes pushed to the storage backend in failed reads that got us
		// here.
		c.t.updatePieceCompletion(i)
	}
	// We've probably dropped a piece from storage, but there's no way to communicate this to the
	// peer. If they ask for it again, we kick them allowing us to send them updated piece states if
	// we reconnect. TODO: Instead, we could just try to update them with Bitfield or HaveNone and
	// if they kick us for breaking protocol, on reconnect we will be compliant again (at least
	// initially).
	c.useBestReject(r)
}

// Reject a peer request using the best protocol support we have available.
func (c *PeerConn) useBestReject(r Request) {
	if c.fastEnabled() {
		c.reject(r)
	} else {
		if c.choking {
			// If fast isn't enabled, I think we would have wiped all peer requests when we last
			// choked, and requests while we're choking would be ignored. It could be possible that
			// a peer request data read completed concurrently to it being deleted elsewhere.
			c.protocolLogger.WithDefaultLevel(log.Warning).Printf("already choking peer, requests might not be rejected correctly")
		}
		// Choking a non-fast peer should cause them to flush all their requests.
		c.choke(c.write)
	}
}

func (c *PeerConn) readPeerRequestData(r Request) ([]byte, error) {
	b := make([]byte, r.Length)
	p := c.t.info.Piece(int(r.Index))
	n, err := c.t.readAt(b, p.Offset()+int64(r.Begin))
	if n == len(b) {
		if errors.Is(err, io.EOF) {
			err = nil
		}
	} else {
		if err == nil {
			panic("expected error")
		}
	}
	return b, err
}

func (c *PeerConn) logProtocolBehaviour(level log.Level, format string, arg ...interface{}) {
	c.protocolLogger.WithContextText(fmt.Sprintf(
		"peer id %q, ext v %q", c.PeerID, c.PeerClientName.Load(),
	)).SkipCallers(1).Levelf(level, format, arg...)
}

// Processes incoming BitTorrent wire-protocol messages. The client lock is held upon entry and
// exit. Returning will end the connection.
func (c *PeerConn) mainReadLoop() (err error) {
	defer func() {
		if err != nil {
			torrent.Add("connection.mainReadLoop returned with error", 1)
		} else {
			torrent.Add("connection.mainReadLoop returned with no error", 1)
		}
	}()
	t := c.t
	cl := t.cl

	decoder := pp.Decoder{
		R:         bufio.NewReaderSize(c.r, 1<<17),
		MaxLength: 4 * pp.Integer(max(int64(t.chunkSize), defaultChunkSize)),
		Pool:      &t.chunkPool,
	}
	for {
		var msg pp.Message
		func() {
			cl.unlock()
			// TODO: Could TryLock and pump for more messages here until we can get the lock and
			// process them in a batch.
			defer cl.lock()
			err = decoder.Decode(&msg)
			if err != nil {
				err = fmt.Errorf("decoding message: %w", err)
			}
		}()
		// Do this before checking closed.
		if cb := c.callbacks.ReadMessage; cb != nil && err == nil {
			cb(c, &msg)
		}
		if t.closed.IsSet() || c.closed.IsSet() {
			return nil
		}
		if err != nil {
			return err
		}
		c.lastMessageReceived = time.Now()
		if msg.Keepalive {
			receivedKeepalives.Add(1)
			continue
		}
		messageTypesReceived.Add(msg.Type.String(), 1)
		if msg.Type.FastExtension() && !c.fastEnabled() {
			runSafeExtraneous(func() { torrent.Add("fast messages received when extension is disabled", 1) })
			return fmt.Errorf("received fast extension message (type=%v) but extension is disabled", msg.Type)
		}
		switch msg.Type {
		case pp.Choke:
			if c.peerChoking {
				break
			}
			if !c.fastEnabled() {
				c.deleteAllRequests("choked by non-fast PeerConn")
			} else {
				// We don't decrement pending requests here, let's wait for the peer to either
				// reject or satisfy the outstanding requests. Additionally, some peers may unchoke
				// us and resume where they left off, we don't want to have piled on to those chunks
				// in the meanwhile. I think a peer's ability to abuse this should be limited: they
				// could let us request a lot of stuff, then choke us and never reject, but they're
				// only a single peer, our chunk balancing should smooth over this abuse.
			}
			c.peerChoking = true
			c.updateExpectingChunks()
		case pp.Unchoke:
			if !c.peerChoking {
				// Some clients do this for some reason. Transmission doesn't error on this, so we
				// won't for consistency.
				c.logProtocolBehaviour(log.Debug, "received unchoke when already unchoked")
				break
			}
			c.peerChoking = false
			preservedCount := 0
			c.requestState.Requests.Iterate(func(x RequestIndex) bool {
				if !c.peerAllowedFast.Contains(c.t.pieceIndexOfRequestIndex(x)) {
					preservedCount++
				}
				return true
			})
			if preservedCount != 0 {
				// TODO: Yes this is a debug log but I'm not happy with the state of the logging lib
				// right now.
				c.protocolLogger.Levelf(log.Debug,
					"%v requests were preserved while being choked (fast=%v)",
					preservedCount,
					c.fastEnabled())

				torrent.Add("requestsPreservedThroughChoking", int64(preservedCount))
			}
			if !c.t._pendingPieces.IsEmpty() {
				c.onNeedUpdateRequests("unchoked")
			}
			c.updateExpectingChunks()
		case pp.Interested:
			c.peerInterested = true
			c.tickleWriter()
		case pp.NotInterested:
			c.peerInterested = false
			// We don't clear their requests since it isn't clear in the spec.
			// We'll probably choke them for this, which will clear them if
			// appropriate, and is clearly specified.
		case pp.Have:
			err = c.peerSentHave(pieceIndex(msg.Index))
		case pp.Bitfield:
			err = c.peerSentBitfield(msg.Bitfield)
		case pp.Request:
			r := newRequestFromMessage(&msg)
			err = c.onReadRequest(r, true)
			if err != nil {
				err = fmt.Errorf("on reading request %v: %w", r, err)
			}
		case pp.Piece:
			c.doChunkReadStats(int64(len(msg.Piece)))
			err = c.receiveChunk(&msg)
			t.putChunkBuffer(msg.Piece)
			msg.Piece = nil
			if err != nil {
				err = fmt.Errorf("receiving chunk: %w", err)
			}
		case pp.Cancel:
			req := newRequestFromMessage(&msg)
			c.onPeerSentCancel(req)
		case pp.Port:
			ipa, ok := tryIpPortFromNetAddr(c.RemoteAddr)
			if !ok {
				break
			}
			pingAddr := net.UDPAddr{
				IP:   ipa.IP,
				Port: ipa.Port,
			}
			if msg.Port != 0 {
				pingAddr.Port = int(msg.Port)
			}
			cl.eachDhtServer(func(s DhtServer) {
				go s.Ping(&pingAddr)
			})
		case pp.Suggest:
			torrent.Add("suggests received", 1)
			log.Fmsg("peer suggested piece %d", msg.Index).AddValues(c, msg.Index).LogLevel(log.Debug, c.t.logger)
			c.onNeedUpdateRequests("suggested")
		case pp.HaveAll:
			err = c.onPeerSentHaveAll()
		case pp.HaveNone:
			err = c.peerSentHaveNone()
		case pp.Reject:
			req := newRequestFromMessage(&msg)
			if !c.remoteRejectedRequest(c.t.requestIndexFromRequest(req)) {
				err = fmt.Errorf("received invalid reject for request %v", req)
				c.protocolLogger.Levelf(log.Debug, "%v", err)
			}
		case pp.AllowedFast:
			torrent.Add("allowed fasts received", 1)
			log.Fmsg("peer allowed fast: %d", msg.Index).AddValues(c).LogLevel(log.Debug, c.t.logger)
			c.onNeedUpdateRequests("PeerConn.mainReadLoop allowed fast")
		case pp.Extended:
			err = c.onReadExtendedMsg(msg.ExtendedID, msg.ExtendedPayload)
		case pp.Hashes:
			err = c.onReadHashes(&msg)
		case pp.HashRequest:
			err = c.onHashRequest(&msg)
		case pp.HashReject:
			c.protocolLogger.Levelf(log.Info, "received unimplemented BitTorrent v2 message: %v", msg.Type)
		default:
			err = fmt.Errorf("received unknown message type: %#v", msg.Type)
		}
		if err != nil {
			return err
		}
	}
}

func (c *PeerConn) onReadExtendedMsg(id pp.ExtensionNumber, payload []byte) (err error) {
	defer func() {
		// TODO: Should we still do this?
		if err != nil {
			// These clients use their own extension IDs for outgoing message
			// types, which is incorrect.
			if bytes.HasPrefix(c.PeerID[:], []byte("-SD0100-")) || strings.HasPrefix(string(c.PeerID[:]), "-XL0012-") {
				err = nil
			}
		}
	}()
	t := c.t
	cl := t.cl
	{
		event := PeerConnReadExtensionMessageEvent{
			PeerConn:        c,
			ExtensionNumber: id,
			Payload:         payload,
		}
		for _, cb := range c.callbacks.PeerConnReadExtensionMessage {
			cb(event)
		}
	}
	if id == pp.HandshakeExtendedID {
		var d pp.ExtendedHandshakeMessage
		if err := bencode.Unmarshal(payload, &d); err != nil {
			c.protocolLogger.Printf("error parsing extended handshake message %q: %s", payload, err)
			return fmt.Errorf("unmarshalling extended handshake payload: %w", err)
		}
		// Trigger this callback after it's been processed. If you want to handle it yourself, you
		// should hook PeerConnReadExtensionMessage.
		if cb := c.callbacks.ReadExtendedHandshake; cb != nil {
			cb(c, &d)
		}
		if d.Reqq != 0 {
			c.PeerMaxRequests = d.Reqq
		}
		c.PeerClientName.Store(d.V)
		if c.PeerExtensionIDs == nil {
			c.PeerExtensionIDs = make(map[pp.ExtensionName]pp.ExtensionNumber, len(d.M))
		}
		c.PeerListenPort = d.Port
		c.PeerPrefersEncryption = d.Encryption
		for name, id := range d.M {
			if _, ok := c.PeerExtensionIDs[name]; !ok {
				peersSupportingExtension.Add(
					// expvar.Var.String must produce valid JSON. "ut_payme\xeet_address" was being
					// entered here which caused problems later when unmarshalling.
					strconv.Quote(string(name)),
					1)
			}
			c.PeerExtensionIDs[name] = id
		}
		if d.MetadataSize != 0 {
			if err = t.setMetadataSize(d.MetadataSize); err != nil {
				return fmt.Errorf("setting metadata size to %d: %w", d.MetadataSize, err)
			}
		}
		c.requestPendingMetadata()
		if !t.cl.config.DisablePEX {
			t.pex.Add(c) // we learnt enough now
			// This checks the extension is supported internally.
			c.pex.Init(c)
		}
		return nil
	}
	extensionName, builtin, err := c.LocalLtepProtocolMap.LookupId(id)
	if err != nil {
		return
	}
	if !builtin {
		// User should have taken care of this in PeerConnReadExtensionMessage callback.
		return nil
	}
	switch extensionName {
	case pp.ExtensionNameMetadata:
		err := cl.gotMetadataExtensionMsg(payload, t, c)
		if err != nil {
			return fmt.Errorf("handling metadata extension message: %w", err)
		}
		return nil
	case pp.ExtensionNamePex:
		if !c.pex.IsEnabled() {
			return nil // or hang-up maybe?
		}
		err = c.pex.Recv(payload)
		if err != nil {
			err = fmt.Errorf("receiving pex message: %w", err)
		}
		return
	case utHolepunch.ExtensionName:
		var msg utHolepunch.Msg
		err = msg.UnmarshalBinary(payload)
		if err != nil {
			err = fmt.Errorf("unmarshalling ut_holepunch message: %w", err)
			return
		}
		err = c.t.handleReceivedUtHolepunchMsg(msg, c)
		return
	default:
		panic(fmt.Sprintf("unhandled builtin extension protocol %q", extensionName))
	}
}

// Set both the Reader and Writer for the connection from a single ReadWriter.
func (cn *PeerConn) setRW(rw io.ReadWriter) {
	cn.r = rw
	cn.w = rw
}

// Returns the Reader and Writer as a combined ReadWriter.
func (cn *PeerConn) rw() io.ReadWriter {
	return struct {
		io.Reader
		io.Writer
	}{cn.r, cn.w}
}

func (c *PeerConn) uploadAllowed() bool {
	if c.t.cl.config.NoUpload {
		return false
	}
	if c.t.dataUploadDisallowed {
		return false
	}
	if c.t.seeding() {
		return true
	}
	if !c.peerHasWantedPieces() {
		return false
	}
	// Don't upload more than 100 KiB more than we download.
	if c._stats.BytesWrittenData.Int64() >= c._stats.BytesReadData.Int64()+100<<10 {
		return false
	}
	return true
}

func (c *PeerConn) setRetryUploadTimer(delay time.Duration) {
	if c.uploadTimer == nil {
		c.uploadTimer = time.AfterFunc(delay, c.tickleWriter)
	} else {
		c.uploadTimer.Reset(delay)
	}
}

// Also handles choking and unchoking of the remote peer.
func (c *PeerConn) upload(msg func(pp.Message) bool) bool {
	// Breaking or completing this loop means we don't want to upload to the peer anymore, and we
	// choke them.
another:
	for c.uploadAllowed() {
		// We want to upload to the peer.
		if !c.unchoke(msg) {
			return false
		}
		for r := range c.readyPeerRequests {
			res := c.t.cl.config.UploadRateLimiter.ReserveN(time.Now(), int(r.Length))
			if !res.OK() {
				panic(fmt.Sprintf("upload rate limiter burst size < %d", r.Length))
			}
			delay := res.Delay()
			if delay > 0 {
				res.Cancel()
				c.setRetryUploadTimer(delay)
				// Hard to say what to return here.
				return true
			}
			more := c.sendChunk(r, msg)
			if !more {
				return false
			}
			goto another
		}
		return true
	}
	return c.choke(msg)
}

func (cn *PeerConn) drop() {
	cn.t.dropConnection(cn)
}

func (cn *PeerConn) providedBadData() {
	cn.t.cl.banPeerIP(cn.remoteIp())
}

// This is called when something has changed that should wake the writer, such as putting stuff into
// the writeBuffer, or changing some state that the writer can act on.
func (c *PeerConn) tickleWriter() {
	c.messageWriter.writeCond.Broadcast()
}

func (c *PeerConn) sendChunk(r Request, msg func(pp.Message) bool) (more bool) {
	b := MapMustGet(c.readyPeerRequests, r)
	panicif.NotEq(len(b), r.Length.Int())
	c.deleteReadyPeerRequest(r)
	c.lastChunkSent = time.Now()
	return msg(pp.Message{
		Type:  pp.Piece,
		Index: r.Index,
		Begin: r.Begin,
		Piece: b,
	})
}

func (c *PeerConn) setTorrent(t *Torrent) {
	panicif.NotNil(c.t)
	c.t = t
	c.initClosedCtx()
	c.logger.WithDefaultLevel(log.Debug).Printf("set torrent=%v", t)
	c.setPeerLoggers(t.logger, t.slogger())
	c.reconcileHandshakeStats()
}

func (c *PeerConn) pexPeerFlags() pp.PexPeerFlags {
	f := pp.PexPeerFlags(0)
	if c.PeerPrefersEncryption {
		f |= pp.PexPrefersEncryption
	}
	if c.outgoing {
		f |= pp.PexOutgoingConn
	}
	if c.utp() {
		f |= pp.PexSupportsUtp
	}
	return f
}

// This returns the address to use if we want to dial the peer again. It incorporates the peer's
// advertised listen port.
func (c *PeerConn) dialAddr() PeerRemoteAddr {
	if c.outgoing || c.PeerListenPort == 0 {
		return c.RemoteAddr
	}
	addrPort, err := addrPortFromPeerRemoteAddr(c.RemoteAddr)
	if err != nil {
		c.logger.Levelf(
			log.Warning,
			"error parsing %q for alternate dial port: %v",
			c.RemoteAddr,
			err,
		)
		return c.RemoteAddr
	}
	return netip.AddrPortFrom(addrPort.Addr(), uint16(c.PeerListenPort))
}

func (c *PeerConn) pexEvent(t pexEventType) (_ pexEvent, err error) {
	f := c.pexPeerFlags()
	dialAddr := c.dialAddr()
	addr, err := addrPortFromPeerRemoteAddr(dialAddr)
	if err != nil || !addr.IsValid() {
		err = fmt.Errorf("parsing dial addr %q: %w", dialAddr, err)
		return
	}
	return pexEvent{t, addr, f, nil}, nil
}

func (pc *PeerConn) String() string {
	return fmt.Sprintf(
		"%T %p [flags=%v id=%+q, exts=%v, v=%q]",
		pc,
		pc,
		pc.connectionFlags(),
		pc.PeerID,
		pc.PeerExtensionBytes,
		pc.PeerClientName.Load(),
	)
}

// Returns the pieces the peer could have based on their claims. If we don't know how many pieces
// are in the torrent, it could be a very large range if the peer has sent HaveAll.
func (pc *PeerConn) PeerPieces() *roaring.Bitmap {
	pc.locker().RLock()
	defer pc.locker().RUnlock()
	return pc.newPeerPieces()
}

func (pc *PeerConn) remoteIsTransmission() bool {
	return bytes.HasPrefix(pc.PeerID[:], []byte("-TR")) && pc.PeerID[7] == '-'
}

func (pc *PeerConn) remoteDialAddrPort() (netip.AddrPort, error) {
	dialAddr := pc.dialAddr()
	return addrPortFromPeerRemoteAddr(dialAddr)
}

func (pc *PeerConn) bitExtensionEnabled(bit pp.ExtensionBit) bool {
	return pc.t.cl.config.Extensions.GetBit(bit) && pc.PeerExtensionBytes.GetBit(bit)
}

func (cn *PeerConn) peerPiecesChanged() {
	cn.t.maybeDropMutuallyCompletePeer(cn)
}

// Returns whether the connection could be useful to us. We're seeding and
// they want data, we don't have metainfo and they can provide it, etc.
func (c *PeerConn) useful() bool {
	t := c.t
	if c.closed.IsSet() {
		return false
	}
	if !t.haveInfo() {
		return c.supportsExtension("ut_metadata")
	}
	if t.seeding() && c.peerInterested {
		return true
	}
	if c.peerHasWantedPieces() {
		return true
	}
	return false
}

func makeBuiltinLtepProtocols(pex bool) LocalLtepProtocolMap {
	ps := []pp.ExtensionName{pp.ExtensionNameMetadata, utHolepunch.ExtensionName}
	if pex {
		ps = append(ps, pp.ExtensionNamePex)
	}
	return LocalLtepProtocolMap{
		Index:      ps,
		NumBuiltin: len(ps),
	}
}

func (c *PeerConn) addBuiltinLtepProtocols(pex bool) {
	c.LocalLtepProtocolMap = &c.t.cl.defaultLocalLtepProtocolMap
}

func (pc *PeerConn) WriteExtendedMessage(extName pp.ExtensionName, payload []byte) error {
	pc.locker().Lock()
	defer pc.locker().Unlock()
	id := pc.PeerExtensionIDs[extName]
	if id == 0 {
		return fmt.Errorf("peer does not support or has disabled extension %q", extName)
	}
	pc.write(pp.Message{
		Type:            pp.Extended,
		ExtendedID:      id,
		ExtendedPayload: payload,
	})
	return nil
}

func (pc *PeerConn) shouldRequestHashes() bool {
	return pc.t.haveInfo() && pc.v2 && pc.t.info.HasV2()
}

func (pc *PeerConn) requestMissingHashes() {
	if !pc.shouldRequestHashes() {
		return
	}
	info := pc.t.info
	baseLayer := pp.Integer(merkle.Log2RoundingUp(merkle.RoundUpToPowerOfTwo(
		uint((pc.t.usualPieceSize() + merkle.BlockSize - 1) / merkle.BlockSize)),
	))
	nextFileBeginPiece := 0
file:
	for _, file := range info.UpvertedFiles() {
		fileNumPieces := int((file.Length + info.PieceLength - 1) / info.PieceLength)
		// We would be requesting the leaves, the file must be short enough that we can just do with
		// the pieces root as the piece hash.
		if fileNumPieces <= 1 {
			continue
		}
		curFileBeginPiece := nextFileBeginPiece
		nextFileBeginPiece += fileNumPieces
		haveAllHashes := true
		for i := range fileNumPieces {
			torrentPieceIndex := curFileBeginPiece + i
			if !pc.peerHasPiece(torrentPieceIndex) {
				continue file
			}
			if !pc.t.piece(torrentPieceIndex).hashV2.Ok {
				haveAllHashes = false
			}
		}
		if haveAllHashes {
			continue
		}
		piecesRoot := file.PiecesRoot.Unwrap()
		proofLayers := pp.Integer(0)
		for index := 0; index < fileNumPieces; index += 512 {
			// Minimizing to the number of pieces in a file conflicts with the BEP.
			length := merkle.RoundUpToPowerOfTwo(uint(min(512, fileNumPieces-index)))
			if length < 2 {
				// This should have been filtered out by baseLayer and pieces root as piece hash
				// checks.
				panic(length)
			}
			if length%2 != 0 {
				pc.protocolLogger.Levelf(log.Warning, "requesting odd hashes length %d", length)
			}
			msg := pp.Message{
				Type:        pp.HashRequest,
				PiecesRoot:  piecesRoot,
				BaseLayer:   baseLayer,
				Index:       pp.Integer(index),
				Length:      pp.Integer(length),
				ProofLayers: proofLayers,
			}
			hr := hashRequestFromMessage(msg)
			if generics.MapContains(pc.sentHashRequests, hr) {
				continue
			}
			pc.write(msg)
			generics.MakeMapIfNil(&pc.sentHashRequests)
			pc.sentHashRequests[hr] = struct{}{}
		}
	}
}

func (pc *PeerConn) onReadHashes(msg *pp.Message) (err error) {
	file := pc.t.getFileByPiecesRoot(msg.PiecesRoot)
	filePieceHashes := pc.receivedHashPieces[msg.PiecesRoot]
	if filePieceHashes == nil {
		filePieceHashes = make([][32]byte, file.numPieces())
		generics.MakeMapIfNil(&pc.receivedHashPieces)
		pc.receivedHashPieces[msg.PiecesRoot] = filePieceHashes
	}
	if msg.ProofLayers != 0 {
		// This isn't handled yet.
		panic(msg.ProofLayers)
	}
	copy(filePieceHashes[msg.Index:], msg.Hashes)
	root := merkle.RootWithPadHash(
		filePieceHashes,
		metainfo.HashForPiecePad(int64(pc.t.usualPieceSize())))
	expectedPiecesRoot := file.piecesRoot.Unwrap()
	if root == expectedPiecesRoot {
		pc.protocolLogger.WithNames(v2HashesLogName).Levelf(
			log.Info,
			"got piece hashes for file %v (num pieces %v)",
			file, file.numPieces())
		for filePieceIndex, peerHash := range filePieceHashes {
			torrentPieceIndex := file.BeginPieceIndex() + filePieceIndex
			pc.t.piece(torrentPieceIndex).setV2Hash(peerHash)
		}
	} else {
		pc.protocolLogger.WithNames(v2HashesLogName).Levelf(
			log.Debug,
			"peer file piece hashes root mismatch: %x != %x",
			root, expectedPiecesRoot)
	}
	return nil
}

func (pc *PeerConn) getHashes(msg *pp.Message) ([][32]byte, error) {
	if msg.ProofLayers != 0 {
		return nil, errors.New("proof layers not supported")
	}
	if msg.Length > 8192 {
		return nil, fmt.Errorf("requested too many hashes: %d", msg.Length)
	}
	file := pc.t.getFileByPiecesRoot(msg.PiecesRoot)
	if file == nil {
		return nil, fmt.Errorf("no file for pieces root %x", msg.PiecesRoot)
	}
	beginPieceIndex := file.BeginPieceIndex()
	endPieceIndex := file.EndPieceIndex()
	length := merkle.RoundUpToPowerOfTwo(uint(endPieceIndex - beginPieceIndex))
	if uint(msg.Index+msg.Length) > length {
		return nil, errors.New("invalid hash range")
	}

	hashes := make([][32]byte, msg.Length)
	padHash := metainfo.HashForPiecePad(int64(pc.t.usualPieceSize()))
	for i := range hashes {
		torrentPieceIndex := beginPieceIndex + int(msg.Index) + i
		if torrentPieceIndex >= endPieceIndex {
			hashes[i] = padHash
			continue
		}
		piece := pc.t.piece(torrentPieceIndex)
		hash, err := piece.obtainHashV2()
		if err != nil {
			return nil, fmt.Errorf("can't get hash for piece %d: %w", torrentPieceIndex, err)
		}
		hashes[i] = hash
	}
	return hashes, nil
}

func (pc *PeerConn) onHashRequest(msg *pp.Message) error {
	if !pc.t.info.HasV2() {
		return errors.New("torrent has no v2 metadata")
	}

	resp := pp.Message{
		PiecesRoot:  msg.PiecesRoot,
		BaseLayer:   msg.BaseLayer,
		Index:       msg.Index,
		Length:      msg.Length,
		ProofLayers: msg.ProofLayers,
	}

	hashes, err := pc.getHashes(msg)
	if err != nil {
		pc.protocolLogger.WithNames(v2HashesLogName).Levelf(log.Debug, "error getting hashes: %v", err)
		resp.Type = pp.HashReject
		pc.write(resp)
		return nil
	}

	resp.Type = pp.Hashes
	resp.Hashes = hashes
	pc.write(resp)
	return nil
}

type hashRequest struct {
	piecesRoot                            [32]byte
	baseLayer, index, length, proofLayers pp.Integer
}

func (hr hashRequest) toMessage() pp.Message {
	return pp.Message{
		Type:        pp.HashRequest,
		PiecesRoot:  hr.piecesRoot,
		BaseLayer:   hr.baseLayer,
		Index:       hr.index,
		Length:      hr.length,
		ProofLayers: hr.proofLayers,
	}
}

func hashRequestFromMessage(m pp.Message) hashRequest {
	return hashRequest{
		piecesRoot:  m.PiecesRoot,
		baseLayer:   m.BaseLayer,
		index:       m.Index,
		length:      m.Length,
		proofLayers: m.ProofLayers,
	}
}

func (me *PeerConn) peerPtr() *Peer {
	return &me.Peer
}

// The actual value to use as the maximum outbound requests.
func (cn *PeerConn) nominalMaxRequests() maxRequests {
	return max(1, min(cn.PeerMaxRequests, cn.peakRequests*2, maxLocalToRemoteRequests))
}

// Set the Peer loggers. This is given Client loggers, and later Torrent loggers when the Torrent is
// set.
func (me *PeerConn) setPeerLoggers(a log.Logger, s *slog.Logger) {
	me.Peer.logger = a.WithDefaultLevel(log.Warning).WithContextText(fmt.Sprintf("%T %p", me, me))
	me.Peer.slogger = s.With(fmt.Sprintf("%T", me), fmt.Sprintf("%p", me))
	me.protocolLogger = me.logger.WithNames(protocolLoggingName)
}

// Methods moved from peer.go (in their original order):

func (p *PeerConn) initRequestState() {
	p.requestState.Requests = &peerRequests{}
}

func (cn *PeerConn) expectingChunks() bool {
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
	cn.peerAllowedFast.Iterate(func(i pieceIndex) bool {
		haveAllowedFastRequests = roaringBitmapRangeCardinality[RequestIndex](
			cn.requestState.Requests,
			cn.t.pieceRequestIndexBegin(i),
			cn.t.pieceRequestIndexBegin(i+1),
		) == 0
		return !haveAllowedFastRequests
	})
	return haveAllowedFastRequests
}

func (cn *PeerConn) cumInterest() time.Duration {
	ret := cn.priorInterest
	if cn.requestState.Interested {
		ret += time.Since(cn.lastBecameInterested)
	}
	return ret
}

func (cn *PeerConn) supportsExtension(ext pp.ExtensionName) bool {
	_, ok := cn.PeerExtensionIDs[ext]
	return ok
}

// Inspired by https://github.com/transmission/transmission/wiki/Peer-Status-Text.
func (cn *PeerConn) statusFlags() (ret string) {
	c := func(b byte) {
		ret += string([]byte{b})
	}
	if cn.requestState.Interested {
		c('i')
	}
	if cn.choking {
		c('c')
	}
	c(':')
	ret += cn.connectionFlags()
	c(':')
	if cn.peerInterested {
		c('i')
	}
	if cn.peerChoking {
		c('c')
	}
	return
}

func (cn *PeerConn) iterContiguousPieceRequests(f func(piece pieceIndex, count int)) {
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
	cn.requestState.Requests.Iterate(func(requestIndex requestStrategy.RequestIndex) bool {
		next(Some(cn.t.pieceIndexOfRequestIndex(requestIndex)))
		return true
	})
	next(None[pieceIndex]())
}

func (cn *PeerConn) peerImplWriteStatus(w io.Writer) {
	prio, err := cn.peerPriority()
	prioStr := fmt.Sprintf("%08x", prio)
	if err != nil {
		prioStr += ": " + err.Error()
	}
	fmt.Fprintf(w, "bep40-prio: %v\n", prioStr)
	fmt.Fprintf(w, "last msg: %s, connected: %s, last helpful: %s, itime: %s, etime: %s\n",
		eventAgeString(cn.lastMessageReceived),
		eventAgeString(cn.completedHandshake),
		eventAgeString(cn.lastHelpful()),
		cn.cumInterest(),
		cn.totalExpectingTime(),
	)
	fmt.Fprintf(w,
		"%s completed, chunks uploaded: %v\n",
		cn.completedString(),
		&cn._stats.ChunksWritten,
	)
	fmt.Fprintf(w, "requested pieces:")
	cn.iterContiguousPieceRequests(func(piece pieceIndex, count int) {
		fmt.Fprintf(w, " %v(%v)", piece, count)
	})
}

func (cn *PeerConn) setInterested(interested bool) bool {
	if cn.requestState.Interested == interested {
		return true
	}
	cn.requestState.Interested = interested
	if interested {
		cn.lastBecameInterested = time.Now()
	} else if !cn.lastBecameInterested.IsZero() {
		cn.priorInterest += time.Since(cn.lastBecameInterested)
	}
	cn.updateExpectingChunks()
	return cn.writeInterested(interested)
}

// This function seems to only used by Peer.request. It's all logic checks, so maybe we can no-op it
// when we want to go fast.
func (cn *PeerConn) shouldRequest(r RequestIndex) error {
	err := cn.t.checkValidReceiveChunk(cn.t.requestIndexToRequest(r))
	if err != nil {
		return err
	}
	pi := cn.t.pieceIndexOfRequestIndex(r)
	if cn.requestState.Cancelled.Contains(r) {
		return errors.New("request is cancelled and waiting acknowledgement")
	}
	if !cn.peerHasPiece(pi) {
		return errors.New("requesting piece peer doesn't have")
	}
	if !cn.t.peerIsActive(cn.peerPtr()) {
		panic("requesting but not in active conns")
	}
	if cn.closed.IsSet() {
		panic("requesting when connection is closed")
	}
	if cn.t.hashingPiece(pi) {
		panic("piece is being hashed")
	}
	p := cn.t.piece(pi)
	if p.marking {
		panic("piece is being marked")
	}
	if cn.t.pieceQueuedForHash(pi) {
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

func (cn *PeerConn) mustRequest(r RequestIndex) bool {
	more, err := cn.request(r)
	if err != nil {
		panic(err)
	}
	return more
}

func (cn *PeerConn) request(r RequestIndex) (more bool, err error) {
	if err := cn.shouldRequest(r); err != nil {
		panic(err)
	}
	if cn.requestState.Requests.Contains(r) {
		return true, nil
	}
	if maxRequests(cn.requestState.Requests.GetCardinality()) >= cn.nominalMaxRequests() {
		return true, errors.New("too many outstanding requests")
	}
	cn.requestState.Requests.Add(r)
	if cn.validReceiveChunks == nil {
		cn.validReceiveChunks = make(map[RequestIndex]int)
	}
	cn.validReceiveChunks[r]++
	cn.t.requestState[r] = requestState{
		peer: weak.Make(cn),
		when: time.Now(),
	}
	cn.updateExpectingChunks()
	ppReq := cn.t.requestIndexToRequest(r)
	for _, f := range cn.callbacks.SentRequest {
		f(PeerRequestEvent{cn.peerPtr(), ppReq})
	}
	return cn._request(ppReq), nil
}

func (me *PeerConn) cancel(r RequestIndex) {
	if !me.deleteRequest(r) {
		panic("request not existing should have been guarded")
	}
	me.handleCancel(r)
	me.decPeakRequests()
	if me.isLowOnRequests() {
		me.onNeedUpdateRequests(peerUpdateRequestsPeerCancelReason)
	}
}

// Sets a reason to update requests, and if there wasn't already one, handle it.
func (cn *PeerConn) onNeedUpdateRequests(reason updateRequestReason) {
	if cn.needRequestUpdate != "" {
		return
	}
	cn.needRequestUpdate = reason
	// Run this before the Client lock is released.
	cn.locker().DeferUniqueUnaryFunc(cn, cn.handleOnNeedUpdateRequests)
}

// Returns true if it was valid to reject the request.
func (c *PeerConn) remoteRejectedRequest(r RequestIndex) bool {
	if c.deleteRequest(r) {
		c.decPeakRequests()
	} else if !c.requestState.Cancelled.CheckedRemove(r) {
		// The request was already cancelled.
		return false
	}
	if c.isLowOnRequests() {
		c.onNeedUpdateRequests(peerUpdateRequestsRemoteRejectReason)
	}
	c.decExpectedChunkReceive(r)
	return true
}

func (c *PeerConn) decExpectedChunkReceive(r RequestIndex) {
	count := c.validReceiveChunks[r]
	if count == 1 {
		delete(c.validReceiveChunks, r)
	} else if count > 1 {
		c.validReceiveChunks[r] = count - 1
	} else {
		panic(r)
	}
}

// Returns true if an outstanding request is removed. Cancelled requests should be handled
// separately.
func (c *PeerConn) deleteRequest(r RequestIndex) bool {
	if !c.requestState.Requests.CheckedRemove(r) {
		return false
	}
	for _, f := range c.callbacks.DeletedRequest {
		f(PeerRequestEvent{c.peerPtr(), c.t.requestIndexToRequest(r)})
	}
	c.updateExpectingChunks()
	// TODO: Can't this happen if a request is stolen?
	if c.t.requestingPeer(r) != c {
		panic("only one peer should have a given request at a time")
	}
	delete(c.t.requestState, r)
	// c.t.iterPeers(func(p *Peer) {
	// 	if p.isLowOnRequests() {
	// 		p.onNeedUpdateRequests("Peer.deleteRequest")
	// 	}
	// })
	return true
}

func (c *PeerConn) deleteAllRequests(reason updateRequestReason) {
	if c.requestState.Requests.IsEmpty() {
		return
	}
	c.requestState.Requests.IterateSnapshot(func(x RequestIndex) bool {
		if !c.deleteRequest(x) {
			panic("request should exist")
		}
		return true
	})
	c.assertNoRequests()
	c.t.iterPeers(func(p *Peer) {
		if p.isLowOnRequests() {
			p.onNeedUpdateRequests(reason)
		}
	})
}

func (c *PeerConn) assertNoRequests() {
	if !c.requestState.Requests.IsEmpty() {
		panic(c.requestState.Requests.GetCardinality())
	}
}

func (c *PeerConn) cancelAllRequests() {
	c.requestState.Requests.IterateSnapshot(func(x RequestIndex) bool {
		c.cancel(x)
		return true
	})
	c.assertNoRequests()
}

func (p *PeerConn) uncancelledRequests() uint64 {
	return p.requestState.Requests.GetCardinality()
}

func (p *PeerConn) isLowOnRequests() bool {
	return p.requestState.Requests.IsEmpty() && p.requestState.Cancelled.IsEmpty()
}

func (c *PeerConn) checkReceivedChunk(req RequestIndex, msg *pp.Message, ppReq Request) (intended bool, err error) {
	if c.validReceiveChunks[req] <= 0 {
		ChunksReceived.Add("unexpected", 1)
		err = errors.New("received unexpected chunk")
		return
	}
	c.decExpectedChunkReceive(req)

	if c.peerChoking && c.peerAllowedFast.Contains(pieceIndex(ppReq.Index)) {
		ChunksReceived.Add("due to allowed fast", 1)
	}
	// The request needs to be deleted immediately to prevent cancels occurring asynchronously when
	// have actually already received the piece, while we have the Client unlocked to write the data
	// out.
	{
		if c.requestState.Requests.Contains(req) {
			for _, f := range c.callbacks.ReceivedRequested {
				f(PeerMessageEvent{c.peerPtr(), msg})
			}
		}
		// Request has been satisfied.
		if c.deleteRequest(req) || c.requestState.Cancelled.CheckedRemove(req) {
			intended = true
			if c.isLowOnRequests() {
				c.onNeedUpdateRequests("Peer.receiveChunk deleted request")
			}
		} else {
			ChunksReceived.Add("unintended", 1)
		}
	}

	return
}

// Reconcile bytes transferred before connection was associated with a torrent.
func (c *PeerConn) reconcileHandshakeStats() {
	panicif.True(c.reconciledHandshakeStats)
	if c._stats != (ConnStats{
		// Handshakes should only increment these fields:
		BytesWritten: c._stats.BytesWritten,
		BytesRead:    c._stats.BytesRead,
	}) {
		panic("bad stats")
	}
	// Add the stat data so far to relevant Torrent stats that were skipped before the handshake
	// completed.
	c.relevantConnStats(&c.t.connStats)(func(cs *ConnStats) bool {
		cs.BytesRead.Add(c._stats.BytesRead.Int64())
		cs.BytesWritten.Add(c._stats.BytesWritten.Int64())
		return true
	})
	c.reconciledHandshakeStats = true
}
