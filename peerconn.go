package torrent

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/chansync"
	. "github.com/anacrolix/generics"
	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/iter"
	"github.com/anacrolix/missinggo/v2/bitmap"
	"github.com/anacrolix/multiless"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/mse"
	pp "github.com/anacrolix/torrent/peer_protocol"
	request_strategy "github.com/anacrolix/torrent/request-strategy"
	"github.com/anacrolix/torrent/typed-roaring"
	"golang.org/x/time/rate"
)

type PeerSource string

const (
	PeerSourceTracker         = "Tr"
	PeerSourceIncoming        = "I"
	PeerSourceDhtGetPeers     = "Hg" // Peers we found by searching a DHT.
	PeerSourceDhtAnnouncePeer = "Ha" // Peers that were announced to us by a DHT.
	PeerSourcePex             = "X"
	// The peer was given directly, such as through a magnet link.
	PeerSourceDirect = "M"
)

type peerRequestState struct {
	data []byte
}

type PeerRemoteAddr interface {
	String() string
}

type (
	// Since we have to store all the requests in memory, we can't reasonably exceed what could be
	// indexed with the memory space available.
	maxRequests = int
)

type Peer struct {
	// First to ensure 64-bit alignment for atomics. See #262.
	_stats ConnStats

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
	PeerListenPort        int
	// The highest possible number of pieces the torrent could have based on
	// communication with the peer. Generally only useful until we have the
	// torrent info.
	peerMinPieces pieceIndex
	// Pieces we've accepted chunks for from the peer.
	peerTouchedPieces map[pieceIndex]struct{}
	peerAllowedFast   typedRoaring.Bitmap[pieceIndex]

	PeerMaxRequests  maxRequests // Maximum pending requests the peer allows.
	PeerExtensionIDs map[pp.ExtensionName]pp.ExtensionNumber
	PeerClientName   atomic.Value

	logger log.Logger
}

type peerRequests = orderedBitmap[RequestIndex]

func (pc *Peer) initRequestState() {
	pc.requestState.Requests = &peerRequests{}
}

// Maintains the state of a BitTorrent-protocol based connection with a peer.
type PeerConn struct {
	Peer

	// A string that should identify the PeerConn's net.Conn endpoints. The net.Conn could
	// be wrapping WebRTC, uTP, or TCP etc. Used in writing the conn status for peers.
	connString string

	// See BEP 3 etc.
	PeerID             PeerID
	PeerExtensionBytes pp.PeerExtensionBits

	// The actual Conn, used for closing, and setting socket options. Do not use methods on this
	// while holding any mutexes.
	conn net.Conn
	// The Reader and Writer for this Conn, with hooks installed for stats,
	// limiting, deadlines etc.
	w io.Writer
	r io.Reader

	messageWriter peerConnMsgWriter

	uploadTimer *time.Timer
	pex         pexConnState

	// The pieces the peer has claimed to have.
	_peerPieces roaring.Bitmap
	// The peer has everything. This can occur due to a special message, when
	// we may not even know the number of pieces in the torrent yet.
	peerSentHaveAll bool
}

func (c *PeerConn) connStatusString() string {
	return fmt.Sprintf("%+-55q %s %s", c.PeerID, c.PeerExtensionBytes, c.connString)
}

func (pc *Peer) updateExpectingChunks() {
	if pc.expectingChunks() {
		if pc.lastStartedExpectingToReceiveChunks.IsZero() {
			pc.lastStartedExpectingToReceiveChunks = time.Now()
		}
	} else {
		if !pc.lastStartedExpectingToReceiveChunks.IsZero() {
			pc.cumulativeExpectedToReceiveChunks += time.Since(pc.lastStartedExpectingToReceiveChunks)
			pc.lastStartedExpectingToReceiveChunks = time.Time{}
		}
	}
}

func (pc *Peer) expectingChunks() bool {
	if pc.requestState.Requests.IsEmpty() {
		return false
	}
	if !pc.requestState.Interested {
		return false
	}
	if !pc.peerChoking {
		return true
	}
	haveAllowedFastRequests := false
	pc.peerAllowedFast.Iterate(func(i pieceIndex) bool {
		haveAllowedFastRequests = roaringBitmapRangeCardinality[RequestIndex](
			pc.requestState.Requests,
			pc.t.pieceRequestIndexOffset(i),
			pc.t.pieceRequestIndexOffset(i+1),
		) == 0
		return !haveAllowedFastRequests
	})
	return haveAllowedFastRequests
}

func (pc *Peer) remoteChokingPiece(piece pieceIndex) bool {
	return pc.peerChoking && !pc.peerAllowedFast.Contains(piece)
}

// Returns true if the connection is over IPv6.
func (c *PeerConn) ipv6() bool {
	ip := c.remoteIp()
	if ip.To4() != nil {
		return false
	}
	return len(ip) == net.IPv6len
}

// Returns true the if the dialer/initiator has the lower client peer ID. TODO: Find the
// specification for this.
func (c *PeerConn) isPreferredDirection() bool {
	return bytes.Compare(c.t.cl.peerID[:], c.PeerID[:]) < 0 == c.outgoing
}

