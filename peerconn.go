package torrent

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/generics"
	. "github.com/anacrolix/generics"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2/bitmap"
	"github.com/anacrolix/multiless"
	"golang.org/x/exp/maps"
	"golang.org/x/time/rate"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/internal/alloclim"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/mse"
	pp "github.com/anacrolix/torrent/peer_protocol"
	utHolepunch "github.com/anacrolix/torrent/peer_protocol/ut-holepunch"
)

// Maintains the state of a BitTorrent-protocol based connection with a peer.
type PeerConn struct {
	Peer

	// A string that should identify the PeerConn's net.Conn endpoints. The net.Conn could
	// be wrapping WebRTC, uTP, or TCP etc. Used in writing the conn status for peers.
	connString string

	// See BEP 3 etc.
	PeerID             PeerID
	PeerExtensionBytes pp.PeerExtensionBits
	PeerListenPort     int

	// The actual Conn, used for closing, and setting socket options. Do not use methods on this
	// while holding any mutexes.
	conn net.Conn
	// The Reader and Writer for this Conn, with hooks installed for stats,
	// limiting, deadlines etc.
	w io.Writer
	r io.Reader

	messageWriter peerConnMsgWriter

	PeerExtensionIDs map[pp.ExtensionName]pp.ExtensionNumber
	PeerClientName   atomic.Value
	uploadTimer      *time.Timer
	pex              pexConnState

	// The pieces the peer has claimed to have.
	_peerPieces roaring.Bitmap
	// The peer has everything. This can occur due to a special message, when
	// we may not even know the number of pieces in the torrent yet.
	peerSentHaveAll bool

	peerRequestDataAllocLimiter alloclim.Limiter

	outstandingHolepunchingRendezvous map[netip.AddrPort]struct{}
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
	if true {
		return fmt.Sprintf(
			"%v conns, %v unsent events",
			len(cn.pex.remoteLiveConns),
			cn.pex.numPending(),
		)
	} else {
		// This alternative branch prints out the remote live conn addresses.
		return fmt.Sprintf(
			"%v conns, %v unsent events",
			strings.Join(generics.SliceMap(
				maps.Keys(cn.pex.remoteLiveConns),
				func(from netip.AddrPort) string {
					return from.String()
				}), ","),
			cn.pex.numPending(),
		)
	}
}

