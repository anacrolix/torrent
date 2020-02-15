package torrent

import (
	"bufio"
	"bytes"
	stderrors "errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RoaringBitmap/roaring"
	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/missinggo/bitmap"
	"github.com/anacrolix/missinggo/prioritybitmap"
	"github.com/anacrolix/multiless"
	"github.com/pkg/errors"

	"github.com/james-lawrence/torrent/bencode"
	"github.com/james-lawrence/torrent/internal/x/bitmapx"
	"github.com/james-lawrence/torrent/internal/x/bytesx"
	"github.com/james-lawrence/torrent/mse"
	pp "github.com/james-lawrence/torrent/peer_protocol"
)

type peerSource string

const (
	peerSourceTracker         = "Tr"
	peerSourceIncoming        = "I"
	peerSourceDhtGetPeers     = "Hg" // Peers we found by searching a DHT.
	peerSourceDhtAnnouncePeer = "Ha" // Peers that were announced to us by a DHT.
	peerSourcePex             = "X"
)

func newConnection(nc net.Conn, outgoing bool, remoteAddr IpPort) (c *connection) {
	return &connection{
		_mu:              &sync.RWMutex{},
		conn:             nc,
		outgoing:         outgoing,
		Choked:           true,
		PeerChoked:       true,
		PeerMaxRequests:  250,
		writeBuffer:      new(bytes.Buffer),
		remoteAddr:       remoteAddr,
		touched:          roaring.NewBitmap(),
		fastset:          roaring.NewBitmap(),
		claimed:          roaring.NewBitmap(),
		blacklisted:      roaring.NewBitmap(),
		sentHaves:        roaring.NewBitmap(),
		requests:         make(map[uint64]request, maxRequests),
		PeerRequests:     make(map[request]struct{}, maxRequests),
		drop:             make(chan error, 1),
		PeerExtensionIDs: make(map[pp.ExtensionName]pp.ExtensionNumber),
	}
}

// Maintains the state of a connection with a peer.
type connection struct {
	// First to ensure 64-bit alignment for atomics. See #262.
	stats ConnStats

	t *torrent

	_mu *sync.RWMutex

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
	lastPeriodicUpdate      time.Time

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
	sentHaves        *roaring.Bitmap

	// Stuff controlled by the remote peer.
	PeerID             PeerID
	PeerInterested     bool
	PeerChoked         bool
	PeerRequests       map[request]struct{}
	PeerExtensionBytes pp.PeerExtensionBits

	// bitmaps representing availability of chunks from the peer.
	blacklisted *roaring.Bitmap // represents chunks which we've temporarily blacklisted.
	claimed     *roaring.Bitmap // represents chunks which our peer claims to have available.
	fastset     *roaring.Bitmap // represents chunks which out peer will allow us to request while choked.
	// pieces we've accepted chunks for from the peer.
	touched *roaring.Bitmap

	// // The pieces the peer has claimed to have.
	// peerPieces bitmap.Bitmap

	// The peer has everything. This can occur due to a special message, when
	// we may not even know the number of pieces in the torrent yet.
	peerSentHaveAll bool

	// The highest possible number of pieces the torrent could have based on
	// communication with the peer. Generally only useful until we have the
	// torrent info.
	peerMinPieces pieceIndex

	PeerMaxRequests  int // Maximum pending requests the peer allows.
	PeerExtensionIDs map[pp.ExtensionName]pp.ExtensionNumber
	PeerClientName   string

	pieceInclination  []int
	pieceRequestOrder prioritybitmap.PriorityBitmap

	writeBuffer *bytes.Buffer
	uploadTimer *time.Timer
	writerCond  sync.Cond

	drop chan error
}

func (cn *connection) periodicUpdates() {
	// these tasks only run periodically (every second)
	if cn.lastPeriodicUpdate.After(time.Now()) {
		return
	}

	cn.detectNewAvailability()
	cn.lastPeriodicUpdate = time.Now().Add(time.Second)
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

	cn.cmu().Lock()
	m := cn.claimed.GetCardinality()
	cn.cmu().Unlock()
	return cn.t.piecesM.cmaximum == int64(m), true
}

func (cn *connection) cmu() sync.Locker {
	return cn._mu
}

func (cn *connection) localAddr() net.Addr {
	return cn.conn.LocalAddr()
}

func (cn *connection) supportsExtension(ext pp.ExtensionName) bool {
	return cn.extension(ext) != 0
}

// The best guess at number of pieces in the torrent for this peer.
func (cn *connection) bestPeerNumPieces() pieceIndex {
	if cn.t.haveInfo() {
		return cn.t.numPieces()
	}
	return cn.peerMinPieces
}

func (cn *connection) completedString() string {
	have := pieceIndex(cn.claimed.GetCardinality())
	if cn.peerSentHaveAll {
		have = cn.bestPeerNumPieces()
	}
	return fmt.Sprintf("%d/%d", have, cn.t.piecesM.cmaximum)
}

// Correct the PeerPieces slice length. Return false if the existing slice is
// invalid, such as by receiving badly sized BITFIELD, or invalid HAVE
// messages.
func (cn *connection) setNumPieces(num pieceIndex) error {
	cn.cmu().Lock()
	cn.claimed.RemoveRange(uint64(cn.t.piecesM.cmaximum), cn.claimed.GetCardinality())
	cn.cmu().Unlock()
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
	return parseNetworkString(cn.network).UDP
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
		"    %s completed, good chunks: %v/%v-%v reqq: (%d,%d,%d]-%d, flags: %s, dr: %.1f KiB/s\n",
		cn.completedString(),
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
	// fmt.Fprintf(w, "    next pieces: %v%s\n",
	// 	iter.ToSlice(iter.Head(10, cn.iterPendingPiecesUntyped)),
	// 	func() string {
	// 		if cn.shouldRequestWithoutBias() {
	// 			return " (fastest)"
	// 		} else {
	// 			return ""
	// 		}
	// 	}())
}