// Returns whether the left connection should be preferred over the right one,
// considering only their networking properties. If ok is false, we can't
// decide.
func (c *PeerConn) hasPreferredNetworkOver(r *PeerConn) bool {
	var ml multiless.Computation
	ml = ml.Bool(r.isPreferredDirection(), c.isPreferredDirection())
	ml = ml.Bool(c.utp(), r.utp())
	ml = ml.Bool(r.ipv6(), c.ipv6())
	return ml.Less()
}

func (pc *Peer) cumInterest() time.Duration {
	ret := pc.priorInterest
	if pc.requestState.Interested {
		ret += time.Since(pc.lastBecameInterested)
	}
	return ret
}

func (c *PeerConn) peerHasAllPieces() (all, known bool) {
	if c.peerSentHaveAll {
		return true, true
	}
	if !c.t.haveInfo() {
		return false, false
	}
	return c._peerPieces.GetCardinality() == uint64(c.t.numPieces()), true
}

func (pc *Peer) locker() *lockWithDeferreds {
	return pc.t.cl.locker()
}

func (pc *Peer) supportsExtension(ext pp.ExtensionName) bool {
	_, ok := pc.PeerExtensionIDs[ext]
	return ok
}

// The best guess at number of pieces in the torrent for this peer.
func (pc *Peer) bestPeerNumPieces() pieceIndex {
	if pc.t.haveInfo() {
		return pc.t.numPieces()
	}
	return pc.peerMinPieces
}

func (pc *Peer) completedString() string {
	have := pieceIndex(pc.peerPieces().GetCardinality())
	if all, _ := pc.peerHasAllPieces(); all {
		have = pc.bestPeerNumPieces()
	}
	return fmt.Sprintf("%d/%d", have, pc.bestPeerNumPieces())
}

func (c *PeerConn) onGotInfo(info *metainfo.Info) {
	c.setNumPieces(info.NumPieces())
}

// Correct the PeerPieces slice length. Return false if the existing slice is invalid, such as by
// receiving badly sized BITFIELD, or invalid HAVE messages.
func (c *PeerConn) setNumPieces(num pieceIndex) {
	c._peerPieces.RemoveRange(bitmap.BitRange(num), bitmap.ToEnd)
	c.peerPiecesChanged()
}

func (c *PeerConn) peerPieces() *roaring.Bitmap {
	return &c._peerPieces
}

func eventAgeString(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return fmt.Sprintf("%.2fs ago", time.Since(t).Seconds())
}

func (c *PeerConn) connectionFlags() (ret string) {
	c := func(b byte) {
		ret += string([]byte{b})
	}
	if c.cryptoMethod == mse.CryptoMethodRC4 {
		c('E')
	} else if c.headerEncrypted {
		c('e')
	}
	ret += string(c.Discovery)
	if c.utp() {
		c('U')
	}
	return
}

func (c *PeerConn) utp() bool {
	return parseNetworkString(c.Network).Udp
}

// Inspired by https://github.com/transmission/transmission/wiki/Peer-Status-Text.
func (pc *Peer) statusFlags() (ret string) {
	c := func(b byte) {
		ret += string([]byte{b})
	}
	if pc.requestState.Interested {
		c('i')
	}
	if pc.choking {
		c('c')
	}
	c('-')
	ret += pc.connectionFlags()
	c('-')
	if pc.peerInterested {
		c('i')
	}
	if pc.peerChoking {
		c('c')
	}
	return
}

func (pc *Peer) downloadRate() float64 {
	num := pc._stats.BytesReadUsefulData.Int64()
	if num == 0 {
		return 0
	}
	return float64(num) / pc.totalExpectingTime().Seconds()
}

func (pc *Peer) DownloadRate() float64 {
	pc.locker().RLock()
	defer pc.locker().RUnlock()

	return pc.downloadRate()
}

func (pc *Peer) iterContiguousPieceRequests(f func(piece pieceIndex, count int)) {
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
	pc.requestState.Requests.Iterate(func(requestIndex request_strategy.RequestIndex) bool {
		next(Some(pc.t.pieceIndexOfRequestIndex(requestIndex)))
		return true
	})
	next(None[pieceIndex]())
}

func (pc *Peer) writeStatus(w io.Writer, t *Torrent) {
	// \t isn't preserved in <pre> blocks?
	if pc.closed.IsSet() {
		fmt.Fprint(w, "CLOSED: ")
	}
	fmt.Fprintln(w, pc.connStatusString())
	prio, err := pc.peerPriority()
	prioStr := fmt.Sprintf("%08x", prio)
	if err != nil {
		prioStr += ": " + err.Error()
	}
	fmt.Fprintf(w, "    bep40-prio: %v\n", prioStr)
	fmt.Fprintf(w, "    last msg: %s, connected: %s, last helpful: %s, itime: %s, etime: %s\n",
		eventAgeString(pc.lastMessageReceived),
		eventAgeString(pc.completedHandshake),
		eventAgeString(pc.lastHelpful()),
		pc.cumInterest(),
		pc.totalExpectingTime(),
	)
	fmt.Fprintf(w,
		"    %s completed, %d pieces touched, good chunks: %v/%v:%v reqq: %d+%v/(%d/%d):%d/%d, flags: %s, dr: %.1f KiB/s\n",
		pc.completedString(),
		len(pc.peerTouchedPieces),
		&pc._stats.ChunksReadUseful,
		&pc._stats.ChunksRead,
		&pc._stats.ChunksWritten,
		pc.requestState.Requests.GetCardinality(),
		pc.requestState.Cancelled.GetCardinality(),
		pc.nominalMaxRequests(),
		pc.PeerMaxRequests,
		len(pc.peerRequests),
		localClientReqq,
		pc.statusFlags(),
		pc.downloadRate()/(1<<10),
	)
	fmt.Fprintf(w, "    requested pieces:")
	pc.iterContiguousPieceRequests(func(piece pieceIndex, count int) {
		fmt.Fprintf(w, " %v(%v)", piece, count)
	})
	fmt.Fprintf(w, "\n")
}

