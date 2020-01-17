package torrent

import (
	"bufio"
	"bytes"
	stderrors "errors"
	"fmt"
	"io"
	l2 "log"
	"math"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/missinggo/bitmap"
	"github.com/anacrolix/missinggo/iter"
	"github.com/anacrolix/missinggo/prioritybitmap"
	"github.com/anacrolix/multiless"
	"github.com/pkg/errors"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/mse"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

var _ = l2.Print

type peerSource string

const (
	peerSourceTracker         = "Tr"
	peerSourceIncoming        = "I"
	peerSourceDhtGetPeers     = "Hg" // Peers we found by searching a DHT.
	peerSourceDhtAnnouncePeer = "Ha" // Peers that were announced to us by a DHT.
	peerSourcePex             = "X"
)

// Maintains the state of a connection with a peer.
type connection struct {
	// First to ensure 64-bit alignment for atomics. See #262.
	stats ConnStats

	t *torrent
	// The actual Conn, used for closing, and setting socket options.
	conn       net.Conn
	outgoing   bool
	network    string
	remoteAddr IpPort
	// The Reader and Writer for this Conn, with hooks installed for stats,
	// limiting, deadlines etc.
	w io.Writer
	r io.Reader
	// True if the connection is operating over MSE obfuscation.
	headerEncrypted bool
	cryptoMethod    mse.CryptoMethod
	Discovery       peerSource
	trusted         bool
	closed          missinggo.Event
	// Set true after we've added our ConnStats generated during handshake to
	// other ConnStat instances as determined when the *torrent became known.
	reconciledHandshakeStats bool

	lastMessageReceived     time.Time
	completedHandshake      time.Time
	lastUsefulChunkReceived time.Time
	lastChunkSent           time.Time

	// Stuff controlled by the local peer.
	Interested           bool
	lastBecameInterested time.Time
	priorInterest        time.Duration

	lastStartedExpectingToReceiveChunks time.Time
	cumulativeExpectedToReceiveChunks   time.Duration
	chunksReceivedWhileExpecting        int64

	Choked           bool
	requests         map[uint64]request
	requestsLowWater int

	// Indexed by metadata piece, set to true if posted and pending a
	// response.
	metadataRequests []bool
	sentHaves        bitmap.Bitmap

	// Stuff controlled by the remote peer.
	PeerID             PeerID
	PeerInterested     bool
	PeerChoked         bool
	PeerRequests       map[request]struct{}
	PeerExtensionBytes pp.PeerExtensionBits
	// The pieces the peer has claimed to have.
	peerPieces bitmap.Bitmap
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

	pieceInclination  []int
	pieceRequestOrder prioritybitmap.PriorityBitmap

	writeBuffer *bytes.Buffer
	uploadTimer *time.Timer
	writerCond  sync.Cond

	logger log.Logger
}

func (cn *connection) updateExpectingChunks() {
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

func (cn *connection) expectingChunks() bool {
	return cn.Interested && !cn.PeerChoked
}

// Returns true if the connection is over IPv6.
func (cn *connection) ipv6() bool {
	ip := cn.remoteAddr.IP
	if ip.To4() != nil {
		return false
	}
	return len(ip) == net.IPv6len
}

// Returns true the dialer has the lower client peer ID. TODO: Find the
// specification for this.
func (cn *connection) isPreferredDirection() bool {
	return bytes.Compare(cn.t.cln.peerID[:], cn.PeerID[:]) < 0 == cn.outgoing
}

// Returns whether the left connection should be preferred over the right one,
// considering only their networking properties. If ok is false, we can't
// decide.
func (cn *connection) hasPreferredNetworkOver(r *connection) (left, ok bool) {
	var ml multiLess
	ml.NextBool(cn.isPreferredDirection(), r.isPreferredDirection())
	ml.NextBool(!cn.utp(), !r.utp())
	ml.NextBool(cn.ipv6(), r.ipv6())
	return ml.FinalOk()
}

func (cn *connection) cumInterest() time.Duration {
	ret := cn.priorInterest
	if cn.Interested {
		ret += time.Since(cn.lastBecameInterested)
	}
	return ret
}

func (cn *connection) peerHasAllPieces() (all bool, known bool) {
	if cn.peerSentHaveAll {
		return true, true
	}
	if !cn.t.haveInfo() {
		return false, false
	}
	return bitmap.Flip(cn.peerPieces, 0, bitmap.BitIndex(cn.t.numPieces())).IsEmpty(), true
}

func (cn *connection) mu() sync.Locker {
	return cn.t.locker()
}

func (cn *connection) localAddr() net.Addr {
	return cn.conn.LocalAddr()
}

func (cn *connection) supportsExtension(ext pp.ExtensionName) bool {
	_, ok := cn.PeerExtensionIDs[ext]
	return ok
}

// The best guess at number of pieces in the torrent for this peer.
func (cn *connection) bestPeerNumPieces() pieceIndex {
	if cn.t.haveInfo() {
		return cn.t.numPieces()
	}
	return cn.peerMinPieces
}

func (cn *connection) completedString() string {
	have := pieceIndex(cn.peerPieces.Len())
	if cn.peerSentHaveAll {
		have = cn.bestPeerNumPieces()
	}
	return fmt.Sprintf("%d/%d", have, cn.bestPeerNumPieces())
}

// Correct the PeerPieces slice length. Return false if the existing slice is
// invalid, such as by receiving badly sized BITFIELD, or invalid HAVE
// messages.
func (cn *connection) setNumPieces(num pieceIndex) error {
	cn.peerPieces.RemoveRange(bitmap.BitIndex(num), bitmap.ToEnd)
	cn.peerPiecesChanged()
	return nil
}

func eventAgeString(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return fmt.Sprintf("%.2fs ago", time.Since(t).Seconds())
}

func (cn *connection) connectionFlags() (ret string) {
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

func (cn *connection) utp() bool {
	return parseNetworkString(cn.network).Udp
}

// Inspired by https://github.com/transmission/transmission/wiki/Peer-Status-Text.
func (cn *connection) statusFlags() (ret string) {
	c := func(b byte) {
		ret += string([]byte{b})
	}
	if cn.Interested {
		c('i')
	}
	if cn.Choked {
		c('c')
	}
	c('-')
	ret += cn.connectionFlags()
	c('-')
	if cn.PeerInterested {
		c('i')
	}
	if cn.PeerChoked {
		c('c')
	}
	return
}

// func (cn *connection) String() string {
// 	var buf bytes.Buffer
// 	cn.WriteStatus(&buf, nil)
// 	return buf.String()
// }

func (cn *connection) downloadRate() float64 {
	return float64(cn.stats.BytesReadUsefulData.Int64()) / cn.cumInterest().Seconds()
}

func (cn *connection) WriteStatus(w io.Writer, t *torrent) {
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
		&cn.stats.ChunksReadUseful,
		&cn.stats.ChunksRead,
		&cn.stats.ChunksWritten,
		cn.requestsLowWater,
		cn.numLocalRequests(),
		cn.nominalMaxRequests(),
		len(cn.PeerRequests),
		cn.statusFlags(),
		cn.downloadRate()/(1<<10),
	)
	fmt.Fprintf(w, "    next pieces: %v%s\n",
		iter.ToSlice(iter.Head(10, cn.iterPendingPiecesUntyped)),
		func() string {
			if cn.shouldRequestWithoutBias() {
				return " (fastest)"
			} else {
				return ""
			}
		}())
}

func (cn *connection) Close() {
	if !cn.closed.Set() {
		return
	}

	cn.tickleWriter()
	cn.discardPieceInclination()
	cn.pieceRequestOrder.Clear()

	if cn.conn != nil {
		go cn.conn.Close()
	}
}

func (cn *connection) PeerHasPiece(piece pieceIndex) bool {
	return cn.peerSentHaveAll || cn.peerPieces.Contains(bitmap.BitIndex(piece))
}

// Writes a message into the write buffer.
func (cn *connection) Post(msg pp.Message) {
	metrics.Add(fmt.Sprintf("messages posted of type %s", msg.Type.String()), 1)
	// We don't need to track bytes here because a connection.w Writer wrapper
	// takes care of that (although there's some delay between us recording
	// the message, and the connection writer flushing it out.).
	cn.writeBuffer.Write(msg.MustMarshalBinary())
	// Last I checked only Piece messages affect stats, and we don't post
	// those.
	cn.wroteMsg(&msg)
	cn.tickleWriter()
}

func (cn *connection) requestMetadataPiece(index int) {
	eID := cn.PeerExtensionIDs[pp.ExtensionNameMetadata]
	if eID == 0 {
		return
	}
	if index < len(cn.metadataRequests) && cn.metadataRequests[index] {
		return
	}
	cn.logger.Printf("requesting metadata piece %d", index)
	cn.Post(pp.Message{
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

func (cn *connection) requestedMetadataPiece(index int) bool {
	return index < len(cn.metadataRequests) && cn.metadataRequests[index]
}

// The actual value to use as the maximum outbound requests.
func (cn *connection) nominalMaxRequests() (ret int) {
	if cn.t.requestStrategy == 3 {
		expectingTime := int64(cn.totalExpectingTime())
		if expectingTime == 0 {
			expectingTime = math.MaxInt64
		} else {
			expectingTime *= 2
		}
		return int(clamp(
			1,
			int64(cn.PeerMaxRequests),
			max(
				// It makes sense to always pipeline at least one connection,
				// since latency must be non-zero.
				2,
				// Request only as many as we expect to receive in the
				// dupliateRequestTimeout window. We are trying to avoid having to
				// duplicate requests.
				cn.chunksReceivedWhileExpecting*int64(cn.t.duplicateRequestTimeout)/expectingTime,
			),
		))
	}
	return int(clamp(
		1,
		int64(cn.PeerMaxRequests),
		max(64,
			cn.stats.ChunksReadUseful.Int64()-(cn.stats.ChunksRead.Int64()-cn.stats.ChunksReadUseful.Int64()))))
}

func (cn *connection) totalExpectingTime() (ret time.Duration) {
	ret = cn.cumulativeExpectedToReceiveChunks
	if !cn.lastStartedExpectingToReceiveChunks.IsZero() {
		ret += time.Since(cn.lastStartedExpectingToReceiveChunks)
	}
	return

}

func (cn *connection) onPeerSentCancel(r request) {
	if _, ok := cn.PeerRequests[r]; !ok {
		metrics.Add("unexpected cancels received", 1)
		return
	}
	if cn.fastEnabled() {
		cn.reject(r)
	} else {
		delete(cn.PeerRequests, r)
	}
}

func (cn *connection) Choke(msg messageWriter) (more bool) {
	if cn.Choked {
		return true
	}
	cn.Choked = true
	more = msg(pp.Message{
		Type: pp.Choke,
	})
	if cn.fastEnabled() {
		for r := range cn.PeerRequests {
			// TODO: Don't reject pieces in allowed fast set.
			cn.reject(r)
		}
	} else {
		cn.PeerRequests = nil
	}
	return
}

func (cn *connection) Unchoke(msg func(pp.Message) bool) bool {
	if !cn.Choked {
		return true
	}
	cn.Choked = false
	return msg(pp.Message{
		Type: pp.Unchoke,
	})
}

func (cn *connection) SetInterested(interested bool, msg func(pp.Message) bool) bool {
	if cn.Interested == interested {
		return true
	}
	cn.Interested = interested
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
func (cn *connection) request(r request, mw messageWriter) bool {
	if _, ok := cn.t.conns[cn]; !ok {
		panic("requesting but not in active conns")
	}

	if cn.requests == nil {
		cn.requests = make(map[uint64]request)
	}
	cn.requests[r.digest()] = r
	cn.updateExpectingChunks()

	return mw(pp.Message{
		Type:   pp.Request,
		Index:  r.Index,
		Begin:  r.Begin,
		Length: r.Length,
	})
}

func (cn *connection) fillWriteBuffer(msg func(pp.Message) bool) {
	if !cn.t.networkingEnabled {
		if !cn.SetInterested(false, msg) {
			return
		}

		if len(cn.requests) != 0 {
			for _, r := range cn.requests {
				cn.deleteRequest(r)
				// log.Printf("%p: cancelling request: %v", cn, r)
				if !msg(makeCancelMessage(r)) {
					return
				}
			}
		}
	}

	// optimisticly unchoke the connection when locally we have all the pieces
	// we don't actually care about the result.
	if cn.uploadAllowed() && cn.t.piecesM.Missing() == 0 && cn.Unchoke(msg) {
	}

	// check if we are actually missing pieces before executing this code.
	if len(cn.requests) <= cn.requestsLowWater && cn.t.piecesM.Missing() > 0 {
		filledBuffer := false
		done := false

		available := bmap(&cn.peerPieces)
		if cn.peerSentHaveAll {
			available = everybmap{}
		}

		// load until we have the maximum number of requests or until the buffer is full.
		for i := cn.nominalMaxRequests() - len(cn.requests); i > 0 && !filledBuffer && !done; {
			var (
				err error
				req request
			)

			// Choking is looked at here because our interest is dependent
			// on whether we'd make requests in its absence.
			if cn.PeerChoked {
				if !cn.peerAllowedFast.Get(bitmap.BitIndex(req.Index)) {
					break
				}

				metrics.Add("allowed fast requests sent", 1)
			}

			if req, err = cn.t.piecesM.Pop(available); stderrors.As(err, &empty{}) {
				break
			} else if err != nil {
				cn.t.logger.Printf("failed to request piece: %T - %v", err, err)
				filledBuffer = true
				continue
			}

			// l2.Printf("t(%p) - popped r(%d,%d,%d) - %v\n", cn.t, req.Index, req.Begin, req.Length, err)

			if !cn.SetInterested(true, msg) {
				filledBuffer = true
				continue
			}

			// we want the peer to upload to us.
			if !cn.Unchoke(msg) {
				filledBuffer = true
				continue
			}

			filledBuffer = !cn.request(req, msg)
			i--
		}

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
func (cn *connection) writer(keepAliveTimeout time.Duration) {
	var (
		lastWrite      time.Time = time.Now()
		keepAliveTimer *time.Timer
	)

	keepAliveTimer = time.AfterFunc(time.Second, func() {
		cn.mu().Lock()
		defer cn.mu().Unlock()
		if time.Since(lastWrite) >= keepAliveTimeout {
			cn.tickleWriter()
		}
		keepAliveTimer.Reset(keepAliveTimeout)
	})
	cn.mu().Lock()
	defer cn.mu().Unlock()
	defer cn.Close()
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
				metrics.Add(fmt.Sprintf("messages filled of type %s", msg.Type.String()), 1)
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
			if cn.closed.IsSet() {
				return
			}
			continue
		}
		// Flip the buffers.
		frontBuf, cn.writeBuffer = cn.writeBuffer, frontBuf
		cn.mu().Unlock()
		n, err := cn.w.Write(frontBuf.Bytes())
		cn.mu().Lock()
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

func (cn *connection) Have(piece pieceIndex) {
	if cn.sentHaves.Get(bitmap.BitIndex(piece)) {
		return
	}
	cn.Post(pp.Message{
		Type:  pp.Have,
		Index: pp.Integer(piece),
	})
	cn.sentHaves.Add(bitmap.BitIndex(piece))
}

func (cn *connection) PostBitfield() {
	if cn.sentHaves.Len() != 0 {
		panic("bitfield must be first have-related message sent")
	}
	if !cn.t.haveAnyPieces() {
		return
	}
	cn.Post(pp.Message{
		Type:     pp.Bitfield,
		Bitfield: cn.t.bitfield(),
	})
	cn.sentHaves = cn.t.completedPieces.Copy()
}

func (cn *connection) updateRequests() {
	// log.Print("update requests")
	cn.tickleWriter()
}

// Emits the indices in the Bitmaps bms in order, never repeating any index.
// skip is mutated during execution, and its initial values will never be
// emitted.
func iterBitmapsDistinct(skip *bitmap.Bitmap, bms ...bitmap.Bitmap) iter.Func {
	return func(cb iter.Callback) {
		for _, bm := range bms {
			if !iter.All(func(i interface{}) bool {
				skip.Add(i.(int))
				return cb(i)
			}, bitmap.Sub(bm, *skip).Iter) {
				return
			}
		}
	}
}

func (cn *connection) iterUnbiasedPieceRequestOrder(f func(piece pieceIndex) bool) bool {
	now, readahead := cn.t.readerPiecePriorities()
	var skip bitmap.Bitmap
	if !cn.peerSentHaveAll {
		// Pieces to skip include pieces the peer doesn't have.
		skip = bitmap.Flip(cn.peerPieces, 0, bitmap.BitIndex(cn.t.numPieces()))
	}
	// And pieces that we already have.
	skip.Union(cn.t.completedPieces)
	skip.Union(cn.t.piecesQueuedForHash)
	// Return an iterator over the different priority classes, minus the skip
	// pieces.
	return iter.All(
		func(_piece interface{}) bool {
			i := _piece.(bitmap.BitIndex)
			if cn.t.hashingPiece(pieceIndex(i)) {
				return true
			}
			return f(pieceIndex(i))
		},
		iterBitmapsDistinct(&skip, now, readahead),
		func(cb iter.Callback) {
			// TODO: this should be dead code.
			cn.t.piecesM.missing.IterTyped(func(chunk int) bool {
				piece := cn.t.piecesM.pindex(chunk)
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

// The connection should download highest priority pieces first, without any
// inclination toward avoiding wastage. Generally we might do this if there's
// a single connection, or this is the fastest connection, and we have active
// readers that signal an ordering preference. It's conceivable that the best
// connection should do this, since it's least likely to waste our time if
// assigned to the highest priority pieces, and assigning more than one this
// role would cause significant wasted bandwidth.
func (cn *connection) shouldRequestWithoutBias() bool {
	if cn.t.requestStrategy != 2 {
		return false
	}
	if len(cn.t.readers) == 0 {
		return false
	}
	if len(cn.t.conns) == 1 {
		return true
	}
	if cn == cn.t.fastestConn {
		return true
	}
	return false
}

func (cn *connection) iterPendingPieces(f func(pieceIndex) bool) bool {
	if !cn.t.haveInfo() {
		return false
	}
	if cn.t.requestStrategy == 3 {
		return cn.iterUnbiasedPieceRequestOrder(f)
	}
	if cn.shouldRequestWithoutBias() {
		return cn.iterUnbiasedPieceRequestOrder(f)
	} else {
		return cn.pieceRequestOrder.IterTyped(func(i int) bool {
			return f(pieceIndex(i))
		})
	}
}

func (cn *connection) iterPendingPiecesUntyped(f iter.Callback) {
	cn.iterPendingPieces(func(i pieceIndex) bool { return f(i) })
}

func (cn *connection) iterPendingRequests(piece pieceIndex, f func(request) bool) bool {
	return iterUndirtiedChunks(piece, cn.t, func(cs chunkSpec) bool {
		r := request{Index: pp.Integer(piece), chunkSpec: cs}
		if cn.t.requestStrategy == 3 {
			if _, ok := cn.t.lastRequested[r]; ok {
				// This piece has been requested on another connection, and
				// the duplicate request timer is still running.
				return true
			}
		}
		return f(r)
	})
}

func iterUndirtiedChunks(piece pieceIndex, t *torrent, f func(chunkSpec) bool) bool {
	p := &t.pieces[piece]
	if t.requestStrategy == 3 {
		for i := pp.Integer(0); i < p.numChunks(); i++ {
			if !p.dirtyChunks.Get(bitmap.BitIndex(i)) {
				if !f(t.chunkIndexSpec(i, piece)) {
					return false
				}
			}
		}
		return true
	}
	chunkIndices := t.pieces[piece].undirtiedChunkIndices()
	return iter.ForPerm(chunkIndices.Len(), func(i int) bool {
		ci, err := chunkIndices.RB.Select(uint32(i))
		if err != nil {
			panic(err)
		}
		return f(t.chunkIndexSpec(pp.Integer(ci), piece))
	})
}

// check callers updaterequests
func (cn *connection) stopRequestingPiece(piece pieceIndex) bool {
	return cn.pieceRequestOrder.Remove(bitmap.BitIndex(piece))
}

// This is distinct from Torrent piece priority, which is the user's
// preference. Connection piece priority is specific to a connection and is
// used to pseudorandomly avoid connections always requesting the same pieces
// and thus wasting effort.
func (cn *connection) updatePiecePriority(piece pieceIndex) bool {
	tpp := cn.t.piecePriority(piece)
	if !cn.PeerHasPiece(piece) {
		// l2.Println("peer missing piece", piece)
		tpp = PiecePriorityNone
	}
	if tpp == PiecePriorityNone {
		// l2.Println("disableRequestingPiece", piece)
		return cn.stopRequestingPiece(piece)
	}
	prio := cn.getPieceInclination()[piece]
	switch cn.t.requestStrategy {
	case 1:
		switch tpp {
		case PiecePriorityNormal:
		case PiecePriorityReadahead:
			prio -= int(cn.t.numPieces())
		case PiecePriorityNext, PiecePriorityNow:
			prio -= 2 * int(cn.t.numPieces())
		default:
			panic(tpp)
		}
		prio += int(piece / 3)
	default:
	}

	// for _, c := range cn.t.piecesM.Chunks(piece) {
	// 	cn.t.piecesM.Pend(c, c.Priority)
	// }

	return cn.pieceRequestOrder.Set(bitmap.BitIndex(piece), prio) || cn.shouldRequestWithoutBias()
}

func (cn *connection) getPieceInclination() []int {
	if cn.pieceInclination == nil {
		cn.pieceInclination = cn.t.getConnPieceInclination()
	}
	return cn.pieceInclination
}

func (cn *connection) discardPieceInclination() {
	if cn.pieceInclination == nil {
		return
	}
	cn.t.putPieceInclination(cn.pieceInclination)
	cn.pieceInclination = nil
}

func (cn *connection) peerPiecesChanged() {
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

func (cn *connection) raisePeerMinPieces(newMin pieceIndex) {
	if newMin > cn.peerMinPieces {
		cn.peerMinPieces = newMin
	}
}

func (cn *connection) peerSentHave(piece pieceIndex) error {
	if cn.t.haveInfo() && piece >= cn.t.numPieces() || piece < 0 {
		return errors.New("invalid piece")
	}

	if cn.PeerHasPiece(piece) {
		return nil
	}

	cn.raisePeerMinPieces(piece + 1)
	cn.peerPieces.Set(bitmap.BitIndex(piece), true)

	if cn.updatePiecePriority(piece) {
		cn.updateRequests()
	}

	return nil
}

func (cn *connection) peerSentBitfield(bf []bool) error {
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
		cn.peerPieces.Set(i, have)
	}
	cn.peerPiecesChanged()
	return nil
}

func (cn *connection) onPeerSentHaveAll() error {
	cn.peerSentHaveAll = true
	cn.peerPieces.Clear()
	cn.peerPiecesChanged()
	return nil
}

func (cn *connection) peerSentHaveNone() error {
	cn.peerPieces.Clear()
	cn.peerSentHaveAll = false
	cn.peerPiecesChanged()
	return nil
}

func (c *connection) requestPendingMetadata() {
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

func (cn *connection) wroteMsg(msg *pp.Message) {
	metrics.Add(fmt.Sprintf("messages written of type %s", msg.Type.String()), 1)
	cn.allStats(func(cs *ConnStats) { cs.wroteMsg(msg) })
}

func (cn *connection) readMsg(msg *pp.Message) {
	cn.allStats(func(cs *ConnStats) { cs.readMsg(msg) })
}

// After handshake, we know what Torrent and Client stats to include for a
// connection.
func (cn *connection) postHandshakeStats(f func(*ConnStats)) {
	t := cn.t
	f(&t.stats)
	f(&t.cln.stats)
}

// All ConnStats that include this connection. Some objects are not known
// until the handshake is complete, after which it's expected to reconcile the
// differences.
func (cn *connection) allStats(f func(*ConnStats)) {
	f(&cn.stats)
	if cn.reconciledHandshakeStats {
		cn.postHandshakeStats(f)
	}
}

func (cn *connection) wroteBytes(n int64) {
	cn.allStats(add(n, func(cs *ConnStats) *Count { return &cs.BytesWritten }))
}

func (cn *connection) readBytes(n int64) {
	cn.allStats(add(n, func(cs *ConnStats) *Count { return &cs.BytesRead }))
}

// Returns whether the connection could be useful to us. We're seeding and
// they want data, we don't have metainfo and they can provide it, etc.
func (c *connection) useful() bool {
	t := c.t
	if c.closed.IsSet() {
		return false
	}
	if !t.haveInfo() {
		return c.supportsExtension("ut_metadata")
	}
	if t.seeding() && c.PeerInterested {
		return true
	}
	if c.peerHasWantedPieces() {
		return true
	}
	return false
}

func (c *connection) lastHelpful() (ret time.Time) {
	ret = c.lastUsefulChunkReceived
	if c.t.seeding() && c.lastChunkSent.After(ret) {
		ret = c.lastChunkSent
	}
	return
}

func (cn *connection) fastEnabled() bool {
	return cn.PeerExtensionBytes.SupportsFast() && cn.t.cln.extensionBytes.SupportsFast()
}

func (cn *connection) reject(r request) {
	if !cn.fastEnabled() {
		panic("fast not enabled")
	}
	cn.Post(r.ToMsg(pp.Reject))
	delete(cn.PeerRequests, r)
}

func (c *connection) onReadRequest(r request) error {
	requestedChunkLengths.Add(strconv.FormatUint(r.Length.Uint64(), 10), 1)
	if _, ok := c.PeerRequests[r]; ok {
		metrics.Add("duplicate requests received", 1)
		return nil
	}

	if c.Choked {
		metrics.Add("requests received while choking", 1)
		if c.fastEnabled() {
			l2.Printf("%p - onReadRequest: choked, rejecting request", c)
			metrics.Add("requests rejected while choking", 1)
			c.reject(r)
		}
		return nil
	}

	if len(c.PeerRequests) >= maxRequests {
		metrics.Add("requests received while queue full", 1)
		if c.fastEnabled() {
			l2.Printf("%p - onReadRequest: PeerRequests >= maxRequests, rejecting request", c)
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
		l2.Output(2, fmt.Sprintf("t(%p) - onReadRequest: requested piece not available: r(%d,%d,%d)", c.t, r.Index, r.Begin, r.Length))
		return fmt.Errorf("peer requested piece we don't have: %v", r.Index.Int())
	}

	// l2.Printf("(%d) t(%p) - RECEIVED REQUEST: r(%d,%d,%d)\n", os.Getpid(), c.t, r.Index, r.Begin, r.Length)
	// Check this after we know we have the piece, so that the piece length will be known.
	if r.Begin+r.Length > c.t.pieceLength(pieceIndex(r.Index)) {
		l2.Printf("%p onReadRequest - request has invalid length: %d received (%d+%d), expected (%d)", c, r.Index, r.Begin, r.Length, c.t.pieceLength(pieceIndex(r.Index)))
		metrics.Add("bad requests received", 1)
		return errors.New("bad request")
	}

	if c.PeerRequests == nil {
		c.PeerRequests = make(map[request]struct{}, maxRequests)
	}

	c.PeerRequests[r] = struct{}{}
	// l2.Printf("%p - onReadRequest: accepted request - tickle upload", c)
	c.tickleWriter()
	return nil
}

// Processes incoming BitTorrent wire-protocol messages. The client lock is held upon entry and
// exit. Returning will end the connection.
func (c *connection) mainReadLoop() (err error) {
	// defer l2.Println("mainReadLoop completed")
	defer func() {
		if err != nil {
			metrics.Add("connection.mainReadLoop returned with error", 1)
		} else {
			metrics.Add("connection.mainReadLoop returned with no error", 1)
		}
	}()
	t := c.t

	decoder := pp.Decoder{
		R:         bufio.NewReaderSize(c.r, 1<<17),
		MaxLength: 256 * 1024,
		Pool:      t.chunkPool,
	}
	for {
		var msg pp.Message
		func() {
			t.unlock()
			defer t.lock()
			err = decoder.Decode(&msg)
		}()
		if t.closed.IsSet() || c.closed.IsSet() || err == io.EOF {
			return nil
		}
		if err != nil {
			// l2.Printf("mainReadLoop failed %T - %s\n", err, err)
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

		// l2.Printf("(%d) t(%p) - RECEIVED MESSAGE: %s - %t - %d\n", os.Getpid(), c.t, msg.Type, msg.Keepalive, msg.Index)
		switch msg.Type {
		case pp.Choke:
			c.PeerChoked = true
			c.deleteAllRequests()
			// We can then reset our interest.
			c.updateRequests()
			c.updateExpectingChunks()
		case pp.Unchoke:
			c.PeerChoked = false
			c.updateExpectingChunks()
			c.tickleWriter()
		case pp.Interested:
			c.PeerInterested = true
			c.tickleWriter()
		case pp.NotInterested:
			c.PeerInterested = false
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
			pingAddr := net.UDPAddr{
				IP:   c.remoteAddr.IP,
				Port: int(c.remoteAddr.Port),
			}
			if msg.Port != 0 {
				pingAddr.Port = int(msg.Port)
			}

			t.ping(pingAddr)
		case pp.Suggest:
			metrics.Add("suggests received", 1)
			log.Fmsg("peer suggested piece %d", msg.Index).AddValues(c, msg.Index, debugLogValue).Log(c.t.logger)
			c.updateRequests()
		case pp.HaveAll:
			err = c.onPeerSentHaveAll()
		case pp.HaveNone:
			err = c.peerSentHaveNone()
		case pp.Reject:
			req := newRequestFromMessage(&msg)
			c.deleteRequest(req)
			c.t.piecesM.Pend(req, -1*int(req.Index))
		case pp.AllowedFast:
			metrics.Add("allowed fasts received", 1)
			log.Fmsg("peer allowed fast: %d", msg.Index).AddValues(c, debugLogValue).Log(c.t.logger)
			c.peerAllowedFast.Add(int(msg.Index))
			c.updateRequests()
		case pp.Extended:
			err = c.onReadExtendedMsg(msg.ExtendedID, msg.ExtendedPayload)
		default:
			err = fmt.Errorf("received unknown message type: %#v", msg.Type)
		}
		if err != nil {
			l2.Println("error during read", err)
			return err
		}
	}
}

func (c *connection) onReadExtendedMsg(id pp.ExtensionNumber, payload []byte) (err error) {
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
				metrics.Add(fmt.Sprintf("peers supporting extension %q", name), 1)
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
		err := t.gotMetadataExtensionMsg(payload, c)
		if err != nil {
			return fmt.Errorf("handling metadata extension message: %v", err)
		}
		return nil
	case pexExtendedId:
		if t.config.DisablePEX {
			// TODO: Maybe close the connection. Check that we're not
			// advertising that we support PEX if it's disabled.
			return nil
		}
		var pexMsg pp.PexMsg
		err := bencode.Unmarshal(payload, &pexMsg)
		if err != nil {
			return fmt.Errorf("error unmarshalling PEX message: %s", err)
		}
		metrics.Add("pex added6 peers received", int64(len(pexMsg.Added6)))
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
func (cn *connection) setRW(rw io.ReadWriter) {
	cn.r = rw
	cn.w = rw
}

// Returns the Reader and Writer as a combined ReadWriter.
func (cn *connection) rw() io.ReadWriter {
	return struct {
		io.Reader
		io.Writer
	}{cn.r, cn.w}
}

// Handle a received chunk from a peer.
func (c *connection) receiveChunk(msg *pp.Message) error {
	t := c.t
	metrics.Add("chunks received", 1)

	req := newRequestFromMessage(msg)

	// l2.Printf("(%d) t(%p) - RECEIVED CHUNK: r(%d,%d,%d)\n", os.Getpid(), c.t, req.Index, req.Begin, req.Length)
	if c.PeerChoked {
		metrics.Add("chunks received while choked", 1)
	}

	if c.PeerChoked && c.peerAllowedFast.Get(int(req.Index)) {
		metrics.Add("chunks received due to allowed fast", 1)
	}

	// Request has been satisfied.
	if c.deleteRequest(req) {
		if c.expectingChunks() {
			c.chunksReceivedWhileExpecting++
		}
	} else {
		metrics.Add("chunks received unwanted", 1)
	}

	// Do we actually want this chunk?
	if t.haveChunk(req) {
		metrics.Add("chunks received wasted", 1)
		c.allStats(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadWasted }))
		return nil
	}

	piece := &t.pieces[req.Index]

	c.allStats(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadUseful }))
	c.allStats(add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulData }))
	c.lastUsefulChunkReceived = time.Now()
	t.fastestConn = c

	// Need to record that it hasn't been written yet, before we attempt to do
	// anything with it.
	piece.incrementPendingWrites()
	// Record that we have the chunk, so we aren't trying to download it while
	// waiting for it to be written to storage.
	piece.unpendChunkIndex(chunkIndex(req.chunkSpec, t.chunkSize))
	delete(c.requests, req.digest())
	if err := c.t.piecesM.Verify(req); err != nil {
		metrics.Add("chunks received unexpected", 1)
		return err
	}

	// Cancel pending requests for this chunk.
	for c := range t.conns {
		c.postCancel(req)
	}

	err := func() error {
		t.unlock()
		defer t.lock()
		concurrentChunkWrites.Add(1)
		defer concurrentChunkWrites.Add(-1)
		// Write the chunk out. Note that the upper bound on chunk writing
		// concurrency will be the number of connections. We write inline with
		// receiving the chunk (with this lock dance), because we want to
		// handle errors synchronously and I haven't thought of a nice way to
		// defer any concurrency to the storage and have that notify the
		// client of errors. TODO: Do that instead.
		return t.writeChunk(int(msg.Index), int64(msg.Begin), msg.Piece)
	}()

	piece.decrementPendingWrites()

	if err != nil {
		panic(fmt.Sprintf("error writing chunk: %v", err))
		// t.pendRequest(req)
		// t.updatePieceCompletion(pieceIndex(msg.Index))
		// return nil
	}

	// It's important that the piece is potentially queued before we check if
	// the piece is still wanted, because if it is queued, it won't be wanted.
	if t.pieceAllDirty(pieceIndex(req.Index)) {
		t.queuePieceCheck(pieceIndex(req.Index))
		t.pendAllChunkSpecs(pieceIndex(req.Index))
	}

	c.onDirtiedPiece(pieceIndex(req.Index))

	t.event.Broadcast()
	t.publishPieceChange(pieceIndex(req.Index))

	return nil
}

func (c *connection) onDirtiedPiece(piece pieceIndex) {
	if c.peerTouchedPieces == nil {
		c.peerTouchedPieces = make(map[pieceIndex]struct{})
	}
	c.peerTouchedPieces[piece] = struct{}{}
	ds := &c.t.pieces[piece].dirtiers
	if *ds == nil {
		*ds = make(map[*connection]struct{})
	}
	(*ds)[c] = struct{}{}
}

func (c *connection) uploadAllowed() bool {
	if c.t.config.NoUpload {
		return false
	}

	if c.t.seeding() {
		return true
	}

	if !c.peerHasWantedPieces() {
		return false
	}

	// Don't upload more than 100 KiB more than we download.
	if c.stats.BytesWrittenData.Int64() >= c.stats.BytesReadData.Int64()+100<<10 {
		return false
	}

	return true
}

func (cn *connection) setRetryUploadTimer(delay time.Duration) {
	if cn.uploadTimer == nil {
		cn.uploadTimer = time.AfterFunc(delay, cn.writerCond.Broadcast)
	} else {
		cn.uploadTimer.Reset(delay)
	}
}

// Also handles choking and unchoking of the remote peer.
func (c *connection) upload(msg func(pp.Message) bool) bool {
	// Breaking or completing this loop means we don't want to upload to the
	// peer anymore, and we choke them.
another:
	for c.uploadAllowed() {
		// We want to upload to the peer.
		if !c.Unchoke(msg) {
			return false
		}

		for r := range c.PeerRequests {
			res := c.t.config.UploadRateLimiter.ReserveN(time.Now(), int(r.Length))
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
			delete(c.PeerRequests, r)
			if !more {
				return false
			}
			goto another
		}
		return true
	}
	return c.Choke(msg)
}

func (cn *connection) Drop() {
	cn.t.dropConnection(cn)
}

func (cn *connection) netGoodPiecesDirtied() int64 {
	return cn.stats.PiecesDirtiedGood.Int64() - cn.stats.PiecesDirtiedBad.Int64()
}

func (cn *connection) peerHasWantedPieces() bool {
	return !cn.pieceRequestOrder.IsEmpty()
}

func (cn *connection) numLocalRequests() int {
	return len(cn.requests)
}

func (cn *connection) deleteRequest(r request) bool {
	d := r.digest()
	if _, ok := cn.requests[d]; !ok {
		return false
	}

	cn.t.piecesM.Release(r)
	delete(cn.requests, d)
	cn.updateExpectingChunks()
	if t, ok := cn.t.lastRequested[r]; ok {
		t.Stop()
		delete(cn.t.lastRequested, r)
	}

	cn.updateRequests()
	for _c := range cn.t.conns {
		if !_c.Interested && _c != cn && cn.PeerHasPiece(pieceIndex(r.Index)) {
			_c.updateRequests()
		}
	}
	return true
}

func (cn *connection) deleteAllRequests() {
	for _, r := range cn.requests {
		cn.deleteRequest(r)
	}

	if len(cn.requests) != 0 {
		panic(len(cn.requests))
	}
}

func (cn *connection) tickleWriter() {
	cn.writerCond.Broadcast()
}

func (cn *connection) postCancel(r request) bool {
	if !cn.deleteRequest(r) {
		return false
	}
	cn.Post(makeCancelMessage(r))
	return true
}

func (cn *connection) sendChunk(r request, msg func(pp.Message) bool) (more bool, err error) {
	// Count the chunk being sent, even if it isn't.
	b := make([]byte, r.Length)
	p := cn.t.info.Piece(int(r.Index))
	n, err := cn.t.readAt(b, p.Offset()+int64(r.Begin))
	if n != len(b) {
		if err == nil {
			panic("expected error")
		}
		return
	} else if err == io.EOF {
		err = nil
	}
	// b2 := make([]byte, p.Length())
	// n2, err2 := c.t.readAt(b2, p.Offset())
	// digest := pieceHash.New()
	// digest.Write(b2)
	// l2.Printf("(%p) encoded piece (%d,%d,%d) l(%d) %10v - %s (%d - %d - %s)\n", c.t, r.Index, r.Begin, r.Length, p.Length(), hex.EncodeToString(b2), hex.EncodeToString(digest.Sum(nil)), len(b2), n2, err2)
	// l2.Printf("(%p) encoded chunk (%d,%d,%d) l(%d) %10v", c.t, r.Index, r.Begin, r.Length, len(b), hex.EncodeToString(b))
	more = msg(pp.Message{
		Type:  pp.Piece,
		Index: r.Index,
		Begin: r.Begin,
		Piece: b,
	})
	cn.lastChunkSent = time.Now()
	return
}

func (cn *connection) setTorrent(t *torrent) {
	if cn.t != nil {
		panic("connection already associated with a torrent")
	}
	cn.t = t
	cn.logger.Printf("torrent=%v", t)
	t.reconcileHandshakeStats(cn)
}

func (cn *connection) peerPriority() peerPriority {
	return bep40PriorityIgnoreError(cn.remoteIpPort(), cn.t.publicAddr(cn.remoteIp()))
}

func (cn *connection) remoteIp() net.IP {
	return cn.remoteAddr.IP
}

func (cn *connection) remoteIpPort() IpPort {
	return cn.remoteAddr
}

func (cn *connection) String() string {
	return fmt.Sprintf("connection %p", cn)
}

func (cn *connection) trust() connectionTrust {
	return connectionTrust{Implicit: cn.trusted, NetGoodPiecesDirted: cn.netGoodPiecesDirtied()}
}

type connectionTrust struct {
	Implicit            bool
	NetGoodPiecesDirted int64
}

func (l connectionTrust) Less(r connectionTrust) bool {
	return multiless.New().Bool(l.Implicit, r.Implicit).Int64(l.NetGoodPiecesDirted, r.NetGoodPiecesDirted).Less()
}