func (cn *connection) Close() {
	// trace(fmt.Sprintf("c(%p) initiated", cn))
	// defer trace(fmt.Sprintf("c(%p) completed", cn))
	defer cn.t.cln.event.Broadcast()
	defer cn.deleteAllRequests()
	cn.cmu().Lock()
	defer cn.cmu().Unlock()

	if cn.closed.IsSet() {
		return
	}

	if !cn.closed.Set() {
		return
	}

	if cn.t != nil {
		cn.t.incrementReceivedConns(cn, -1)
	}
	cn.updateRequests()
	cn.discardPieceInclination()
	cn.pieceRequestOrder.Clear()

	if cn.conn != nil {
		go cn.conn.Close()
	}
}

func (cn *connection) PeerHasPiece(piece pieceIndex) bool {
	return cn.peerSentHaveAll || bitmapx.Contains(cn.claimed, cn.t.piecesM.chunks(piece)...)
}

// Writes a message into the write buffer.
func (cn *connection) Post(msg pp.Message) {
	// l2.Output(2, fmt.Sprintf("c(%p) Post initiated: %s\n", cn, msg.Type))
	// l2.Output(2, fmt.Sprintf("c(%p) Post completed: %s\n", cn, msg.Type))

	metrics.Add(fmt.Sprintf("messages posted of type %s", msg.Type.String()), 1)

	// We don't need to track bytes here because a connection.w Writer wrapper
	// takes care of that (although there's some delay between us recording
	// the message, and the connection writer flushing it out.).
	cn.cmu().Lock()
	cn.writeBuffer.Write(msg.MustMarshalBinary())
	cn.cmu().Unlock()

	// Last I checked only Piece messages affect stats, and we don't post
	// those.
	cn.wroteMsg(&msg)
	cn.updateRequests()
}

func (cn *connection) requestMetadataPiece(index int) {
	eID := cn.extension(pp.ExtensionNameMetadata)
	if eID == 0 {
		return
	}

	if index < len(cn.metadataRequests) && cn.metadataRequests[index] {
		return
	}

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
	return int(clamp(
		1,
		int64(cn.PeerMaxRequests),
		max(64, cn.stats.ChunksReadUseful.Int64()-(cn.stats.ChunksRead.Int64()-cn.stats.ChunksReadUseful.Int64())),
	))
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
			}
			return pp.NotInterested
		}(),
	})
}

// The function takes a message to be sent, and returns true if more messages
// are okay.
type messageWriter func(pp.Message) bool

// Proxies the messageWriter's response.
func (cn *connection) request(r request, mw messageWriter) bool {
	cn.cmu().Lock()
	cn.requests[r.Digest] = r
	cn.cmu().Unlock()
	cn.updateExpectingChunks()

	return mw(pp.Message{
		Type:   pp.Request,
		Index:  r.Index,
		Begin:  r.Begin,
		Length: r.Length,
	})
}

func (cn *connection) fillWriteBuffer(msg func(pp.Message) bool) {
	var (
		err       error
		reqs      []request
		req       request
		available *roaring.Bitmap
	)

	if !cn.t.networkingEnabled {
		if !cn.SetInterested(false, msg) {
			return
		}

		if len(cn.requests) != 0 {
			for _, r := range cn.dupRequests() {
				cn.releaseRequest(r)
				// log.Printf("%p: cancelling request: %v", cn, r)
				if !msg(makeCancelMessage(r)) {
					return
				}
			}
		}
	}

	// if we're choked and not allowed to fast track any chunks then there is nothing
	// to do.
	if cn.PeerChoked && cn.fastset.IsEmpty() {
		return
	}

	// release timed out chunks
	if deleted := cn.deleteTimedout((cn.t.piecesM.gracePeriod) + time.Second); deleted == 0 {
		// l2.Printf("c(%p) skipping chunk requests - space unavailable(%d > %d) missing(%t)\n", cn, len(cn.requests), cn.requestsLowWater, cn.t.piecesM.Missing() == 0)
	}

	if len(cn.requests) > cn.requestsLowWater || cn.t.piecesM.Missing() == 0 {
		return
	}

	// l2.Printf("(%d) - c(%p) filling buffer - available(%d) - outstanding (%d)\n", os.Getpid(), cn, cn.peerPieces.Len(), len(cn.requests))
	filledBuffer := false

	if cn.peerSentHaveAll && cn.claimed.IsEmpty() {
		cn.cmu().Lock()
		cn.t.piecesM.fill(cn.claimed)
		cn.cmu().Unlock()
	}

	if cn.PeerChoked && !cn.fastset.IsEmpty() {
		// l2.Printf("(%d) - c(%p) only requesting fast set\n", os.Getpid(), cn)
		available = cn.fastset.Clone()
	} else {
		available = cn.claimed.Clone()
	}

	if !cn.SetInterested(true, msg) {
		return
	}

	// we want the peer to upload to us.
	if !cn.Unchoke(msg) {
		return
	}

	max := cn.nominalMaxRequests() - len(cn.requests)
	if reqs, err = cn.t.piecesM.Pop(max, bitmapx.AndNot(available, cn.blacklisted)); stderrors.As(err, &empty{}) {
		// clear the blacklist when we run out of work to do.
		cn.cmu().Lock()
		cn.blacklisted.Clear()
		cn.cmu().Unlock()

		if len(reqs) == 0 {
			// if cn.t.piecesM.Missing() > 0 && cn.t.piecesM.Missing() < 5 {
			// 	ug := available.Clone()
			// 	ug.And(cn.t.piecesM.missing)
			// 	cn.t.config.info().Printf(
			// 		"(%d) - c(%p) empty queue - available(%d) - outstanding (%d) missing(%d - %s) want(%s) blacklisted(%s)\n",
			// 		os.Getpid(), cn, available.GetCardinality(), len(cn.requests), cn.t.piecesM.Missing(), bitmapx.Debug(cn.t.piecesM.missing), bitmapx.Debug(ug), bitmapx.Debug(cn.blacklisted),
			// 	)
			// }
			return
		}
	} else if err != nil {
		cn.t.config.warn().Printf("failed to request piece: %T - %v\n", err, err)
		return
	}

	for max, req = range reqs {
		// l2.Printf("c(%p) - popped d(%020d) r(%d,%d,%d) - %v\n", cn, req.Digest, req.Index, req.Begin, req.Length, err)

		if cn.closed.IsSet() {
			// l2.Printf("c(%p) closed break - d(%020d) r(%d,%d,%d)\n", cn, req.Digest, req.Index, req.Begin, req.Length)
			break
		}

		// Choking is looked at here because our interest is dependent
		// on whether we'd make requests in its absence.
		if cn.PeerChoked && !cn.fastset.ContainsInt(cn.t.piecesM.requestCID(req)) {
			// l2.Printf("c(%p) choked - d(%020d) r(%d,%d,%d)\n", cn, req.Digest, req.Index, req.Begin, req.Length)
			continue
		} else if cn.PeerChoked {
			// l2.Printf("c(%p) - allowed fast request r(%d,%d,%d) (%d)\n", cn, req.Index, req.Begin, req.Length, req.Priority)
			metrics.Add("allowed fast requests sent", 1)
		}

		if filledBuffer = !cn.request(req, msg); filledBuffer {
			break
		}
	}

	// l2.Printf("c(%p) filled - cleaning up reqs(%d)\n", cn, len(cn.requests))
	// advance to just the unused chunks.
	if len(reqs) > 0 {
		reqs = reqs[max:]
		// release any unused requests back to the queue.
		cn.t.piecesM.Retry(reqs...)
	}

	// If we didn't completely top up the requests, we shouldn't mark
	// the low water, since we'll want to top up the requests as soon
	// as we have more write buffer space.
	if !filledBuffer {
		cn.requestsLowWater = len(cn.requests) / 2
	}
}