func (pc *Peer) close() {
	if !pc.closed.Set() {
		return
	}
	if pc.updateRequestsTimer != nil {
		pc.updateRequestsTimer.Stop()
	}
	pc.peerImpl.onClose()
	if pc.t != nil {
		pc.t.decPeerPieceAvailability(pc)
	}
	for _, f := range pc.callbacks.PeerClosed {
		f(pc)
	}
}

func (c *PeerConn) onClose() {
	if c.pex.IsEnabled() {
		c.pex.Close()
	}
	c.tickleWriter()
	if c.conn != nil {
		go c.conn.Close()
	}
	if cb := c.callbacks.PeerConnClosed; cb != nil {
		cb(c)
	}
}

// Peer definitely has a piece, for purposes of requesting. So it's not sufficient that we think
// they do (known=true).
func (pc *Peer) peerHasPiece(piece pieceIndex) bool {
	if all, known := pc.peerHasAllPieces(); all && known {
		return true
	}
	return pc.peerPieces().ContainsInt(piece)
}

// 64KiB, but temporarily less to work around an issue with WebRTC. TODO: Update when
// https://github.com/pion/datachannel/issues/59 is fixed.
const (
	writeBufferHighWaterLen = 1 << 15
	writeBufferLowWaterLen  = writeBufferHighWaterLen / 2
)

// Writes a message into the write buffer. Returns whether it's okay to keep writing. Writing is
// done asynchronously, so it may be that we're not able to honour backpressure from this method.
func (c *PeerConn) write(msg pp.Message) bool {
	torrent.Add(fmt.Sprintf("messages written of type %s", msg.Type.String()), 1)
	// We don't need to track bytes here because the connection's Writer has that behaviour injected
	// (although there's some delay between us buffering the message, and the connection writer
	// flushing it out.).
	notFull := c.messageWriter.write(msg)
	// Last I checked only Piece messages affect stats, and we don't write those.
	c.wroteMsg(&msg)
	c.tickleWriter()
	return notFull
}

func (c *PeerConn) requestMetadataPiece(index int) {
	eID := c.PeerExtensionIDs[pp.ExtensionNameMetadata]
	if eID == pp.ExtensionDeleteNumber {
		return
	}
	if index < len(c.metadataRequests) && c.metadataRequests[index] {
		return
	}
	c.logger.WithDefaultLevel(log.Debug).Printf("requesting metadata piece %d", index)
	c.write(pp.MetadataExtensionRequestMsg(eID, index))
	for index >= len(c.metadataRequests) {
		c.metadataRequests = append(c.metadataRequests, false)
	}
	c.metadataRequests[index] = true
}

func (c *PeerConn) requestedMetadataPiece(index int) bool {
	return index < len(c.metadataRequests) && c.metadataRequests[index]
}

var (
	interestedMsgLen = len(pp.Message{Type: pp.Interested}.MustMarshalBinary())
	requestMsgLen    = len(pp.Message{Type: pp.Request}.MustMarshalBinary())
	// This is the maximum request count that could fit in the write buffer if it's at or below the
	// low water mark when we run maybeUpdateActualRequestState.
	maxLocalToRemoteRequests = (writeBufferHighWaterLen - writeBufferLowWaterLen - interestedMsgLen) / requestMsgLen
)

// The actual value to use as the maximum outbound requests.
func (pc *Peer) nominalMaxRequests() maxRequests {
	return maxInt(1, minInt(pc.PeerMaxRequests, pc.peakRequests*2, maxLocalToRemoteRequests))
}

func (pc *Peer) totalExpectingTime() (ret time.Duration) {
	ret = pc.cumulativeExpectedToReceiveChunks
	if !pc.lastStartedExpectingToReceiveChunks.IsZero() {
		ret += time.Since(pc.lastStartedExpectingToReceiveChunks)
	}
	return
}

func (c *PeerConn) onPeerSentCancel(r Request) {
	if _, ok := c.peerRequests[r]; !ok {
		torrent.Add("unexpected cancels received", 1)
		return
	}
	if c.fastEnabled() {
		c.reject(r)
	} else {
		delete(c.peerRequests, r)
	}
}

func (c *PeerConn) choke(msg messageWriter) (more bool) {
	if c.choking {
		return true
	}
	c.choking = true
	more = msg(pp.Message{
		Type: pp.Choke,
	})
	if !c.fastEnabled() {
		c.peerRequests = nil
	}
	return
}

