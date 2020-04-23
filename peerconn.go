package torrent

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/missinggo/iter"
	"github.com/anacrolix/missinggo/v2/bitmap"
	"github.com/anacrolix/missinggo/v2/prioritybitmap"
	"github.com/anacrolix/multiless"
	"github.com/pkg/errors"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/mse"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

type PeerSource string

const (
	PeerSourceTracker         = "Tr"
	PeerSourceIncoming        = "I"
	PeerSourceDhtGetPeers     = "Hg" // Peers we found by searching a DHT.
	PeerSourceDhtAnnouncePeer = "Ha" // Peers that were announced to us by a DHT.
	PeerSourcePex             = "X"
)

// Maintains the state of a connection with a peer.
type PeerConn struct {
	// First to ensure 64-bit alignment for atomics. See #262.
	_stats ConnStats

	t *Torrent
	// The actual Conn, used for closing, and setting socket options.
	conn       net.Conn
	outgoing   bool
	network    string
	remoteAddr net.Addr
	// The Reader and Writer for this Conn, with hooks installed for stats,
	// limiting, deadlines etc.
	w io.Writer
	r io.Reader
	// True if the connection is operating over MSE obfuscation.
	headerEncrypted bool
	cryptoMethod    mse.CryptoMethod
	Discovery       PeerSource
	trusted         bool
	closed          missinggo.Event
	// Set true after we've added our ConnStats generated during handshake to
	// other ConnStat instances as determined when the *Torrent became known.
	reconciledHandshakeStats bool

	lastMessageReceived     time.Time
	completedHandshake      time.Time
	lastUsefulChunkReceived time.Time
	lastChunkSent           time.Time

	// Stuff controlled by the local peer.
	interested           bool
	lastBecameInterested time.Time
	priorInterest        time.Duration

	lastStartedExpectingToReceiveChunks time.Time
	cumulativeExpectedToReceiveChunks   time.Duration
	_chunksReceivedWhileExpecting       int64

	choking          bool
	requests         map[request]struct{}
	requestsLowWater int
	// Chunks that we might reasonably expect to receive from the peer. Due to
	// latency, buffering, and implementation differences, we may receive
	// chunks that are no longer in the set of requests actually want.
	validReceiveChunks map[request]struct{}
	// Indexed by metadata piece, set to true if posted and pending a
	// response.
	metadataRequests []bool
	sentHaves        bitmap.Bitmap

	// Stuff controlled by the remote peer.
	PeerID             PeerID
	peerInterested     bool
	peerChoking        bool
	peerRequests       map[request]struct{}
	PeerExtensionBytes pp.PeerExtensionBits
	// The pieces the peer has claimed to have.
	_peerPieces bitmap.Bitmap
	// The peer has everything. This can occur due to a special message, when
	// we may not even know the number of pieces in the torrent yet.
	peerSentHaveAll bool
	// The highest possible number of pieces the torrent could have based on
	// communication with the peer. Generally only useful until we have the
	// torrent info.
	peerMinPieces pieceIndex
	// Pieces we've accepted chunks for from the peer.
	peerTouchedPieces map[pieceIndex]struct{}
	peerAllowedFast   bitmap.Bitmap

	PeerMaxRequests  int // Maximum pending requests the peer allows.
	PeerExtensionIDs map[pp.ExtensionName]pp.ExtensionNumber
	PeerClientName   string

	pieceInclination   []int
	_pieceRequestOrder prioritybitmap.PriorityBitmap

	writeBuffer *bytes.Buffer
	uploadTimer *time.Timer
	writerCond  sync.Cond

	logger log.Logger
}