// Routine that writes to the peer. Some of what to write is buffered by
// activity elsewhere in the Client, and some is determined locally when the
// connection is writable.
func (cn *connection) writer(keepAliveTimeout time.Duration) {
	// cn.t.config.info().Printf("c(%p) writer initiated\n", cn)
	// defer cn.t.config.info().Printf("c(%p) writer completed\n", cn)
	defer cn.checkFailures()
	defer cn.deleteAllRequests()

	var (
		lastWrite      time.Time = time.Now()
		keepAliveTimer *time.Timer
	)

	keepAliveTimer = time.AfterFunc(time.Second, func() {
		if time.Since(lastWrite) >= keepAliveTimeout {
			cn.updateRequests()
		}
		keepAliveTimer.Reset(keepAliveTimeout)
	})

	defer keepAliveTimer.Stop()

	writer := func(msg pp.Message) bool {
		cn.writeBuffer.Write(msg.MustMarshalBinary())
		cn.wroteMsg(&msg)
		metrics.Add(fmt.Sprintf("messages filled of type %s", msg.Type.String()), 1)
		return cn.writeBuffer.Len() < 64*bytesx.KiB
	}

	for attempts := 0; ; attempts++ {
		cn.periodicUpdates()

		if cn.closed.IsSet() {
			return
		}

		if !cn.lastMessageReceived.IsZero() && time.Since(cn.lastMessageReceived) > 2*keepAliveTimeout {
			select {
			case cn.drop <- fmt.Errorf("killing connection %v %v %v", cn.remoteIP(), time.Since(cn.lastMessageReceived), cn.lastMessageReceived):
				cn.Close()
			default:
			}
			return
		}

		if cn.writeBuffer.Len() == 0 {
			cn.checkFailures()
			cn.fillWriteBuffer(writer)
			cn.upload(writer)
		}

		if cn.writeBuffer.Len() == 0 && time.Since(lastWrite) >= keepAliveTimeout {
			cn.writeBuffer.Write(pp.Message{Keepalive: true}.MustMarshalBinary())
			postedKeepalives.Add(1)
		}

		// TODO: Minimize wakeups....
		if cn.writeBuffer.Len() == 0 {
			// let the loop spin for a couple iterations when there is nothing to write.
			// this helps prevent some stalls should be able to remove later.
			if attempts < 50 {
				time.Sleep(50 * time.Millisecond)
				continue
			}

			cn.cmu().Lock()
			// l2.Printf("c(%p) writer going to sleep: %d\n", cn, cn.writeBuffer.Len())
			cn.writerCond.Wait()
			// l2.Printf("(%d) c(%p) - writer awake: pending(%d) - remaining(%d) - failed(%d) - outstanding(%d) - unverified(%d) - completed(%d)\n", os.Getpid(), cn, len(cn.requests), cn.t.piecesM.Missing(), cn.t.piecesM.failed.GetCardinality(), len(cn.t.piecesM.outstanding), cn.t.piecesM.unverified.Len(), cn.t.piecesM.completed.Len())
			cn.cmu().Unlock()
			continue
		}

		// reset the attempts
		attempts = 0

		// l2.Printf("(%d) c(%p) - WRITING: attempt(%d) - remaining(%d) - unverified(%d) - failed(%d) - outstanding(%d) - completed(%d)\n", os.Getpid(), cn, attempts, cn.t.piecesM.Missing(), cn.t.piecesM.unverified.Len(), cn.t.piecesM.failed.GetCardinality(), len(cn.requests), cn.t.piecesM.completed.Len())

		cn.cmu().Lock()
		buf := cn.writeBuffer.Bytes()
		cn.writeBuffer.Reset()
		n, err := cn.w.Write(buf)
		cn.cmu().Unlock()

		if n != 0 {
			lastWrite = time.Now()
			keepAliveTimer.Reset(keepAliveTimeout)
		}

		if err != nil {
			// cn.t.logger.Printf("c(%p) error writing requests: %v", cn, err)
			return
		}

		if n != len(buf) {
			cn.t.config.warn().Printf("error: write failed written != len(buf) (%d != %d)\n", n, len(buf))
			cn.Close()
			return
		}
	}
}