func (c *PeerConn) unchoke(msg func(pp.Message) bool) bool {
	if !c.choking {
		return true
	}
	c.choking = false
	return msg(pp.Message{
		Type: pp.Unchoke,
	})
}

func (pc *Peer) setInterested(interested bool) bool {
	if pc.requestState.Interested == interested {
		return true
	}
	pc.requestState.Interested = interested
	if interested {
		pc.lastBecameInterested = time.Now()
	} else if !pc.lastBecameInterested.IsZero() {
		pc.priorInterest += time.Since(pc.lastBecameInterested)
	}
	pc.updateExpectingChunks()
	// log.Printf("%p: setting interest: %v", cn, interested)
	return pc.writeInterested(interested)
}

func (c *PeerConn) writeInterested(interested bool) bool {
	return c.write(pp.Message{
		Type: func() pp.MessageType {
			if interested {
				return pp.Interested
			} else {
				return pp.NotInterested
			}
		}(),
	})
}

// The function takes a message to be sent, and returns true if more messages
// are okay.
type messageWriter func(pp.Message) bool

// This function seems to only used by Peer.request. It's all logic checks, so maybe we can no-op it
// when we want to go fast.
func (pc *Peer) shouldRequest(r RequestIndex) error {
	pi := pc.t.pieceIndexOfRequestIndex(r)
	if pc.requestState.Cancelled.Contains(r) {
		return errors.New("request is cancelled and waiting acknowledgement")
	}
	if !pc.peerHasPiece(pi) {
		return errors.New("requesting piece peer doesn't have")
	}
	if !pc.t.peerIsActive(pc) {
		panic("requesting but not in active conns")
	}
	if pc.closed.IsSet() {
		panic("requesting when connection is closed")
	}
	if pc.t.hashingPiece(pi) {
		panic("piece is being hashed")
	}
	if pc.t.pieceQueuedForHash(pi) {
		panic("piece is queued for hash")
	}
	if pc.peerChoking && !pc.peerAllowedFast.Contains(pi) {
		// This could occur if we made a request with the fast extension, and then got choked and
		// haven't had the request rejected yet.
		if !pc.requestState.Requests.Contains(r) {
			panic("peer choking and piece not allowed fast")
		}
	}
	return nil
}

func (pc *Peer) mustRequest(r RequestIndex) bool {
	more, err := pc.request(r)
	if err != nil {
		panic(err)
	}
	return more
}

func (pc *Peer) request(r RequestIndex) (more bool, err error) {
	if err := pc.shouldRequest(r); err != nil {
		panic(err)
	}
	if pc.requestState.Requests.Contains(r) {
		return true, nil
	}
	if maxRequests(pc.requestState.Requests.GetCardinality()) >= pc.nominalMaxRequests() {
		return true, errors.New("too many outstanding requests")
	}
	pc.requestState.Requests.Add(r)
	if pc.validReceiveChunks == nil {
		pc.validReceiveChunks = make(map[RequestIndex]int)
	}
	pc.validReceiveChunks[r]++
	pc.t.requestState[r] = requestState{
		peer: pc,
		when: time.Now(),
	}
	pc.updateExpectingChunks()
	ppReq := pc.t.requestIndexToRequest(r)
	for _, f := range pc.callbacks.SentRequest {
		f(PeerRequestEvent{pc, ppReq})
	}
	return pc.peerImpl._request(ppReq), nil
}

func (c *PeerConn) _request(r Request) bool {
	return c.write(pp.Message{
		Type:   pp.Request,
		Index:  r.Index,
		Begin:  r.Begin,
		Length: r.Length,
	})
}

func (pc *Peer) cancel(r RequestIndex) {
	if !pc.deleteRequest(r) {
		panic("request not existing should have been guarded")
	}
	if pc._cancel(r) {
		if !pc.requestState.Cancelled.CheckedAdd(r) {
			panic("request already cancelled")
		}
	}
	pc.decPeakRequests()
	if pc.isLowOnRequests() {
		pc.updateRequests("Peer.cancel")
	}
}

func (c *PeerConn) _cancel(r RequestIndex) bool {
	c.write(makeCancelMessage(c.t.requestIndexToRequest(r)))
	// Transmission does not send rejects for received cancels. See
	// https://github.com/transmission/transmission/pull/2275.
	return c.fastEnabled() && !c.remoteIsTransmission()
}

func (c *PeerConn) fillWriteBuffer() {
	if c.messageWriter.writeBuffer.Len() > writeBufferLowWaterLen {
		// Fully committing to our max requests requires sufficient space (see
		// maxLocalToRemoteRequests). Flush what we have instead. We also prefer always to make
		// requests than to do PEX or upload, so we short-circuit before handling those. Any update
		// request reason will not be cleared, so we'll come right back here when there's space. We
		// can't do this in maybeUpdateActualRequestState because it's a method on Peer and has no
		// knowledge of write buffers.
	}
	c.maybeUpdateActualRequestState()
	if c.pex.IsEnabled() {
		if flow := c.pex.Share(c.write); !flow {
			return
		}
	}
	c.upload(c.write)
}