func (cn *PeerConn) peerImplStatusLines() []string {
	return []string{
		cn.connString,
		fmt.Sprintf("peer id: %+q", cn.PeerID),
		fmt.Sprintf("extensions: %v", cn.PeerExtensionBytes),
		fmt.Sprintf("ltep extensions: %v", cn.PeerExtensionIDs),
		fmt.Sprintf("pex: %s", cn.pexStatus()),
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

func (cn *PeerConn) connectionFlags() (ret string) {
	c := func(b byte) {
		ret += string([]byte{b})
	}
	if cn.cryptoMethod == mse.CryptoMethodRC4 {
		c('E')
	} else if cn.headerEncrypted {
		c('e')
	}
	ret += string(cn.Discovery)
	if cn.utp() {
		c('U')
	}
	return
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
	cn.logger.WithDefaultLevel(log.Debug).Printf("requesting metadata piece %d", index)
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
	if _, ok := cn.peerRequests[r]; !ok {
		torrent.Add("unexpected cancels received", 1)
		return
	}
	if cn.fastEnabled() {
		cn.reject(r)
	} else {
		delete(cn.peerRequests, r)
	}
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
	for _, state := range cn.peerRequests {
		state.allocReservation.Drop()
	}
	cn.peerRequests = nil
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

func (me *PeerConn) _request(r Request) bool {
	return me.write(pp.Message{
		Type:   pp.Request,
		Index:  r.Index,
		Begin:  r.Begin,
		Length: r.Length,
	})
}

func (me *PeerConn) _cancel(r RequestIndex) bool {
	me.write(makeCancelMessage(me.t.requestIndexToRequest(r)))
	// Transmission does not send rejects for received cancels. See
	// https://github.com/transmission/transmission/pull/2275.
	return me.fastEnabled() && !me.remoteIsTransmission()
}

func (cn *PeerConn) fillWriteBuffer() {
	if cn.messageWriter.writeBuffer.Len() > writeBufferLowWaterLen {
		// Fully committing to our max requests requires sufficient space (see
		// maxLocalToRemoteRequests). Flush what we have instead. We also prefer always to make
		// requests than to do PEX or upload, so we short-circuit before handling those. Any update
		// request reason will not be cleared, so we'll come right back here when there's space. We
		// can't do this in maybeUpdateActualRequestState because it's a method on Peer and has no
		// knowledge of write buffers.
	}
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

func (cn *PeerConn) handleUpdateRequests() {
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
		cn.updateRequests("have")
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
		cn.updateRequests("bitfield")
	}
	// We didn't guard this before, I see no reason to do it now.
	cn.peerPiecesChanged()
	return nil
}

func (cn *PeerConn) onPeerHasAllPieces() {
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
	if !cn.t._pendingPieces.IsEmpty() {
		cn.updateRequests("Peer.onPeerHasAllPieces")
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
	cn.allStats(func(cs *ConnStats) { cs.wroteMsg(msg) })
}

func (cn *PeerConn) wroteBytes(n int64) {
	cn.allStats(add(n, func(cs *ConnStats) *Count { return &cs.BytesWritten }))
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
	if state, ok := c.peerRequests[r]; ok {
		state.allocReservation.Drop()
		delete(c.peerRequests, r)
	}
}

func (c *PeerConn) maximumPeerRequestChunkLength() (_ Option[int]) {
	uploadRateLimiter := c.t.cl.config.UploadRateLimiter
	if uploadRateLimiter.Limit() == rate.Inf {
		return
	}
	return Some(uploadRateLimiter.Burst())
}

// startFetch is for testing purposes currently.
func (c *PeerConn) onReadRequest(r Request, startFetch bool) error {
	requestedChunkLengths.Add(strconv.FormatUint(r.Length.Uint64(), 10), 1)
	if _, ok := c.peerRequests[r]; ok {
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
	if len(c.peerRequests) >= localClientReqq {
		torrent.Add("requests received while queue full", 1)
		if c.fastEnabled() {
			c.reject(r)
		}
		// BEP 6 says we may close here if we choose.
		return nil
	}
	if opt := c.maximumPeerRequestChunkLength(); opt.Ok && int(r.Length) > opt.Value {
		err := fmt.Errorf("peer requested chunk too long (%v)", r.Length)
		c.logger.Levelf(log.Warning, err.Error())
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
	if c.peerRequests == nil {
		c.peerRequests = make(map[Request]*peerRequestState, localClientReqq)
	}
	value := &peerRequestState{
		allocReservation: c.peerRequestDataAllocLimiter.Reserve(int64(r.Length)),
	}
	c.peerRequests[r] = value
	if startFetch {
		// TODO: Limit peer request data read concurrency.
		go c.peerRequestDataReader(r, value)
	}
	return nil
}

func (c *PeerConn) peerRequestDataReader(r Request, prs *peerRequestState) {
	// Should we depend on Torrent closure here? I think it's okay to get cancelled from elsewhere,
	// or fail to read and then cleanup. Also, we used to hang here if the reservation was never
	// dropped, that was fixed.
	ctx := context.Background()
	err := prs.allocReservation.Wait(ctx)
	if err != nil {
		c.logger.WithDefaultLevel(log.Debug).Levelf(log.ErrorLevel(err), "waiting for alloc limit reservation: %v", err)
		return
	}
	b, err := c.readPeerRequestData(r)
	c.locker().Lock()
	defer c.locker().Unlock()
	if err != nil {
		c.peerRequestDataReadFailed(err, r)
	} else {
		if b == nil {
			panic("data must be non-nil to trigger send")
		}
		torrent.Add("peer request data read successes", 1)
		prs.data = b
		// This might be required for the error case too (#752 and #753).
		c.tickleWriter()
	}
}

// If this is maintained correctly, we might be able to support optional synchronous reading for
// chunk sending, the way it used to work.
func (c *PeerConn) peerRequestDataReadFailed(err error, r Request) {
	torrent.Add("peer request data read failures", 1)
	logLevel := log.Warning
	if c.t.hasStorageCap() {
		// It's expected that pieces might drop. See
		// https://github.com/anacrolix/torrent/issues/702#issuecomment-1000953313.
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
	if c.fastEnabled() {
		c.reject(r)
	} else {
		if c.choking {
			// If fast isn't enabled, I think we would have wiped all peer requests when we last
			// choked, and requests while we're choking would be ignored. It could be possible that
			// a peer request data read completed concurrently to it being deleted elsewhere.
			c.logger.WithDefaultLevel(log.Warning).Printf("already choking peer, requests might not be rejected correctly")
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
		if err == io.EOF {
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
	c.logger.WithContextText(fmt.Sprintf(
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
			defer cl.lock()
			err = decoder.Decode(&msg)
		}()
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
				c.logger.Levelf(log.Debug,
					"%v requests were preserved while being choked (fast=%v)",
					preservedCount,
					c.fastEnabled())

				torrent.Add("requestsPreservedThroughChoking", int64(preservedCount))
			}
			if !c.t._pendingPieces.IsEmpty() {
				c.updateRequests("unchoked")
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
			if len(msg.Piece) == int(t.chunkSize) {
				t.chunkPool.Put(&msg.Piece)
			}
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
			c.updateRequests("suggested")
		case pp.HaveAll:
			err = c.onPeerSentHaveAll()
		case pp.HaveNone:
			err = c.peerSentHaveNone()
		case pp.Reject:
			req := newRequestFromMessage(&msg)
			if !c.remoteRejectedRequest(c.t.requestIndexFromRequest(req)) {
				err = fmt.Errorf("received invalid reject for request %v", req)
				c.logger.Levelf(log.Debug, "%v", err)
			}
		case pp.AllowedFast:
			torrent.Add("allowed fasts received", 1)
			log.Fmsg("peer allowed fast: %d", msg.Index).AddValues(c).LogLevel(log.Debug, c.t.logger)
			c.updateRequests("PeerConn.mainReadLoop allowed fast")
		case pp.Extended:
			err = c.onReadExtendedMsg(msg.ExtendedID, msg.ExtendedPayload)
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
	switch id {
	case pp.HandshakeExtendedID:
		var d pp.ExtendedHandshakeMessage
		if err := bencode.Unmarshal(payload, &d); err != nil {
			c.logger.Printf("error parsing extended handshake message %q: %s", payload, err)
			return fmt.Errorf("unmarshalling extended handshake payload: %w", err)
		}
		if cb := c.callbacks.ReadExtendedHandshake; cb != nil {
			cb(c, &d)
		}
		// c.logger.WithDefaultLevel(log.Debug).Printf("received extended handshake message:\n%s", spew.Sdump(d))
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
	case metadataExtendedId:
		err := cl.gotMetadataExtensionMsg(payload, t, c)
		if err != nil {
			return fmt.Errorf("handling metadata extension message: %w", err)
		}
		return nil
	case pexExtendedId:
		if !c.pex.IsEnabled() {
			return nil // or hang-up maybe?
		}
		err = c.pex.Recv(payload)
		if err != nil {
			err = fmt.Errorf("receiving pex message: %w", err)
		}
		return
	case utHolepunchExtendedId:
		var msg utHolepunch.Msg
		err = msg.UnmarshalBinary(payload)
		if err != nil {
			err = fmt.Errorf("unmarshalling ut_holepunch message: %w", err)
			return
		}
		err = c.t.handleReceivedUtHolepunchMsg(msg, c)
		return
	default:
		return fmt.Errorf("unexpected extended message ID: %v", id)
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
	// Breaking or completing this loop means we don't want to upload to the
	// peer anymore, and we choke them.
another:
	for c.uploadAllowed() {
		// We want to upload to the peer.
		if !c.unchoke(msg) {
			return false
		}
		for r, state := range c.peerRequests {
			if state.data == nil {
				continue
			}
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
			more := c.sendChunk(r, msg, state)
			delete(c.peerRequests, r)
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

func (cn *PeerConn) ban() {
	cn.t.cl.banPeerIP(cn.remoteIp())
}

// This is called when something has changed that should wake the writer, such as putting stuff into
// the writeBuffer, or changing some state that the writer can act on.
func (c *PeerConn) tickleWriter() {
	c.messageWriter.writeCond.Broadcast()
}

func (c *PeerConn) sendChunk(r Request, msg func(pp.Message) bool, state *peerRequestState) (more bool) {
	c.lastChunkSent = time.Now()
	state.allocReservation.Release()
	return msg(pp.Message{
		Type:  pp.Piece,
		Index: r.Index,
		Begin: r.Begin,
		Piece: state.data,
	})
}

func (c *PeerConn) setTorrent(t *Torrent) {
	if c.t != nil {
		panic("connection already associated with a torrent")
	}
	c.t = t
	c.logger.WithDefaultLevel(log.Debug).Printf("set torrent=%v", t)
	t.reconcileHandshakeStats(c)
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

func (c *PeerConn) String() string {
	return fmt.Sprintf("%T %p [id=%+q, exts=%v, v=%q]", c, c, c.PeerID, c.PeerExtensionBytes, c.PeerClientName.Load())
}

// Returns the pieces the peer could have based on their claims. If we don't know how many pieces
// are in the torrent, it could be a very large range the peer has sent HaveAll.
func (cn *PeerConn) PeerPieces() *roaring.Bitmap {
	cn.locker().RLock()
	defer cn.locker().RUnlock()
	return cn.newPeerPieces()
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