// connections check their own failures, this amortizes the cost of failures to
// the connections themselves instead of bottlenecking at the torrent.
func (cn *connection) checkFailures() {
	cn.cmu().Lock()
	defer cn.cmu().Unlock()

	failed := cn.t.piecesM.Failed(cn.touched.Clone())

	if failed.IsEmpty() {
		return
	}

	// log.Output(2, fmt.Sprintf("c(%p) detected failed chunks: %s", cn, bitmapx.Debug(failed)))

	iter := failed.ReverseIterator()
	for prev, pid := -1, 0; iter.HasNext(); prev = pid {
		pid = cn.t.piecesM.pindex(int(iter.Next()))
		if pid != prev {
			cn.stats.incrementPiecesDirtiedBad()
			if !cn.t.piecesM.ChunksComplete(pid) {
				cn.t.piecesM.ChunksRetry(pid)
			}
		}
	}

	if cn.stats.PiecesDirtiedBad.Int64() > 10 {
		cn.ban()
	}
}

func (cn *connection) ban() {
	select {
	case cn.drop <- banned{IP: cn.remoteAddr.IP}:
		cn.Close()
	default:
	}
}

func (cn *connection) Have(piece pieceIndex) {
	cn.cmu().Lock()
	if !cn.sentHaves.IsEmpty() && cn.sentHaves.ContainsInt(piece) {
		cn.cmu().Unlock()
		return
	}
	cn.sentHaves.AddInt(piece)
	cn.cmu().Unlock()

	cn.Post(pp.Message{
		Type:  pp.Have,
		Index: pp.Integer(piece),
	})
}

func (cn *connection) PostBitfield() {
	if !cn.t.haveAnyPieces() {
		return
	}

	dup := cn.t.piecesM.completed.Clone()
	cn.Post(pp.Message{
		Type:     pp.Bitfield,
		Bitfield: bitmapx.Bools(cn.t.numPieces(), dup),
	})
	cn.sentHaves = bitmapx.Lazy(dup)
}

func (cn *connection) updateRequests() {
	// l2.Output(2, fmt.Sprintf("(%d) c(%p) - notifying writer\n", os.Getpid(), cn))
	cn.writerCond.Broadcast()
}

// The connection should download highest priority pieces first, without any
// inclination toward avoiding wastage. Generally we might do this if there's
// a single connection, or this is the fastest connection, and we have active
// readers that signal an ordering preference. It's conceivable that the best
// connection should do this, since it's least likely to waste our time if
// assigned to the highest priority pieces, and assigning more than one this
// role would cause significant wasted bandwidth.
func (cn *connection) shouldRequestWithoutBias() bool {
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

func (cn *connection) stopRequestingPiece(piece pieceIndex) bool {
	return cn.pieceRequestOrder.Remove(bitmap.BitIndex(piece))
}

// This is distinct from Torrent piece priority, which is the user's
// preference. Connection piece priority is specific to a connection and is
// used to pseudorandomly avoid connections always requesting the same pieces
// and thus wasting effort.
func (cn *connection) updatePiecePriority(piece pieceIndex) bool {
	cn.cmu().Lock()
	defer cn.cmu().Unlock()

	tpp := cn.t.piecePriority(piece)
	if !cn.PeerHasPiece(piece) {
		tpp = PiecePriorityNone
	}
	if tpp == PiecePriorityNone {
		return cn.stopRequestingPiece(piece)
	}
	prio := cn.getPieceInclination()[piece]

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
			cn.t.event.Broadcast()
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
	// l2.Printf("(%d) c(%p) - RECEIVED HAVE: r(%d,-,-)\n", os.Getpid(), cn, piece)
	if cn.t.haveInfo() && piece >= cn.t.numPieces() || piece < 0 {
		return errors.New("invalid piece")
	}

	if cn.PeerHasPiece(piece) {
		return nil
	}

	cn.raisePeerMinPieces(piece + 1)

	cn.cmu().Lock()
	for _, cidx := range cn.t.piecesM.chunks(int(piece)) {
		cn.claimed.AddInt(cidx)
		cn.blacklisted.Remove(uint32(cidx))
	}
	cn.cmu().Unlock()

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

		for _, c := range cn.t.piecesM.chunks(i) {
			cn.claimed.AddInt(c)
		}
	}
	cn.peerPiecesChanged()
	return nil
}

func (cn *connection) onPeerSentHaveAll() error {
	cn.cmu().Lock()
	cn.peerSentHaveAll = true
	cn.claimed.Clear()
	cn.cmu().Unlock()
	cn.peerPiecesChanged()
	return nil
}

func (cn *connection) peerSentHaveNone() error {
	cn.cmu().Lock()
	cn.peerSentHaveAll = false
	cn.claimed.Clear()
	cn.cmu().Unlock()
	cn.peerPiecesChanged()
	return nil
}

func (cn *connection) extension(id pp.ExtensionName) pp.ExtensionNumber {
	cn._mu.RLock()
	defer cn._mu.RUnlock()
	return cn.PeerExtensionIDs[pp.ExtensionNameMetadata]
}