func (c *PeerConn) have(piece pieceIndex) {
	if c.sentHaves.Get(bitmap.BitIndex(piece)) {
		return
	}
	c.write(pp.Message{
		Type:  pp.Have,
		Index: pp.Integer(piece),
	})
	c.sentHaves.Add(bitmap.BitIndex(piece))
}

func (c *PeerConn) postBitfield() {
	if c.sentHaves.Len() != 0 {
		panic("bitfield must be first have-related message sent")
	}
	if !c.t.haveAnyPieces() {
		return
	}
	c.write(pp.Message{
		Type:     pp.Bitfield,
		Bitfield: c.t.bitfield(),
	})
	c.sentHaves = bitmap.Bitmap{c.t._completedPieces.Clone()}
}

// Sets a reason to update requests, and if there wasn't already one, handle it.
func (pc *Peer) updateRequests(reason string) {
	if pc.needRequestUpdate != "" {
		return
	}
	if reason != peerUpdateRequestsTimerReason && !pc.isLowOnRequests() {
		return
	}
	pc.needRequestUpdate = reason
	pc.handleUpdateRequests()
}

func (c *PeerConn) handleUpdateRequests() {
	// The writer determines the request state as needed when it can write.
	c.tickleWriter()
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

func (pc *Peer) peerPiecesChanged() {
	pc.t.maybeDropMutuallyCompletePeer(pc)
}

func (c *PeerConn) raisePeerMinPieces(newMin pieceIndex) {
	if newMin > c.peerMinPieces {
		c.peerMinPieces = newMin
	}
}

func (c *PeerConn) peerSentHave(piece pieceIndex) error {
	if c.t.haveInfo() && piece >= c.t.numPieces() || piece < 0 {
		return errors.New("invalid piece")
	}
	if c.peerHasPiece(piece) {
		return nil
	}
	c.raisePeerMinPieces(piece + 1)
	if !c.peerHasPiece(piece) {
		c.t.incPieceAvailability(piece)
	}
	c._peerPieces.Add(uint32(piece))
	if c.t.wantPieceIndex(piece) {
		c.updateRequests("have")
	}
	c.peerPiecesChanged()
	return nil
}

func (c *PeerConn) peerSentBitfield(bf []bool) error {
	if len(bf)%8 != 0 {
		panic("expected bitfield length divisible by 8")
	}
	// We know that the last byte means that at most the last 7 bits are wasted.
	c.raisePeerMinPieces(pieceIndex(len(bf) - 7))
	if c.t.haveInfo() && len(bf) > int(c.t.numPieces()) {
		// Ignore known excess pieces.
		bf = bf[:c.t.numPieces()]
	}
	bm := boolSliceToBitmap(bf)
	if c.t.haveInfo() && pieceIndex(bm.GetCardinality()) == c.t.numPieces() {
		c.onPeerHasAllPieces()
		return nil
	}
	if !bm.IsEmpty() {
		c.raisePeerMinPieces(pieceIndex(bm.Maximum()) + 1)
	}
	shouldUpdateRequests := false
	if c.peerSentHaveAll {
		if !c.t.deleteConnWithAllPieces(&c.Peer) {
			panic(c)
		}
		c.peerSentHaveAll = false
		if !c._peerPieces.IsEmpty() {
			panic("if peer has all, we expect no individual peer pieces to be set")
		}
	} else {
		bm.Xor(&c._peerPieces)
	}
	c.peerSentHaveAll = false
	// bm is now 'on' for pieces that are changing
	bm.Iterate(func(x uint32) bool {
		pi := pieceIndex(x)
		if c._peerPieces.Contains(x) {
			// Then we must be losing this piece
			c.t.decPieceAvailability(pi)
		} else {
			if !shouldUpdateRequests && c.t.wantPieceIndex(pieceIndex(x)) {
				shouldUpdateRequests = true
			}
			// We must be gaining this piece
			c.t.incPieceAvailability(pieceIndex(x))
		}
		return true
	})
	// Apply the changes. If we had everything previously, this should be empty, so xor is the same
	// as or.
	c._peerPieces.Xor(&bm)
	if shouldUpdateRequests {
		c.updateRequests("bitfield")
	}
	// We didn't guard this before, I see no reason to do it now.
	c.peerPiecesChanged()
	return nil
}

func (c *PeerConn) onPeerHasAllPieces() {
	t := c.t
	if t.haveInfo() {
		c._peerPieces.Iterate(func(x uint32) bool {
			t.decPieceAvailability(pieceIndex(x))
			return true
		})
	}
	t.addConnWithAllPieces(&c.Peer)
	c.peerSentHaveAll = true
	c._peerPieces.Clear()
	if !c.t._pendingPieces.IsEmpty() {
		c.updateRequests("Peer.onPeerHasAllPieces")
	}
	c.peerPiecesChanged()
}

func (c *PeerConn) onPeerSentHaveAll() error {
	c.onPeerHasAllPieces()
	return nil
}

func (c *PeerConn) peerSentHaveNone() error {
	if c.peerSentHaveAll {
		c.t.decPeerPieceAvailability(&c.Peer)
	}
	c._peerPieces.Clear()
	c.peerSentHaveAll = false
	c.peerPiecesChanged()
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

func (c *PeerConn) wroteMsg(msg *pp.Message) {
	torrent.Add(fmt.Sprintf("messages written of type %s", msg.Type.String()), 1)
	if msg.Type == pp.Extended {
		for name, id := range c.PeerExtensionIDs {
			if id != msg.ExtendedID {
				continue
			}
			torrent.Add(fmt.Sprintf("Extended messages written for protocol %q", name), 1)
		}
	}
	c.allStats(func(cs *ConnStats) { cs.wroteMsg(msg) })
}

// After handshake, we know what Torrent and Client stats to include for a
// connection.
func (pc *Peer) postHandshakeStats(f func(*ConnStats)) {
	t := pc.t
	f(&t.stats)
	f(&t.cl.stats)
}

// All ConnStats that include this connection. Some objects are not known
// until the handshake is complete, after which it's expected to reconcile the
// differences.
func (pc *Peer) allStats(f func(*ConnStats)) {
	f(&pc._stats)
	if pc.reconciledHandshakeStats {
		pc.postHandshakeStats(f)
	}
}

func (c *PeerConn) wroteBytes(n int64) {
	c.allStats(add(n, func(cs *ConnStats) *Count { return &cs.BytesWritten }))
}

func (pc *Peer) readBytes(n int64) {
	pc.allStats(add(n, func(cs *ConnStats) *Count { return &cs.BytesRead }))
}

// Returns whether the connection could be useful to us. We're seeding and
// they want data, we don't have metainfo and they can provide it, etc.
func (pc *Peer) useful() bool {
	t := pc.t
	if pc.closed.IsSet() {
		return false
	}
	if !t.haveInfo() {
		return pc.supportsExtension("ut_metadata")
	}
	if t.seeding() && pc.peerInterested {
		return true
	}
	if pc.peerHasWantedPieces() {
		return true
	}
	return false
}

func (pc *Peer) lastHelpful() (ret time.Time) {
	ret = pc.lastUsefulChunkReceived
	if pc.t.seeding() && pc.lastChunkSent.After(ret) {
		ret = pc.lastChunkSent
	}
	return
}

func (c *PeerConn) fastEnabled() bool {
	return c.PeerExtensionBytes.SupportsFast() && c.t.cl.config.Extensions.SupportsFast()
}

func (c *PeerConn) reject(r Request) {
	if !c.fastEnabled() {
		panic("fast not enabled")
	}
	c.write(r.ToMsg(pp.Reject))
	delete(c.peerRequests, r)
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
	// Check this after we know we have the piece, so that the piece length will be known.
	if r.Begin+r.Length > c.t.pieceLength(pieceIndex(r.Index)) {
		torrent.Add("bad requests received", 1)
		return errors.New("bad Request")
	}
	if c.peerRequests == nil {
		c.peerRequests = make(map[Request]*peerRequestState, localClientReqq)
	}
	value := &peerRequestState{}
	c.peerRequests[r] = value
	if startFetch {
		// TODO: Limit peer request data read concurrency.
		go c.peerRequestDataReader(r, value)
	}
	return nil
}

func (c *PeerConn) peerRequestDataReader(r Request, prs *peerRequestState) {
	b, err := readPeerRequestData(r, c)
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
	c.logger.WithDefaultLevel(logLevel).Printf("error reading chunk for peer Request %v: %v", r, err)
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

func readPeerRequestData(r Request, c *PeerConn) ([]byte, error) {
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

func runSafeExtraneous(f func()) {
	if true {
		go f()
	} else {
		f()
	}
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
				c.logger.Printf("received invalid reject [request=%v, peer=%v]", req, c)
				err = fmt.Errorf("received invalid reject [request=%v]", req)
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

// Returns true if it was valid to reject the request.
func (pc *Peer) remoteRejectedRequest(r RequestIndex) bool {
	if pc.deleteRequest(r) {
		pc.decPeakRequests()
	} else if !pc.requestState.Cancelled.CheckedRemove(r) {
		return false
	}
	if pc.isLowOnRequests() {
		pc.updateRequests("Peer.remoteRejectedRequest")
	}
	pc.decExpectedChunkReceive(r)
	return true
}

func (pc *Peer) decExpectedChunkReceive(r RequestIndex) {
	count := pc.validReceiveChunks[r]
	if count == 1 {
		delete(pc.validReceiveChunks, r)
	} else if count > 1 {
		pc.validReceiveChunks[r] = count - 1
	} else {
		panic(r)
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
		return c.pex.Recv(payload)
	default:
		return fmt.Errorf("unexpected extended message ID: %v", id)
	}
}

// Set both the Reader and Writer for the connection from a single ReadWriter.
func (c *PeerConn) setRW(rw io.ReadWriter) {
	c.r = rw
	c.w = rw
}

// Returns the Reader and Writer as a combined ReadWriter.
func (c *PeerConn) rw() io.ReadWriter {
	return struct {
		io.Reader
		io.Writer
	}{c.r, c.w}
}

func (pc *Peer) doChunkReadStats(size int64) {
	pc.allStats(func(cs *ConnStats) { cs.receivedChunk(size) })
}

// Handle a received chunk from a peer.
func (pc *Peer) receiveChunk(msg *pp.Message) error {
	chunksReceived.Add("total", 1)

	ppReq := newRequestFromMessage(msg)
	req := pc.t.requestIndexFromRequest(ppReq)
	t := pc.t

	if pc.bannableAddr.Ok {
		t.smartBanCache.RecordBlock(pc.bannableAddr.Value, req, msg.Piece)
	}

	if pc.peerChoking {
		chunksReceived.Add("while choked", 1)
	}

	if pc.validReceiveChunks[req] <= 0 {
		chunksReceived.Add("unexpected", 1)
		return errors.New("received unexpected chunk")
	}
	pc.decExpectedChunkReceive(req)

	if pc.peerChoking && pc.peerAllowedFast.Contains(pieceIndex(ppReq.Index)) {
		chunksReceived.Add("due to allowed fast", 1)
	}

	// The request needs to be deleted immediately to prevent cancels occurring asynchronously when
	// have actually already received the piece, while we have the Client unlocked to write the data
	// out.
	intended := false
	{
		if pc.requestState.Requests.Contains(req) {
			for _, f := range pc.callbacks.ReceivedRequested {
				f(PeerMessageEvent{pc, msg})
			}
		}
		// Request has been satisfied.
		if pc.deleteRequest(req) || pc.requestState.Cancelled.CheckedRemove(req) {
			intended = true
			if !pc.peerChoking {
				pc._chunksReceivedWhileExpecting++
			}
			if pc.isLowOnRequests() {
				pc.updateRequests("Peer.receiveChunk deleted request")
			}
		} else {
			chunksReceived.Add("unintended", 1)
		}
	}

	cl := t.cl

	// Do we actually want this chunk?
	if t.haveChunk(ppReq) {
		// panic(fmt.Sprintf("%+v", ppReq))
		chunksReceived.Add("redundant", 1)
		pc.allStats(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadWasted }))
		return nil
	}

	piece := &t.pieces[ppReq.Index]

	pc.allStats(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadUseful }))
	pc.allStats(add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulData }))
	if intended {
		pc.piecesReceivedSinceLastRequestUpdate++
		pc.allStats(add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulIntendedData }))
	}
	for _, f := range pc.t.cl.config.Callbacks.ReceivedUsefulData {
		f(ReceivedUsefulDataEvent{pc, msg})
	}
	pc.lastUsefulChunkReceived = time.Now()

	// Need to record that it hasn't been written yet, before we attempt to do
	// anything with it.
	piece.incrementPendingWrites()
	// Record that we have the chunk, so we aren't trying to download it while
	// waiting for it to be written to storage.
	piece.unpendChunkIndex(chunkIndexFromChunkSpec(ppReq.ChunkSpec, t.chunkSize))

	// Cancel pending requests for this chunk from *other* peers.
	if p := t.requestingPeer(req); p != nil {
		if p == p {
			panic("should not be pending request from conn that just received it")
		}
		p.cancel(req)
	}

	err := func() error {
		cl.unlock()
		defer cl.lock()
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
		pc.logger.WithDefaultLevel(log.Error).Printf("writing received chunk %v: %v", req, err)
		t.pendRequest(req)
		// Necessary to pass TestReceiveChunkStorageFailureSeederFastExtensionDisabled. I think a
		// request update runs while we're writing the chunk that just failed. Then we never do a
		// fresh update after pending the failed request.
		pc.updateRequests("Peer.receiveChunk error writing chunk")
		t.onWriteChunkErr(err)
		return nil
	}

	pc.onDirtiedPiece(pieceIndex(ppReq.Index))

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
	t.publishPieceChange(pieceIndex(ppReq.Index))

	return nil
}