func (cn *PeerConn) updateExpectingChunks() {
	if cn.expectingChunks() {
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

func (cn *PeerConn) expectingChunks() bool {
	return cn.interested && !cn.peerChoking
}

// Returns true if the connection is over IPv6.
func (cn *PeerConn) ipv6() bool {
	ip := addrIpOrNil(cn.remoteAddr)
	if ip.To4() != nil {
		return false
	}
	return len(ip) == net.IPv6len
}

// Returns true the if the dialer/initiator has the lower client peer ID. TODO: Find the
// specification for this.
func (cn *PeerConn) isPreferredDirection() bool {
	return bytes.Compare(cn.t.cl.peerID[:], cn.PeerID[:]) < 0 == cn.outgoing
}

// Returns whether the left connection should be preferred over the right one,
// considering only their networking properties. If ok is false, we can't
// decide.
func (l *PeerConn) hasPreferredNetworkOver(r *PeerConn) (left, ok bool) {
	var ml multiLess
	ml.NextBool(l.isPreferredDirection(), r.isPreferredDirection())
	ml.NextBool(!l.utp(), !r.utp())
	ml.NextBool(l.ipv6(), r.ipv6())
	return ml.FinalOk()
}

func (cn *PeerConn) cumInterest() time.Duration {
	ret := cn.priorInterest
	if cn.interested {
		ret += time.Since(cn.lastBecameInterested)
	}
	return ret
}

func (cn *PeerConn) peerHasAllPieces() (all bool, known bool) {
	if cn.peerSentHaveAll {
		return true, true
	}
	if !cn.t.haveInfo() {
		return false, false
	}
	return bitmap.Flip(cn._peerPieces, 0, bitmap.BitIndex(cn.t.numPieces())).IsEmpty(), true
}

func (cn *PeerConn) locker() *lockWithDeferreds {
	return cn.t.cl.locker()
}

func (cn *PeerConn) localAddr() net.Addr {
	return cn.conn.LocalAddr()
}

func (cn *PeerConn) supportsExtension(ext pp.ExtensionName) bool {
	_, ok := cn.PeerExtensionIDs[ext]
	return ok
}

// The best guess at number of pieces in the torrent for this peer.
func (cn *PeerConn) bestPeerNumPieces() pieceIndex {
	if cn.t.haveInfo() {
		return cn.t.numPieces()
	}
	return cn.peerMinPieces
}

func (cn *PeerConn) completedString() string {
	have := pieceIndex(cn._peerPieces.Len())
	if cn.peerSentHaveAll {
		have = cn.bestPeerNumPieces()
	}
	return fmt.Sprintf("%d/%d", have, cn.bestPeerNumPieces())
}

// Correct the PeerPieces slice length. Return false if the existing slice is
// invalid, such as by receiving badly sized BITFIELD, or invalid HAVE
// messages.
func (cn *PeerConn) setNumPieces(num pieceIndex) error {
	cn._peerPieces.RemoveRange(bitmap.BitIndex(num), bitmap.ToEnd)
	cn.peerPiecesChanged()
	return nil
}

func eventAgeString(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return fmt.Sprintf("%.2fs ago", time.Since(t).Seconds())
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
	return parseNetworkString(cn.network).Udp
}

// Inspired by https://github.com/transmission/transmission/wiki/Peer-Status-Text.
func (cn *PeerConn) statusFlags() (ret string) {
	c := func(b byte) {
		ret += string([]byte{b})
	}
	if cn.interested {
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

// func (cn *connection) String() string {
// 	var buf bytes.Buffer
// 	cn.writeStatus(&buf, nil)
// 	return buf.String()
// }

func (cn *PeerConn) downloadRate() float64 {
	return float64(cn._stats.BytesReadUsefulData.Int64()) / cn.cumInterest().Seconds()
}

func (cn *PeerConn) writeStatus(w io.Writer, t *Torrent) {
	// \t isn't preserved in <pre> blocks?
	fmt.Fprintf(w, "%+-55q %s %s-%s\n", cn.PeerID, cn.PeerExtensionBytes, cn.localAddr(), cn.remoteAddr)
	fmt.Fprintf(w, "    last msg: %s, connected: %s, last helpful: %s, itime: %s, etime: %s\n",
		eventAgeString(cn.lastMessageReceived),
		eventAgeString(cn.completedHandshake),
		eventAgeString(cn.lastHelpful()),
		cn.cumInterest(),
		cn.totalExpectingTime(),
	)
	fmt.Fprintf(w,
		"    %s completed, %d pieces touched, good chunks: %v/%v-%v reqq: (%d,%d,%d]-%d, flags: %s, dr: %.1f KiB/s\n",
		cn.completedString(),
		len(cn.peerTouchedPieces),
		&cn._stats.ChunksReadUseful,
		&cn._stats.ChunksRead,
		&cn._stats.ChunksWritten,
		cn.requestsLowWater,
		cn.numLocalRequests(),
		cn.nominalMaxRequests(),
		len(cn.peerRequests),
		cn.statusFlags(),
		cn.downloadRate()/(1<<10),
	)
	fmt.Fprintf(w, "    next pieces: %v%s\n",
		iter.ToSlice(iter.Head(10, cn.iterPendingPiecesUntyped)),
		func() string {
			if cn == t.fastestConn {
				return " (fastest)"
			} else {
				return ""
			}
		}(),
	)
}

func (cn *PeerConn) close() {
	if !cn.closed.Set() {
		return
	}
	cn.tickleWriter()
	cn.discardPieceInclination()
	cn._pieceRequestOrder.Clear()
	if cn.conn != nil {
		go cn.conn.Close()
	}
}

func (cn *PeerConn) peerHasPiece(piece pieceIndex) bool {
	return cn.peerSentHaveAll || cn._peerPieces.Contains(bitmap.BitIndex(piece))
}

// Writes a message into the write buffer.
func (cn *PeerConn) post(msg pp.Message) {
	torrent.Add(fmt.Sprintf("messages posted of type %s", msg.Type.String()), 1)
	// We don't need to track bytes here because a connection.w Writer wrapper
	// takes care of that (although there's some delay between us recording
	// the message, and the connection writer flushing it out.).
	cn.writeBuffer.Write(msg.MustMarshalBinary())
	// Last I checked only Piece messages affect stats, and we don't post
	// those.
	cn.wroteMsg(&msg)
	cn.tickleWriter()
}

func (cn *PeerConn) requestMetadataPiece(index int) {
	eID := cn.PeerExtensionIDs[pp.ExtensionNameMetadata]
	if eID == 0 {
		return
	}
	if index < len(cn.metadataRequests) && cn.metadataRequests[index] {
		return
	}
	cn.logger.Printf("requesting metadata piece %d", index)
	cn.post(pp.Message{
		Type:       pp.Extended,
		ExtendedID: eID,
		ExtendedPayload: func() []byte {
			b, err := bencode.Marshal(map[string]int{
				"msg_type": pp.RequestMetadataExtensionMsgType,
				"piece":    index,
			})
			if err != nil {
				panic(err)
			}
			return b
		}(),
	})
	for index >= len(cn.metadataRequests) {
		cn.metadataRequests = append(cn.metadataRequests, false)
	}
	cn.metadataRequests[index] = true
}

func (cn *PeerConn) requestedMetadataPiece(index int) bool {
	return index < len(cn.metadataRequests) && cn.metadataRequests[index]
}

// The actual value to use as the maximum outbound requests.
func (cn *PeerConn) nominalMaxRequests() (ret int) {
	return int(clamp(
		1,
		int64(cn.PeerMaxRequests),
		int64(cn.t.requestStrategy.nominalMaxRequests(cn.requestStrategyConnection())),
	))
}

func (cn *PeerConn) totalExpectingTime() (ret time.Duration) {
	ret = cn.cumulativeExpectedToReceiveChunks
	if !cn.lastStartedExpectingToReceiveChunks.IsZero() {
		ret += time.Since(cn.lastStartedExpectingToReceiveChunks)
	}
	return

}

func (cn *PeerConn) onPeerSentCancel(r request) {
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
	if cn.fastEnabled() {
		for r := range cn.peerRequests {
			// TODO: Don't reject pieces in allowed fast set.
			cn.reject(r)
		}
	} else {
		cn.peerRequests = nil
	}
	return
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

func (cn *PeerConn) setInterested(interested bool, msg func(pp.Message) bool) bool {
	if cn.interested == interested {
		return true
	}
	cn.interested = interested
	if interested {
		cn.lastBecameInterested = time.Now()
	} else if !cn.lastBecameInterested.IsZero() {
		cn.priorInterest += time.Since(cn.lastBecameInterested)
	}
	cn.updateExpectingChunks()
	// log.Printf("%p: setting interest: %v", cn, interested)
	return msg(pp.Message{
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

// Proxies the messageWriter's response.
func (cn *PeerConn) request(r request, mw messageWriter) bool {
	if _, ok := cn.requests[r]; ok {
		panic("chunk already requested")
	}
	if !cn.peerHasPiece(pieceIndex(r.Index)) {
		panic("requesting piece peer doesn't have")
	}
	if _, ok := cn.t.conns[cn]; !ok {
		panic("requesting but not in active conns")
	}
	if cn.closed.IsSet() {
		panic("requesting when connection is closed")
	}
	if cn.peerChoking {
		if cn.peerAllowedFast.Get(int(r.Index)) {
			torrent.Add("allowed fast requests sent", 1)
		} else {
			panic("requesting while choking and not allowed fast")
		}
	}
	if cn.t.hashingPiece(pieceIndex(r.Index)) {
		panic("piece is being hashed")
	}
	if cn.t.pieceQueuedForHash(pieceIndex(r.Index)) {
		panic("piece is queued for hash")
	}
	if cn.requests == nil {
		cn.requests = make(map[request]struct{})
	}
	cn.requests[r] = struct{}{}
	if cn.validReceiveChunks == nil {
		cn.validReceiveChunks = make(map[request]struct{})
	}
	cn.validReceiveChunks[r] = struct{}{}
	cn.t.pendingRequests[r]++
	cn.t.requestStrategy.hooks().sentRequest(r)
	cn.updateExpectingChunks()
	return mw(pp.Message{
		Type:   pp.Request,
		Index:  r.Index,
		Begin:  r.Begin,
		Length: r.Length,
	})
}

func (cn *PeerConn) fillWriteBuffer(msg func(pp.Message) bool) {
	if !cn.t.networkingEnabled || cn.t.dataDownloadDisallowed {
		if !cn.setInterested(false, msg) {
			return
		}
		if len(cn.requests) != 0 {
			for r := range cn.requests {
				cn.deleteRequest(r)
				// log.Printf("%p: cancelling request: %v", cn, r)
				if !msg(makeCancelMessage(r)) {
					return
				}
			}
		}
	} else if len(cn.requests) <= cn.requestsLowWater {
		filledBuffer := false
		cn.iterPendingPieces(func(pieceIndex pieceIndex) bool {
			cn.iterPendingRequests(pieceIndex, func(r request) bool {
				if !cn.setInterested(true, msg) {
					filledBuffer = true
					return false
				}
				if len(cn.requests) >= cn.nominalMaxRequests() {
					return false
				}
				// Choking is looked at here because our interest is dependent
				// on whether we'd make requests in its absence.
				if cn.peerChoking {
					if !cn.peerAllowedFast.Get(bitmap.BitIndex(r.Index)) {
						return false
					}
				}
				if _, ok := cn.requests[r]; ok {
					return true
				}
				filledBuffer = !cn.request(r, msg)
				return !filledBuffer
			})
			return !filledBuffer
		})
		if filledBuffer {
			// If we didn't completely top up the requests, we shouldn't mark
			// the low water, since we'll want to top up the requests as soon
			// as we have more write buffer space.
			return
		}
		cn.requestsLowWater = len(cn.requests) / 2
	}

	cn.upload(msg)
}

// Routine that writes to the peer. Some of what to write is buffered by
// activity elsewhere in the Client, and some is determined locally when the
// connection is writable.
func (cn *PeerConn) writer(keepAliveTimeout time.Duration) {
	var (
		lastWrite      time.Time = time.Now()
		keepAliveTimer *time.Timer
	)
	keepAliveTimer = time.AfterFunc(keepAliveTimeout, func() {
		cn.locker().Lock()
		defer cn.locker().Unlock()
		if time.Since(lastWrite) >= keepAliveTimeout {
			cn.tickleWriter()
		}
		keepAliveTimer.Reset(keepAliveTimeout)
	})
	cn.locker().Lock()
	defer cn.locker().Unlock()
	defer cn.close()
	defer keepAliveTimer.Stop()
	frontBuf := new(bytes.Buffer)
	for {
		if cn.closed.IsSet() {
			return
		}
		if cn.writeBuffer.Len() == 0 {
			cn.fillWriteBuffer(func(msg pp.Message) bool {
				cn.wroteMsg(&msg)
				cn.writeBuffer.Write(msg.MustMarshalBinary())
				torrent.Add(fmt.Sprintf("messages filled of type %s", msg.Type.String()), 1)
				return cn.writeBuffer.Len() < 1<<16 // 64KiB
			})
		}
		if cn.writeBuffer.Len() == 0 && time.Since(lastWrite) >= keepAliveTimeout {
			cn.writeBuffer.Write(pp.Message{Keepalive: true}.MustMarshalBinary())
			postedKeepalives.Add(1)
		}
		if cn.writeBuffer.Len() == 0 {
			// TODO: Minimize wakeups....
			cn.writerCond.Wait()
			continue
		}
		// Flip the buffers.
		frontBuf, cn.writeBuffer = cn.writeBuffer, frontBuf
		cn.locker().Unlock()
		n, err := cn.w.Write(frontBuf.Bytes())
		cn.locker().Lock()
		if n != 0 {
			lastWrite = time.Now()
			keepAliveTimer.Reset(keepAliveTimeout)
		}
		if err != nil {
			return
		}
		if n != frontBuf.Len() {
			panic("short write")
		}
		frontBuf.Reset()
	}
}

func (cn *PeerConn) have(piece pieceIndex) {
	if cn.sentHaves.Get(bitmap.BitIndex(piece)) {
		return
	}
	cn.post(pp.Message{
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
	cn.post(pp.Message{
		Type:     pp.Bitfield,
		Bitfield: cn.t.bitfield(),
	})
	cn.sentHaves = cn.t._completedPieces.Copy()
}

func (cn *PeerConn) updateRequests() {
	// log.Print("update requests")
	cn.tickleWriter()
}

// Emits the indices in the Bitmaps bms in order, never repeating any index.
// skip is mutated during execution, and its initial values will never be
// emitted.
func iterBitmapsDistinct(skip *bitmap.Bitmap, bms ...bitmap.Bitmap) iter.Func {
	return func(cb iter.Callback) {
		for _, bm := range bms {
			if !iter.All(
				func(i interface{}) bool {
					skip.Add(i.(int))
					return cb(i)
				},
				bitmap.Sub(bm, *skip).Iter,
			) {
				return
			}
		}
	}
}

func iterUnbiasedPieceRequestOrder(cn requestStrategyConnection, f func(piece pieceIndex) bool) bool {
	now, readahead := cn.torrent().readerPiecePriorities()
	skip := bitmap.Flip(cn.peerPieces(), 0, cn.torrent().numPieces())
	skip.Union(cn.torrent().ignorePieces())
	// Return an iterator over the different priority classes, minus the skip pieces.
	return iter.All(
		func(_piece interface{}) bool {
			return f(pieceIndex(_piece.(bitmap.BitIndex)))
		},
		iterBitmapsDistinct(&skip, now, readahead),
		// We have to iterate _pendingPieces separately because it isn't a Bitmap.
		func(cb iter.Callback) {
			cn.torrent().pendingPieces().IterTyped(func(piece int) bool {
				if skip.Contains(piece) {
					return true
				}
				more := cb(piece)
				skip.Add(piece)
				return more
			})
		},
	)
}

// The connection should download highest priority pieces first, without any inclination toward
// avoiding wastage. Generally we might do this if there's a single connection, or this is the
// fastest connection, and we have active readers that signal an ordering preference. It's
// conceivable that the best connection should do this, since it's least likely to waste our time if
// assigned to the highest priority pieces, and assigning more than one this role would cause
// significant wasted bandwidth.
func (cn *PeerConn) shouldRequestWithoutBias() bool {
	return cn.t.requestStrategy.shouldRequestWithoutBias(cn.requestStrategyConnection())
}

func (cn *PeerConn) iterPendingPieces(f func(pieceIndex) bool) bool {
	if !cn.t.haveInfo() {
		return false
	}
	return cn.t.requestStrategy.iterPendingPieces(cn, f)
}
func (cn *PeerConn) iterPendingPiecesUntyped(f iter.Callback) {
	cn.iterPendingPieces(func(i pieceIndex) bool { return f(i) })
}

func (cn *PeerConn) iterPendingRequests(piece pieceIndex, f func(request) bool) bool {
	return cn.t.requestStrategy.iterUndirtiedChunks(
		cn.t.piece(piece).requestStrategyPiece(),
		func(cs chunkSpec) bool {
			return f(request{pp.Integer(piece), cs})
		},
	)
}

// check callers updaterequests
func (cn *PeerConn) stopRequestingPiece(piece pieceIndex) bool {
	return cn._pieceRequestOrder.Remove(bitmap.BitIndex(piece))
}

// This is distinct from Torrent piece priority, which is the user's
// preference. Connection piece priority is specific to a connection and is
// used to pseudorandomly avoid connections always requesting the same pieces
// and thus wasting effort.
func (cn *PeerConn) updatePiecePriority(piece pieceIndex) bool {
	tpp := cn.t.piecePriority(piece)
	if !cn.peerHasPiece(piece) {
		tpp = PiecePriorityNone
	}
	if tpp == PiecePriorityNone {
		return cn.stopRequestingPiece(piece)
	}
	prio := cn.getPieceInclination()[piece]
	prio = cn.t.requestStrategy.piecePriority(cn, piece, tpp, prio)
	return cn._pieceRequestOrder.Set(bitmap.BitIndex(piece), prio) || cn.shouldRequestWithoutBias()
}

func (cn *PeerConn) getPieceInclination() []int {
	if cn.pieceInclination == nil {
		cn.pieceInclination = cn.t.getConnPieceInclination()
	}
	return cn.pieceInclination
}

func (cn *PeerConn) discardPieceInclination() {
	if cn.pieceInclination == nil {
		return
	}
	cn.t.putPieceInclination(cn.pieceInclination)
	cn.pieceInclination = nil
}

func (cn *PeerConn) peerPiecesChanged() {
	if cn.t.haveInfo() {
		prioritiesChanged := false
		for i := pieceIndex(0); i < cn.t.numPieces(); i++ {
			if cn.updatePiecePriority(i) {
				prioritiesChanged = true
			}
		}
		if prioritiesChanged {
			cn.updateRequests()
		}
	}
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
	cn._peerPieces.Set(bitmap.BitIndex(piece), true)
	if cn.updatePiecePriority(piece) {
		cn.updateRequests()
	}
	return nil
}

func (cn *PeerConn) peerSentBitfield(bf []bool) error {
	cn.peerSentHaveAll = false
	if len(bf)%8 != 0 {
		panic("expected bitfield length divisible by 8")
	}
	// We know that the last byte means that at most the last 7 bits are
	// wasted.
	cn.raisePeerMinPieces(pieceIndex(len(bf) - 7))
	if cn.t.haveInfo() && len(bf) > int(cn.t.numPieces()) {
		// Ignore known excess pieces.
		bf = bf[:cn.t.numPieces()]
	}
	for i, have := range bf {
		if have {
			cn.raisePeerMinPieces(pieceIndex(i) + 1)
		}
		cn._peerPieces.Set(i, have)
	}
	cn.peerPiecesChanged()
	return nil
}

func (cn *PeerConn) onPeerSentHaveAll() error {
	cn.peerSentHaveAll = true
	cn._peerPieces.Clear()
	cn.peerPiecesChanged()
	return nil
}

func (cn *PeerConn) peerSentHaveNone() error {
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
	cn.allStats(func(cs *ConnStats) { cs.wroteMsg(msg) })
}

func (cn *PeerConn) readMsg(msg *pp.Message) {
	cn.allStats(func(cs *ConnStats) { cs.readMsg(msg) })
}

// After handshake, we know what Torrent and Client stats to include for a
// connection.
func (cn *PeerConn) postHandshakeStats(f func(*ConnStats)) {
	t := cn.t
	f(&t.stats)
	f(&t.cl.stats)
}

// All ConnStats that include this connection. Some objects are not known
// until the handshake is complete, after which it's expected to reconcile the
// differences.
func (cn *PeerConn) allStats(f func(*ConnStats)) {
	f(&cn._stats)
	if cn.reconciledHandshakeStats {
		cn.postHandshakeStats(f)
	}
}

func (cn *PeerConn) wroteBytes(n int64) {
	cn.allStats(add(n, func(cs *ConnStats) *Count { return &cs.BytesWritten }))
}

func (cn *PeerConn) readBytes(n int64) {
	cn.allStats(add(n, func(cs *ConnStats) *Count { return &cs.BytesRead }))
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

func (c *PeerConn) lastHelpful() (ret time.Time) {
	ret = c.lastUsefulChunkReceived
	if c.t.seeding() && c.lastChunkSent.After(ret) {
		ret = c.lastChunkSent
	}
	return
}

func (c *PeerConn) fastEnabled() bool {
	return c.PeerExtensionBytes.SupportsFast() && c.t.cl.extensionBytes.SupportsFast()
}

func (c *PeerConn) reject(r request) {
	if !c.fastEnabled() {
		panic("fast not enabled")
	}
	c.post(r.ToMsg(pp.Reject))
	delete(c.peerRequests, r)
}

func (c *PeerConn) onReadRequest(r request) error {
	requestedChunkLengths.Add(strconv.FormatUint(r.Length.Uint64(), 10), 1)
	if _, ok := c.peerRequests[r]; ok {
		torrent.Add("duplicate requests received", 1)
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
	if len(c.peerRequests) >= maxRequests {
		torrent.Add("requests received while queue full", 1)
		if c.fastEnabled() {
			c.reject(r)
		}
		// BEP 6 says we may close here if we choose.
		return nil
	}
	if !c.t.havePiece(pieceIndex(r.Index)) {
		// This isn't necessarily them screwing up. We can drop pieces
		// from our storage, and can't communicate this to peers
		// except by reconnecting.
		requestsReceivedForMissingPieces.Add(1)
		return fmt.Errorf("peer requested piece we don't have: %v", r.Index.Int())
	}
	// Check this after we know we have the piece, so that the piece length will be known.
	if r.Begin+r.Length > c.t.pieceLength(pieceIndex(r.Index)) {
		torrent.Add("bad requests received", 1)
		return errors.New("bad request")
	}
	if c.peerRequests == nil {
		c.peerRequests = make(map[request]struct{}, maxRequests)
	}
	c.peerRequests[r] = struct{}{}
	c.tickleWriter()
	return nil
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
		MaxLength: 256 * 1024,
		Pool:      t.chunkPool,
	}
	for {
		var msg pp.Message
		func() {
			cl.unlock()
			defer cl.lock()
			err = decoder.Decode(&msg)
		}()
		if t.closed.IsSet() || c.closed.IsSet() || err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		c.readMsg(&msg)
		c.lastMessageReceived = time.Now()
		if msg.Keepalive {
			receivedKeepalives.Add(1)
			continue
		}
		messageTypesReceived.Add(msg.Type.String(), 1)
		if msg.Type.FastExtension() && !c.fastEnabled() {
			return fmt.Errorf("received fast extension message (type=%v) but extension is disabled", msg.Type)
		}
		switch msg.Type {
		case pp.Choke:
			c.peerChoking = true
			c.deleteAllRequests()
			// We can then reset our interest.
			c.updateRequests()
			c.updateExpectingChunks()
		case pp.Unchoke:
			c.peerChoking = false
			c.tickleWriter()
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
			err = c.onReadRequest(r)
		case pp.Piece:
			err = c.receiveChunk(&msg)
			if len(msg.Piece) == int(t.chunkSize) {
				t.chunkPool.Put(&msg.Piece)
			}
			if err != nil {
				err = fmt.Errorf("receiving chunk: %s", err)
			}
		case pp.Cancel:
			req := newRequestFromMessage(&msg)
			c.onPeerSentCancel(req)
		case pp.Port:
			ipa, ok := tryIpPortFromNetAddr(c.remoteAddr)
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
			log.Fmsg("peer suggested piece %d", msg.Index).AddValues(c, msg.Index, debugLogValue).Log(c.t.logger)
			c.updateRequests()
		case pp.HaveAll:
			err = c.onPeerSentHaveAll()
		case pp.HaveNone:
			err = c.peerSentHaveNone()
		case pp.Reject:
			c.deleteRequest(newRequestFromMessage(&msg))
			delete(c.validReceiveChunks, newRequestFromMessage(&msg))
		case pp.AllowedFast:
			torrent.Add("allowed fasts received", 1)
			log.Fmsg("peer allowed fast: %d", msg.Index).AddValues(c, debugLogValue).Log(c.t.logger)
			c.peerAllowedFast.Add(int(msg.Index))
			c.updateRequests()
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
			c.t.logger.Printf("error parsing extended handshake message %q: %s", payload, err)
			return errors.Wrap(err, "unmarshalling extended handshake payload")
		}
		if d.Reqq != 0 {
			c.PeerMaxRequests = d.Reqq
		}
		c.PeerClientName = d.V
		if c.PeerExtensionIDs == nil {
			c.PeerExtensionIDs = make(map[pp.ExtensionName]pp.ExtensionNumber, len(d.M))
		}
		for name, id := range d.M {
			if _, ok := c.PeerExtensionIDs[name]; !ok {
				torrent.Add(fmt.Sprintf("peers supporting extension %q", name), 1)
			}
			c.PeerExtensionIDs[name] = id
		}
		if d.MetadataSize != 0 {
			if err = t.setMetadataSize(d.MetadataSize); err != nil {
				return errors.Wrapf(err, "setting metadata size to %d", d.MetadataSize)
			}
		}
		c.requestPendingMetadata()
		return nil
	case metadataExtendedId:
		err := cl.gotMetadataExtensionMsg(payload, t, c)
		if err != nil {
			return fmt.Errorf("handling metadata extension message: %w", err)
		}
		return nil
	case pexExtendedId:
		if cl.config.DisablePEX {
			// TODO: Maybe close the connection. Check that we're not
			// advertising that we support PEX if it's disabled.
			return nil
		}
		var pexMsg pp.PexMsg
		err := bencode.Unmarshal(payload, &pexMsg)
		if err != nil {
			return fmt.Errorf("error unmarshalling PEX message: %s", err)
		}
		torrent.Add("pex added6 peers received", int64(len(pexMsg.Added6)))
		var peers Peers
		peers.AppendFromPex(pexMsg.Added6, pexMsg.Added6Flags)
		peers.AppendFromPex(pexMsg.Added, pexMsg.AddedFlags)
		t.addPeers(peers)
		return nil
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

// Handle a received chunk from a peer.
func (c *PeerConn) receiveChunk(msg *pp.Message) error {
	t := c.t
	cl := t.cl
	torrent.Add("chunks received", 1)

	req := newRequestFromMessage(msg)

	if c.peerChoking {
		torrent.Add("chunks received while choking", 1)
	}

	if _, ok := c.validReceiveChunks[req]; !ok {
		torrent.Add("chunks received unexpected", 1)
		//return errors.New("received unexpected chunk")
	}
	delete(c.validReceiveChunks, req)

	if c.peerChoking && c.peerAllowedFast.Get(int(req.Index)) {
		torrent.Add("chunks received due to allowed fast", 1)
	}

	// Request has been satisfied.
	if c.deleteRequest(req) {
		if c.expectingChunks() {
			c._chunksReceivedWhileExpecting++
		}
	} else {
		torrent.Add("chunks received unwanted", 1)
	}

	// Do we actually want this chunk?
	if t.haveChunk(req) {
		torrent.Add("chunks received wasted", 1)
		c.allStats(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadWasted }))
		return nil
	}

	piece := &t.pieces[req.Index]

	c.allStats(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadUseful }))
	c.allStats(add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulData }))
	c.lastUsefulChunkReceived = time.Now()
	// if t.fastestConn != c {
	// log.Printf("setting fastest connection %p", c)
	// }
	t.fastestConn = c

	// Need to record that it hasn't been written yet, before we attempt to do
	// anything with it.
	piece.incrementPendingWrites()
	// Record that we have the chunk, so we aren't trying to download it while
	// waiting for it to be written to storage.
	piece.unpendChunkIndex(chunkIndex(req.chunkSpec, t.chunkSize))

	// Cancel pending requests for this chunk.
	for c := range t.conns {
		c.postCancel(req)
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
		c.logger.Printf("error writing received chunk %v: %v", req, err)
		t.pendRequest(req)
		//t.updatePieceCompletion(pieceIndex(msg.Index))
		t.onWriteChunkErr(err)
		return nil
	}

	c.onDirtiedPiece(pieceIndex(req.Index))

	if t.pieceAllDirty(pieceIndex(req.Index)) {
		t.queuePieceCheck(pieceIndex(req.Index))
		// We don't pend all chunks here anymore because we don't want code dependent on the dirty
		// chunk status (such as the haveChunk call above) to have to check all the various other
		// piece states like queued for hash, hashing etc. This does mean that we need to be sure
		// that chunk pieces are pended at an appropriate time later however.
	}

	cl.event.Broadcast()
	// We do this because we've written a chunk, and may change PieceState.Partial.
	t.publishPieceChange(pieceIndex(req.Index))

	return nil
}

func (c *PeerConn) onDirtiedPiece(piece pieceIndex) {
	if c.peerTouchedPieces == nil {
		c.peerTouchedPieces = make(map[pieceIndex]struct{})
	}
	c.peerTouchedPieces[piece] = struct{}{}
	ds := &c.t.pieces[piece].dirtiers
	if *ds == nil {
		*ds = make(map[*PeerConn]struct{})
	}
	(*ds)[c] = struct{}{}
}

func (c *PeerConn) uploadAllowed() bool {
	if c.t.cl.config.NoUpload {
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
		c.uploadTimer = time.AfterFunc(delay, c.writerCond.Broadcast)
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
		for r := range c.peerRequests {
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
			more, err := c.sendChunk(r, msg)
			if err != nil {
				i := pieceIndex(r.Index)
				if c.t.pieceComplete(i) {
					c.t.updatePieceCompletion(i)
					if !c.t.pieceComplete(i) {
						// We had the piece, but not anymore.
						break another
					}
				}
				log.Str("error sending chunk to peer").AddValues(c, r, err).Log(c.t.logger)
				// If we failed to send a chunk, choke the peer to ensure they
				// flush all their requests. We've probably dropped a piece,
				// but there's no way to communicate this to the peer. If they
				// ask for it again, we'll kick them to allow us to send them
				// an updated bitfield.
				break another
			}
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

func (cn *PeerConn) netGoodPiecesDirtied() int64 {
	return cn._stats.PiecesDirtiedGood.Int64() - cn._stats.PiecesDirtiedBad.Int64()
}

func (c *PeerConn) peerHasWantedPieces() bool {
	return !c._pieceRequestOrder.IsEmpty()
}

func (c *PeerConn) numLocalRequests() int {
	return len(c.requests)
}

func (c *PeerConn) deleteRequest(r request) bool {
	if _, ok := c.requests[r]; !ok {
		return false
	}
	delete(c.requests, r)
	c.updateExpectingChunks()
	c.t.requestStrategy.hooks().deletedRequest(r)
	pr := c.t.pendingRequests
	pr[r]--
	n := pr[r]
	if n == 0 {
		delete(pr, r)
	}
	if n < 0 {
		panic(n)
	}
	c.updateRequests()
	for _c := range c.t.conns {
		if !_c.interested && _c != c && c.peerHasPiece(pieceIndex(r.Index)) {
			_c.updateRequests()
		}
	}
	return true
}

func (c *PeerConn) deleteAllRequests() {
	for r := range c.requests {
		c.deleteRequest(r)
	}
	if len(c.requests) != 0 {
		panic(len(c.requests))
	}
	// for c := range c.t.conns {
	// 	c.tickleWriter()
	// }
}

func (c *PeerConn) tickleWriter() {
	c.writerCond.Broadcast()
}

func (c *PeerConn) postCancel(r request) bool {
	if !c.deleteRequest(r) {
		return false
	}
	c.post(makeCancelMessage(r))
	return true
}

func (c *PeerConn) sendChunk(r request, msg func(pp.Message) bool) (more bool, err error) {
	// Count the chunk being sent, even if it isn't.
	b := make([]byte, r.Length)
	p := c.t.info.Piece(int(r.Index))
	n, err := c.t.readAt(b, p.Offset()+int64(r.Begin))
	if n != len(b) {
		if err == nil {
			panic("expected error")
		}
		return
	} else if err == io.EOF {
		err = nil
	}
	more = msg(pp.Message{
		Type:  pp.Piece,
		Index: r.Index,
		Begin: r.Begin,
		Piece: b,
	})
	c.lastChunkSent = time.Now()
	return
}

func (c *PeerConn) setTorrent(t *Torrent) {
	if c.t != nil {
		panic("connection already associated with a torrent")
	}
	c.t = t
	c.logger.Printf("torrent=%v", t)
	t.reconcileHandshakeStats(c)
}

func (c *PeerConn) peerPriority() peerPriority {
	return bep40PriorityIgnoreError(c.remoteIpPort(), c.t.cl.publicAddr(c.remoteIp()))
}

func (c *PeerConn) remoteIp() net.IP {
	return addrIpOrNil(c.remoteAddr)
}

func (c *PeerConn) remoteIpPort() IpPort {
	ipa, _ := tryIpPortFromNetAddr(c.remoteAddr)
	return IpPort{ipa.IP, uint16(ipa.Port)}
}

func (c *PeerConn) String() string {
	return fmt.Sprintf("connection %p", c)
}

func (c *PeerConn) trust() connectionTrust {
	return connectionTrust{c.trusted, c.netGoodPiecesDirtied()}
}

type connectionTrust struct {
	Implicit            bool
	NetGoodPiecesDirted int64
}

func (l connectionTrust) Less(r connectionTrust) bool {
	return multiless.New().Bool(l.Implicit, r.Implicit).Int64(l.NetGoodPiecesDirted, r.NetGoodPiecesDirted).Less()
}

func (cn *PeerConn) requestStrategyConnection() requestStrategyConnection {
	return cn
}

func (cn *PeerConn) chunksReceivedWhileExpecting() int64 {
	return cn._chunksReceivedWhileExpecting
}

func (cn *PeerConn) fastest() bool {
	return cn == cn.t.fastestConn
}

func (cn *PeerConn) peerMaxRequests() int {
	return cn.PeerMaxRequests
}

// Returns the pieces the peer has claimed to have.
func (cn *PeerConn) PeerPieces() bitmap.Bitmap {
	cn.locker().RLock()
	defer cn.locker().RUnlock()
	return cn.peerPieces()
}

func (cn *PeerConn) peerPieces() bitmap.Bitmap {
	ret := cn._peerPieces.Copy()
	if cn.peerSentHaveAll {
		ret.AddRange(0, cn.t.numPieces())
	}
	return ret
}

func (cn *PeerConn) pieceRequestOrder() *prioritybitmap.PriorityBitmap {
	return &cn._pieceRequestOrder
}

func (cn *PeerConn) stats() *ConnStats {
	return &cn._stats
}

func (cn *PeerConn) torrent() requestStrategyTorrent {
	return cn.t.requestStrategyTorrent()
}