func (cn *connection) requestPendingMetadata() {
	if cn.t.haveInfo() {
		return
	}

	if cn.extension(pp.ExtensionNameMetadata) == 0 {
		// Peer doesn't support this.
		return
	}

	// Request metadata pieces that we don't have in a random order.
	var pending []int
	for index := 0; index < cn.t.metadataPieceCount(); index++ {
		if !cn.t.haveMetadataPiece(index) && !cn.requestedMetadataPiece(index) {
			pending = append(pending, index)
		}
	}
	rand.Shuffle(len(pending), func(i, j int) { pending[i], pending[j] = pending[j], pending[i] })
	for _, i := range pending {
		cn.requestMetadataPiece(i)
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
	cn.allStats(add(n, func(cs *ConnStats) *count { return &cs.BytesWritten }))
}

func (cn *connection) readBytes(n int64) {
	cn.allStats(add(n, func(cs *ConnStats) *count { return &cs.BytesRead }))
}

// Returns whether the connection could be useful to us. We're seeding and
// they want data, we don't have metainfo and they can provide it, etc.
func (cn *connection) useful() bool {
	t := cn.t
	if cn.closed.IsSet() {
		return false
	}
	if !t.haveInfo() {
		return cn.supportsExtension("ut_metadata")
	}
	if t.seeding() && cn.PeerInterested {
		return true
	}
	if cn.peerHasWantedPieces() {
		return true
	}
	return false
}

func (cn *connection) lastHelpful() (ret time.Time) {
	ret = cn.lastUsefulChunkReceived
	if cn.t.seeding() && cn.lastChunkSent.After(ret) {
		ret = cn.lastChunkSent
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

func (cn *connection) onReadRequest(r request) error {
	requestedChunkLengths.Add(strconv.FormatUint(r.Length.Uint64(), 10), 1)
	cn._mu.RLock()
	_, ok := cn.PeerRequests[r]
	cn._mu.RUnlock()

	if ok {
		metrics.Add("duplicate requests received", 1)
		return nil
	}

	if cn.Choked {
		metrics.Add("requests received while choking", 1)
		if cn.fastEnabled() {
			// l2.Printf("%p - onReadRequest: choked, rejecting request", cn)
			metrics.Add("requests rejected while choking", 1)
			cn.reject(r)
		}
		return nil
	}

	if len(cn.PeerRequests) >= maxRequests {
		metrics.Add("requests received while queue full", 1)
		if cn.fastEnabled() {
			// log.Printf("%p - onReadRequest: PeerRequests >= maxRequests, rejecting request", cn)
			cn.reject(r)
		}
		// BEP 6 says we may close here if we choose.
		return nil
	}

	if !cn.t.havePiece(pieceIndex(r.Index)) {
		// This isn't necessarily them screwing up. We can drop pieces
		// from our storage, and can't communicate this to peers
		// except by reconnecting.
		requestsReceivedForMissingPieces.Add(1)
		// log.Output(2, fmt.Sprintf("t(%p) - onReadRequest: requested piece not available: r(%d,%d,%d)", cn.t, r.Index, r.Begin, r.Length))
		return fmt.Errorf("peer requested piece we don't have: %v", r.Index.Int())
	}

	// l2.Printf("(%d) c(%p) - RECEIVED REQUEST: r(%d,%d,%d)\n", os.Getpid(), cn, r.Index, r.Begin, r.Length)

	// Check this after we know we have the piece, so that the piece length will be known.
	if r.Begin+r.Length > cn.t.pieceLength(pieceIndex(r.Index)) {
		// log.Printf("%p onReadRequest - request has invalid length: %d received (%d+%d), expected (%d)", cn, r.Index, r.Begin, r.Length, cn.t.pieceLength(pieceIndex(r.Index)))
		metrics.Add("bad requests received", 1)
		return errors.New("bad request")
	}

	cn.cmu().Lock()
	cn.PeerRequests[r] = struct{}{}
	cn.cmu().Unlock()

	return nil
}

// Processes incoming BitTorrent wire-protocol messages. The client lock is held upon entry and
// exit. Returning will end the connection.
func (cn *connection) mainReadLoop() (err error) {
	// l2.Printf("(%d) c(%p) mainReadLoop initiated\n", os.Getpid(), cn)
	// defer l2.Printf("(%d) c(%p) mainReadLoop completed\n", os.Getpid(), cn)
	defer cn.updateRequests() // tap the writer so it'll clean itself up.
	defer func() {
		if err != nil {
			metrics.Add("connection.mainReadLoop returned with error", 1)
		} else {
			metrics.Add("connection.mainReadLoop returned with no error", 1)
		}
	}()

	// limit := rate.NewLimiter(rate.Every(time.Second), 1)
	decoder := pp.Decoder{
		R:         bufio.NewReaderSize(cn.r, 1<<17),
		MaxLength: 256 * 1024,
		Pool:      cn.t.chunkPool,
	}

	for {
		var msg pp.Message
		err = decoder.Decode(&msg)

		// check for any error signals from the writer.
		select {
		case err = <-cn.drop:
			return err
		default:
		}

		if cn.t.closed.IsSet() || cn.closed.IsSet() || err == io.EOF {
			return nil
		}

		if err != nil {
			return err
		}

		cn.readMsg(&msg)
		cn.lastMessageReceived = time.Now()

		if msg.Keepalive {
			receivedKeepalives.Add(1)
			continue
		}

		messageTypesReceived.Add(msg.Type.String(), 1)
		if msg.Type.FastExtension() && !cn.fastEnabled() {
			return fmt.Errorf("received fast extension message (type=%v) but extension is disabled", msg.Type)
		}

		// l2.Printf("(%d) c(%p) - RECEIVED MESSAGE: %s - pending(%d) - remaining(%d) - failed(%d) - outstanding(%d) - unverified(%d) - completed(%d)\n", os.Getpid(), cn, msg.Type, len(cn.requests), cn.t.piecesM.Missing(), cn.t.piecesM.failed.GetCardinality(), len(cn.t.piecesM.outstanding), cn.t.piecesM.unverified.Len(), cn.t.piecesM.completed.GetCardinality())

		switch msg.Type {
		case pp.Choke:
			cn.PeerChoked = true
			cn.deleteAllRequests()
			cn.updateExpectingChunks()
			// We can then reset our interest.
		case pp.Unchoke:
			cn.PeerChoked = false
			cn.updateExpectingChunks()
			cn.updateRequests()
		case pp.Interested:
			cn.PeerInterested = true
			cn.updateRequests()
		case pp.NotInterested:
			cn.PeerInterested = false
			cn.updateRequests()
			// We don't clear their requests since it isn't clear in the spec.
			// We'll probably choke them for this, which will clear them if
			// appropriate, and is clearly specified.
		case pp.Have:
			err = cn.peerSentHave(pieceIndex(msg.Index))
			cn.updateRequests()
		case pp.Bitfield:
			err = cn.peerSentBitfield(msg.Bitfield)
			cn.updateRequests()
		case pp.Request:
			r := newRequestFromMessage(&msg)
			if err = cn.onReadRequest(r); err == nil {
				cn.updateRequests()
			}
		case pp.Piece:
			if err = errors.Wrap(cn.receiveChunk(&msg), "failed to received chunk"); err == nil {
				if len(msg.Piece) == int(cn.t.chunkSize) {
					cn.t.chunkPool.Put(&msg.Piece)
				}
				cn.updateRequests()
			}
		case pp.Cancel:
			req := newRequestFromMessage(&msg)
			cn.onPeerSentCancel(req)
		case pp.Port:
			pingAddr := net.UDPAddr{
				IP:   cn.remoteAddr.IP,
				Port: int(cn.remoteAddr.Port),
			}

			if msg.Port != 0 {
				pingAddr.Port = int(msg.Port)
			}

			cn.t.ping(pingAddr)
		case pp.Suggest:
			metrics.Add("suggests received", 1)
			cn.t.config.debug().Println("peer suggested piece", msg.Index)
		case pp.HaveAll:
			if err = cn.onPeerSentHaveAll(); err == nil {
				cn.updateRequests()
			}
		case pp.HaveNone:
			if err = cn.peerSentHaveNone(); err == nil {
				cn.updateRequests()
			}
		case pp.Reject:
			req := newRequestFromMessage(&msg)
			if !cn.fastEnabled() {
				return fmt.Errorf("reject recevied, fast not enabled")
			}
			// l2.Printf("(%d) c(%p) REJECTING d(%d) r(%d,%d,%d) cid(%d) cmax(%d) - total(%d) plength(%d) clength(%d)\n", os.Getpid(), cn, req.Digest, req.Index, req.Begin, req.Length, cn.t.piecesM.requestCID(req), cn.t.piecesM.cmaximum, cn.t.info.TotalLength(), cn.t.info.PieceLength, cn.t.piecesM.clength)
			cn.releaseRequest(req)
			cn.cmu().Lock()
			cn.blacklisted.AddInt(cn.t.piecesM.requestCID(req))
			cn.cmu().Unlock()
		case pp.AllowedFast:
			metrics.Add("allowed fasts received", 1)
			cn.t.config.debug().Println("peer allowed fast:", msg.Index)
			for _, cidx := range cn.t.piecesM.chunks(int(msg.Index)) {
				cn.fastset.AddInt(cidx)
			}
			cn.updateRequests()
		case pp.Extended:
			if err = cn.onReadExtendedMsg(msg.ExtendedID, msg.ExtendedPayload); err == nil {
				cn.updateRequests()
			}
		default:
			err = errors.Errorf("received unknown message type: %#v", msg.Type)
		}

		if err != nil {
			return err
		}
	}
}

func (cn *connection) onReadExtendedMsg(id pp.ExtensionNumber, payload []byte) (err error) {
	defer func() {
		// TODO: Should we still do this?
		if err != nil {
			// These clients use their own extension IDs for outgoing message
			// types, which is incorrect.
			if bytes.HasPrefix(cn.PeerID[:], []byte("-SD0100-")) || strings.HasPrefix(string(cn.PeerID[:]), "-XL0012-") {
				err = nil
			}
		}
	}()

	t := cn.t
	switch id {
	case pp.HandshakeExtendedID:
		var d pp.ExtendedHandshakeMessage
		if err := bencode.Unmarshal(payload, &d); err != nil {
			cn.t.config.errors().Printf("error parsing extended handshake message %q: %s\n", payload, err)
			return errors.Wrap(err, "unmarshalling extended handshake payload")
		}
		if d.Reqq != 0 {
			cn.PeerMaxRequests = d.Reqq
		}
		cn.PeerClientName = d.V

		for name, id := range d.M {
			if !cn.supportsExtension(name) {
				metrics.Add(fmt.Sprintf("peers supporting extension %q", name), 1)
			}
			cn.cmu().Lock()
			cn.PeerExtensionIDs[name] = id
			cn.cmu().Unlock()
		}
		if d.MetadataSize != 0 {
			if err = t.setMetadataSize(d.MetadataSize); err != nil {
				return errors.Wrapf(err, "setting metadata size to %d", d.MetadataSize)
			}
		}
		cn.requestPendingMetadata()
		return nil
	case metadataExtendedID:
		err := t.gotMetadataExtensionMsg(payload, cn)
		if err != nil {
			return errors.Errorf("handling metadata extension message: %v", err)
		}
		return nil
	case pexExtendedID:
		if t.config.DisablePEX {
			// TODO: Maybe close the connection. Check that we're not
			// advertising that we support PEX if it's disabled.
			return nil
		}
		var pexMsg pp.PexMsg
		err := bencode.Unmarshal(payload, &pexMsg)
		if err != nil {
			return errors.Errorf("error unmarshalling PEX message: %s", err)
		}
		metrics.Add("pex added6 peers received", int64(len(pexMsg.Added6)))
		var peers Peers
		peers.AppendFromPex(pexMsg.Added6, pexMsg.Added6Flags)
		peers.AppendFromPex(pexMsg.Added, pexMsg.AddedFlags)
		t.AddPeers(peers)
		return nil
	default:
		return errors.Errorf("unexpected extended message ID: %v", id)
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
func (cn *connection) receiveChunk(msg *pp.Message) error {
	metrics.Add("chunks received", 1)

	req := newRequestFromMessage(msg)

	// l2.Printf("(%d) c(%p) - RECEIVED CHUNK: r(%d,%d,%d)\n", os.Getpid(), cn, req.Index, req.Begin, req.Length)

	if cn.PeerChoked {
		metrics.Add("chunks received while choked", 1)
	}

	if cn.PeerChoked && cn.fastset.ContainsInt(int(req.Index)) {
		metrics.Add("chunks received due to allowed fast", 1)
	}

	if cn.clearRequest(req) {
		if cn.expectingChunks() {
			cn.chunksReceivedWhileExpecting++
		}
	} else {
		metrics.Add("chunks received unwanted", 1)
	}

	// Do we actually want this chunk? if the chunk is already available, then we
	// don't need it.
	if cn.t.piecesM.Available(req) {
		cn.t.piecesM.Release(req)
		// l2.Printf("c(%p) - wasted chunk d(%020d) r(%d,%d,%d)\n", cn, req.Digest, req.Index, req.Begin, req.Length)
		metrics.Add("chunks received wasted", 1)
		cn.allStats(add(1, func(cs *ConnStats) *count { return &cs.ChunksReadWasted }))
		return nil
	}

	piece := &cn.t.pieces[req.Index]

	cn.allStats(add(1, func(cs *ConnStats) *count { return &cs.ChunksReadUseful }))
	cn.allStats(add(int64(len(msg.Piece)), func(cs *ConnStats) *count { return &cs.BytesReadUsefulData }))
	cn.lastUsefulChunkReceived = time.Now()
	cn.t.fastestConn = cn

	// Need to record that it hasn't been written yet, before we attempt to do
	// anything with it.
	piece.incrementPendingWrites()
	// Record that we have the chunk, so we aren't trying to download it while
	// waiting for it to be written to storage.
	piece.unpendChunkIndex(chunkIndex(req.chunkSpec, cn.t.chunkSize))
	err := cn.t.writeChunk(int(msg.Index), int64(msg.Begin), msg.Piece)
	piece.decrementPendingWrites()

	if err := cn.t.piecesM.Verify(req); err != nil {
		metrics.Add("chunks received unexpected", 1)
		return err
	}

	if err != nil {
		panic(fmt.Sprintf("error writing chunk: %v", err))
		// t.pendRequest(req)
		// t.updatePieceCompletion(pieceIndex(msg.Index))
		// return nil
	}

	// It's important that the piece is potentially queued before we check if
	// the piece is still wanted, because if it is queued, it won't be wanted.
	if cn.t.piecesM.ChunksAvailable(int(req.Index)) {
		cn.t.digests.Enqueue(int(req.Index))
		cn.t.pendAllChunkSpecs(pieceIndex(req.Index))
	}

	cn.cmu().Lock()
	cn.touched.AddInt(cn.t.piecesM.requestCID(req))
	cn.cmu().Unlock()

	cn.t.publishPieceChange(pieceIndex(req.Index))

	return nil
}

func (cn *connection) uploadAllowed() bool {
	if cn.t.config.NoUpload {
		return false
	}

	if cn.t.seeding() {
		return true
	}

	if !cn.peerHasWantedPieces() {
		return false
	}

	// Don't upload more than 100 KiB more than we download.
	if cn.stats.BytesWrittenData.Int64() >= cn.stats.BytesReadData.Int64()+100<<10 {
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

func (cn *connection) detectNewAvailability() {
	dup := cn.t.piecesM.completed.Clone()
	dup.AndNot(cn.sentHaves)
	count := 0
	for i := dup.Iterator(); i.HasNext(); count++ {
		cn.Have(int(i.Next()))
	}
}

// Also handles choking and unchoking of the remote peer.
func (cn *connection) upload(msg func(pp.Message) bool) bool {
	cn.cmu().Lock()
	defer cn.cmu().Unlock()
	// Breaking or completing this loop means we don't want to upload to the
	// peer anymore, and we choke them.

another:
	for cn.uploadAllowed() {
		// We want to upload to the peer.
		if !cn.Unchoke(msg) {
			return false
		}

		for r := range cn.PeerRequests {
			res := cn.t.config.UploadRateLimiter.ReserveN(time.Now(), int(r.Length))
			if !res.OK() {
				cn.t.config.errors().Printf("upload rate limiter burst size < %d\n", r.Length)
				go cn.ban() // pan this IP address, we'll never be able to support them.
				return false
			}

			delay := res.Delay()
			if delay > 0 {
				res.Cancel()
				cn.setRetryUploadTimer(delay)
				// Hard to say what to return here.
				return true
			}

			more, err := cn.sendChunk(r, msg)
			if err != nil {
				i := pieceIndex(r.Index)
				if cn.t.pieceComplete(i) {
					cn.t.updatePieceCompletion(i)
					if !cn.t.pieceComplete(i) {
						// We had the piece, but not anymore.
						break another
					}
				}
				cn.t.config.warn().Println("error sending chunk to peer")
				// If we failed to send a chunk, choke the peer to ensure they
				// flush all their requests. We've probably dropped a piece,
				// but there's no way to communicate this to the peer. If they
				// ask for it again, we'll kick them to allow us to send them
				// an updated bitfield.
				break another
			}

			delete(cn.PeerRequests, r)

			if !more {
				return false
			}
			goto another
		}
		return true
	}

	return cn.Choke(msg)
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

// delete requests that were requested beyond the timeout.
func (cn *connection) deleteTimedout(grace time.Duration) int {
	ts := time.Now()
	deleted := make([]request, 0, 100)
	for _, req := range cn.dupRequests() {
		if req.Reserved.Add(grace).Before(ts) && cn.clearRequest(req) {
			deleted = append(deleted, req)
		}

		if time.Since(ts) > 100*time.Millisecond {
			break
		}
	}

	// cn.t.piecesM.Retry(deleted...)

	return len(deleted)
}

// clearRequest drops the request from the local connection.
func (cn *connection) clearRequest(r request) bool {
	cn.cmu().Lock()
	defer cn.cmu().Unlock()
	if _, ok := cn.requests[r.Digest]; !ok {
		return false
	}

	// l2.Printf("c(%p) - clearing request d(%020d) r(%d,%d,%d)\n", cn, r.Digest, r.Index, r.Begin, r.Length)
	// add requests that have been released to the reject set to prevent them from
	// being requested from this connection until rejected is reset.
	cn.blacklisted.AddInt(cn.t.piecesM.requestCID(r))
	delete(cn.requests, r.Digest)

	cn.updateExpectingChunks()
	cn.updateRequests()

	return true
}

// releaseRequest returns the request back to the pool.
func (cn *connection) releaseRequest(r request) (ok bool) {
	cn.cmu().Lock()
	defer cn.cmu().Unlock()
	if r, ok = cn.requests[r.Digest]; !ok {
		return false
	}

	// l2.Printf("c(%p) - releasing request d(%020d) r(%d,%d,%d)\n", cn, r.Digest, r.Index, r.Begin, r.Length)
	delete(cn.requests, r.Digest)
	cn.t.piecesM.Retry(r)

	cn.updateExpectingChunks()
	cn.updateRequests()

	return true
}

func (cn *connection) dupRequests() (requests []request) {
	cn._mu.RLock()
	for _, r := range cn.requests {
		requests = append(requests, r)
	}
	cn._mu.RUnlock()
	return requests
}

func (cn *connection) deleteAllRequests() {
	// trace(fmt.Sprintf("c(%p) initiated", cn))
	reqs := cn.dupRequests()
	// defer trace(fmt.Sprintf("c(%p) completed reqs(%d)", cn, len(reqs)))
	for _, r := range reqs {
		cn.releaseRequest(r)
	}
	// cn.t.piecesM.Retry(reqs...)
}

func (cn *connection) postCancel(r request) bool {
	if ok := cn.releaseRequest(r); !ok {
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
		cn.t.config.errors().Println("BUG: connection already associated with a torrent")
		go cn.Close()
	}
	cn.t = t

	t.incrementReceivedConns(cn, 1)
	t.reconcileHandshakeStats(cn)
}

func (cn *connection) peerPriority() peerPriority {
	return bep40PriorityIgnoreError(cn.remoteIPPort(), cn.t.publicAddr(cn.remoteIP()))
}

func (cn *connection) remoteIP() net.IP {
	return cn.remoteAddr.IP
}

func (cn *connection) remoteIPPort() IpPort {
	return cn.remoteAddr
}

func (cn *connection) String() string {
	return fmt.Sprintf("connection %p", cn)
}

func (cn *connection) trust() connectionTrust {
	return connectionTrust{Implicit: cn.trusted, NetGoodPiecesDirted: cn.netGoodPiecesDirtied()}
}

// See the order given in Transmission's tr_peerMsgsNew.
func (cn *connection) sendInitialMessages(cl *Client, torrent *torrent) {
	if cn.PeerExtensionBytes.SupportsExtended() && cl.extensionBytes.SupportsExtended() {
		cn.Post(pp.Message{
			Type:       pp.Extended,
			ExtendedID: pp.HandshakeExtendedID,
			ExtendedPayload: func() []byte {
				msg := pp.ExtendedHandshakeMessage{
					M: map[pp.ExtensionName]pp.ExtensionNumber{
						pp.ExtensionNameMetadata: metadataExtendedID,
					},
					V:            cl.config.ExtendedHandshakeClientVersion,
					Reqq:         64, // TODO: Really?
					YourIp:       pp.CompactIp(cn.remoteAddr.IP),
					Encryption:   cl.config.HeaderObfuscationPolicy.Preferred || !cl.config.HeaderObfuscationPolicy.RequirePreferred,
					Port:         cl.incomingPeerPort(),
					MetadataSize: torrent.metadataSize(),
					// TODO: We can figured these out specific to the socket
					// used.
					Ipv4: pp.CompactIp(cl.config.PublicIP4.To4()),
					Ipv6: cl.config.PublicIP6.To16(),
				}
				if !cl.config.DisablePEX {
					msg.M[pp.ExtensionNamePex] = pexExtendedID
				}
				return bencode.MustMarshal(msg)
			}(),
		})
	}

	if cn.fastEnabled() {
		if torrent.haveAllPieces() {
			cn.Post(pp.Message{Type: pp.HaveAll})
			cn.sentHaves.AddRange(0, uint64(cn.t.NumPieces()))
			return
		} else if !torrent.haveAnyPieces() {
			cn.Post(pp.Message{Type: pp.HaveNone})
			cn.sentHaves.Clear()
			return
		}
	} else {
		cn.PostBitfield()
	}

	if cn.PeerExtensionBytes.SupportsDHT() && cl.extensionBytes.SupportsDHT() && cl.haveDhtServer() {
		cn.Post(pp.Message{
			Type: pp.Port,
			Port: cl.dhtPort(),
		})
	}
}

type connectionTrust struct {
	Implicit            bool
	NetGoodPiecesDirted int64
}

func (l connectionTrust) Less(r connectionTrust) bool {
	return multiless.New().Bool(l.Implicit, r.Implicit).Int64(l.NetGoodPiecesDirted, r.NetGoodPiecesDirted).Less()
}