func (pc *Peer) onDirtiedPiece(piece pieceIndex) {
	if pc.peerTouchedPieces == nil {
		pc.peerTouchedPieces = make(map[pieceIndex]struct{})
	}
	pc.peerTouchedPieces[piece] = struct{}{}
	ds := &pc.t.pieces[piece].dirtiers
	if *ds == nil {
		*ds = make(map[*Peer]struct{})
	}
	(*ds)[pc] = struct{}{}
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

func (c *PeerConn) drop() {
	c.t.dropConnection(c)
}

func (c *PeerConn) ban() {
	c.t.cl.banPeerIP(c.remoteIp())
}

func (pc *Peer) netGoodPiecesDirtied() int64 {
	return pc._stats.PiecesDirtiedGood.Int64() - pc._stats.PiecesDirtiedBad.Int64()
}

func (pc *Peer) peerHasWantedPieces() bool {
	if all, _ := pc.peerHasAllPieces(); all {
		return !pc.t.haveAllPieces() && !pc.t._pendingPieces.IsEmpty()
	}
	if !pc.t.haveInfo() {
		return !pc.peerPieces().IsEmpty()
	}
	return pc.peerPieces().Intersects(&pc.t._pendingPieces)
}

// Returns true if an outstanding request is removed. Cancelled requests should be handled
// separately.
func (pc *Peer) deleteRequest(r RequestIndex) bool {
	if !pc.requestState.Requests.CheckedRemove(r) {
		return false
	}
	for _, f := range pc.callbacks.DeletedRequest {
		f(PeerRequestEvent{pc, pc.t.requestIndexToRequest(r)})
	}
	pc.updateExpectingChunks()
	if pc.t.requestingPeer(r) != pc {
		panic("only one peer should have a given request at a time")
	}
	delete(pc.t.requestState, r)
	// c.t.iterPeers(func(p *Peer) {
	// 	if p.isLowOnRequests() {
	// 		p.updateRequests("Peer.deleteRequest")
	// 	}
	// })
	return true
}

func (pc *Peer) deleteAllRequests(reason string) {
	if pc.requestState.Requests.IsEmpty() {
		return
	}
	pc.requestState.Requests.IterateSnapshot(func(x RequestIndex) bool {
		if !pc.deleteRequest(x) {
			panic("request should exist")
		}
		return true
	})
	pc.assertNoRequests()
	pc.t.iterPeers(func(p *Peer) {
		if p.isLowOnRequests() {
			p.updateRequests(reason)
		}
	})
	return
}

func (pc *Peer) assertNoRequests() {
	if !pc.requestState.Requests.IsEmpty() {
		panic(pc.requestState.Requests.GetCardinality())
	}
}

func (pc *Peer) cancelAllRequests() {
	pc.requestState.Requests.IterateSnapshot(func(x RequestIndex) bool {
		pc.cancel(x)
		return true
	})
	pc.assertNoRequests()
	return
}

// This is called when something has changed that should wake the writer, such as putting stuff into
// the writeBuffer, or changing some state that the writer can act on.
func (c *PeerConn) tickleWriter() {
	c.messageWriter.writeCond.Broadcast()
}

func (c *PeerConn) sendChunk(r Request, msg func(pp.Message) bool, state *peerRequestState) (more bool) {
	c.lastChunkSent = time.Now()
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

func (pc *Peer) peerPriority() (peerPriority, error) {
	return bep40Priority(pc.remoteIpPort(), pc.localPublicAddr)
}

func (pc *Peer) remoteIp() net.IP {
	host, _, _ := net.SplitHostPort(pc.RemoteAddr.String())
	return net.ParseIP(host)
}

func (pc *Peer) remoteIpPort() IpPort {
	ipa, _ := tryIpPortFromNetAddr(pc.RemoteAddr)
	return IpPort{ipa.IP, uint16(ipa.Port)}
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
	if !c.outgoing && c.PeerListenPort != 0 {
		switch addr := c.RemoteAddr.(type) {
		case *net.TCPAddr:
			dialAddr := *addr
			dialAddr.Port = c.PeerListenPort
			return &dialAddr
		case *net.UDPAddr:
			dialAddr := *addr
			dialAddr.Port = c.PeerListenPort
			return &dialAddr
		}
	}
	return c.RemoteAddr
}

func (c *PeerConn) pexEvent(t pexEventType) pexEvent {
	f := c.pexPeerFlags()
	addr := c.dialAddr()
	return pexEvent{t, addr, f, nil}
}

func (c *PeerConn) String() string {
	return fmt.Sprintf("%T %p [id=%q, exts=%v, v=%q]", c, c, c.PeerID, c.PeerExtensionBytes, c.PeerClientName.Load())
}

func (pc *Peer) trust() connectionTrust {
	return connectionTrust{pc.trusted, pc.netGoodPiecesDirtied()}
}

type connectionTrust struct {
	Implicit            bool
	NetGoodPiecesDirted int64
}

func (l connectionTrust) Less(r connectionTrust) bool {
	return multiless.New().Bool(l.Implicit, r.Implicit).Int64(l.NetGoodPiecesDirted, r.NetGoodPiecesDirted).Less()
}

// Returns the pieces the peer could have based on their claims. If we don't know how many pieces
// are in the torrent, it could be a very large range the peer has sent HaveAll.
func (c *PeerConn) PeerPieces() *roaring.Bitmap {
	c.locker().RLock()
	defer c.locker().RUnlock()
	return c.newPeerPieces()
}

// Returns a new Bitmap that includes bits for all pieces the peer could have based on their claims.
func (pc *Peer) newPeerPieces() *roaring.Bitmap {
	// TODO: Can we use copy on write?
	ret := pc.peerPieces().Clone()
	if all, _ := pc.peerHasAllPieces(); all {
		if pc.t.haveInfo() {
			ret.AddRange(0, bitmap.BitRange(pc.t.numPieces()))
		} else {
			ret.AddRange(0, bitmap.ToEnd)
		}
	}
	return ret
}

func (pc *Peer) stats() *ConnStats {
	return &pc._stats
}

func (pc *Peer) TryAsPeerConn() (*PeerConn, bool) {
	pc, ok := pc.peerImpl.(*PeerConn)
	return pc, ok
}

func (pc *Peer) uncancelledRequests() uint64 {
	return pc.requestState.Requests.GetCardinality()
}

func (c *PeerConn) remoteIsTransmission() bool {
	return bytes.HasPrefix(c.PeerID[:], []byte("-TR")) && c.PeerID[7] == '-'
}
