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

func (p *Peer) initRequestState() {
	p.requestState.Requests = &peerRequests{}
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

func (pc *PeerConn) connStatusString() string {
	return fmt.Sprintf("%+-55q %s %s", pc.PeerID, pc.PeerExtensionBytes, pc.connString)
}

func (p *Peer) updateExpectingChunks() {
	if p.expectingChunks() {
		if p.lastStartedExpectingToReceiveChunks.IsZero() {
			p.lastStartedExpectingToReceiveChunks = time.Now()
		}
	} else {
		if !p.lastStartedExpectingToReceiveChunks.IsZero() {
			p.cumulativeExpectedToReceiveChunks += time.Since(p.lastStartedExpectingToReceiveChunks)
			p.lastStartedExpectingToReceiveChunks = time.Time{}
		}
	}
}

func (p *Peer) expectingChunks() bool {
	if p.requestState.Requests.IsEmpty() {
		return false
	}
	if !p.requestState.Interested {
		return false
	}
	if !p.peerChoking {
		return true
	}
	haveAllowedFastRequests := false
	p.peerAllowedFast.Iterate(func(i pieceIndex) bool {
		haveAllowedFastRequests = roaringBitmapRangeCardinality[RequestIndex](
			p.requestState.Requests,
			p.t.pieceRequestIndexOffset(i),
			p.t.pieceRequestIndexOffset(i+1),
		) == 0
		return !haveAllowedFastRequests
	})
	return haveAllowedFastRequests
}

func (p *Peer) remoteChokingPiece(piece pieceIndex) bool {
	return p.peerChoking && !p.peerAllowedFast.Contains(piece)
}

// Returns true if the connection is over IPv6.
func (pc *PeerConn) ipv6() bool {
	ip := pc.remoteIp()
	if ip.To4() != nil {
		return false
	}
	return len(ip) == net.IPv6len
}

// Returns true the if the dialer/initiator has the lower client peer ID. TODO: Find the
// specification for this.
func (pc *PeerConn) isPreferredDirection() bool {
	return bytes.Compare(pc.t.cl.peerID[:], pc.PeerID[:]) < 0 == pc.outgoing
}

// Returns whether the left connection should be preferred over the right one,
// considering only their networking properties. If ok is false, we can't
// decide.
func (pc *PeerConn) hasPreferredNetworkOver(r *PeerConn) bool {
	var ml multiless.Computation
	ml = ml.Bool(r.isPreferredDirection(), pc.isPreferredDirection())
	ml = ml.Bool(pc.utp(), r.utp())
	ml = ml.Bool(r.ipv6(), pc.ipv6())
	return ml.Less()
}

func (p *Peer) cumInterest() time.Duration {
	ret := p.priorInterest
	if p.requestState.Interested {
		ret += time.Since(p.lastBecameInterested)
	}
	return ret
}

func (pc *PeerConn) peerHasAllPieces() (all, known bool) {
	if pc.peerSentHaveAll {
		return true, true
	}
	if !pc.t.haveInfo() {
		return false, false
	}
	return pc._peerPieces.GetCardinality() == uint64(pc.t.numPieces()), true
}

func (p *Peer) locker() *lockWithDeferreds {
	return p.t.cl.locker()
}

func (p *Peer) supportsExtension(ext pp.ExtensionName) bool {
	_, ok := p.PeerExtensionIDs[ext]
	return ok
}

// The best guess at number of pieces in the torrent for this peer.
func (p *Peer) bestPeerNumPieces() pieceIndex {
	if p.t.haveInfo() {
		return p.t.numPieces()
	}
	return p.peerMinPieces
}

func (p *Peer) completedString() string {
	have := pieceIndex(p.peerPieces().GetCardinality())
	if all, _ := p.peerHasAllPieces(); all {
		have = p.bestPeerNumPieces()
	}
	return fmt.Sprintf("%d/%d", have, p.bestPeerNumPieces())
}

func (pc *PeerConn) onGotInfo(info *metainfo.Info) {
	pc.setNumPieces(info.NumPieces())
}

// Correct the PeerPieces slice length. Return false if the existing slice is invalid, such as by
// receiving badly sized BITFIELD, or invalid HAVE messages.
func (pc *PeerConn) setNumPieces(num pieceIndex) {
	pc._peerPieces.RemoveRange(bitmap.BitRange(num), bitmap.ToEnd)
	pc.peerPiecesChanged()
}

func (pc *PeerConn) peerPieces() *roaring.Bitmap {
	return &pc._peerPieces
}

func eventAgeString(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return fmt.Sprintf("%.2fs ago", time.Since(t).Seconds())
}

func (pc *PeerConn) connectionFlags() (ret string) {
	c := func(b byte) {
		ret += string([]byte{b})
	}
	if pc.cryptoMethod == mse.CryptoMethodRC4 {
		c('E')
	} else if pc.headerEncrypted {
		c('e')
	}
	ret += string(pc.Discovery)
	if pc.utp() {
		c('U')
	}
	return
}

func (pc *PeerConn) utp() bool {
	return parseNetworkString(pc.Network).Udp
}

// Inspired by https://github.com/transmission/transmission/wiki/Peer-Status-Text.
func (p *Peer) statusFlags() (ret string) {
	c := func(b byte) {
		ret += string([]byte{b})
	}
	if p.requestState.Interested {
		c('i')
	}
	if p.choking {
		c('c')
	}
	c('-')
	ret += p.connectionFlags()
	c('-')
	if p.peerInterested {
		c('i')
	}
	if p.peerChoking {
		c('c')
	}
	return
}

func (p *Peer) downloadRate() float64 {
	num := p._stats.BytesReadUsefulData.Int64()
	if num == 0 {
		return 0
	}
	return float64(num) / p.totalExpectingTime().Seconds()
}

func (p *Peer) DownloadRate() float64 {
	p.locker().RLock()
	defer p.locker().RUnlock()

	return p.downloadRate()
}

func (p *Peer) iterContiguousPieceRequests(f func(piece pieceIndex, count int)) {
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
	p.requestState.Requests.Iterate(func(requestIndex request_strategy.RequestIndex) bool {
		next(Some(p.t.pieceIndexOfRequestIndex(requestIndex)))
		return true
	})
	next(None[pieceIndex]())
}

func (p *Peer) writeStatus(w io.Writer, t *Torrent) {
	// \t isn't preserved in <pre> blocks?
	if p.closed.IsSet() {
		fmt.Fprint(w, "CLOSED: ")
	}
	fmt.Fprintln(w, p.connStatusString())
	prio, err := p.peerPriority()
	prioStr := fmt.Sprintf("%08x", prio)
	if err != nil {
		prioStr += ": " + err.Error()
	}
	fmt.Fprintf(w, "    bep40-prio: %v\n", prioStr)
	fmt.Fprintf(w, "    last msg: %s, connected: %s, last helpful: %s, itime: %s, etime: %s\n",
		eventAgeString(p.lastMessageReceived),
		eventAgeString(p.completedHandshake),
		eventAgeString(p.lastHelpful()),
		p.cumInterest(),
		p.totalExpectingTime(),
	)
	fmt.Fprintf(w,
		"    %s completed, %d pieces touched, good chunks: %v/%v:%v reqq: %d+%v/(%d/%d):%d/%d, flags: %s, dr: %.1f KiB/s\n",
		p.completedString(),
		len(p.peerTouchedPieces),
		&p._stats.ChunksReadUseful,
		&p._stats.ChunksRead,
		&p._stats.ChunksWritten,
		p.requestState.Requests.GetCardinality(),
		p.requestState.Cancelled.GetCardinality(),
		p.nominalMaxRequests(),
		p.PeerMaxRequests,
		len(p.peerRequests),
		localClientReqq,
		p.statusFlags(),
		p.downloadRate()/(1<<10),
	)
	fmt.Fprintf(w, "    requested pieces:")
	p.iterContiguousPieceRequests(func(piece pieceIndex, count int) {
		fmt.Fprintf(w, " %v(%v)", piece, count)
	})
	fmt.Fprintf(w, "\n")
}

func (p *Peer) close() {
	if !p.closed.Set() {
		return
	}
	if p.updateRequestsTimer != nil {
		p.updateRequestsTimer.Stop()
	}
	p.peerImpl.onClose()
	if p.t != nil {
		p.t.decPeerPieceAvailability(p)
	}
	for _, f := range p.callbacks.PeerClosed {
		f(p)
	}
}

func (pc *PeerConn) onClose() {
	if pc.pex.IsEnabled() {
		pc.pex.Close()
	}
	pc.tickleWriter()
	if pc.conn != nil {
		go pc.conn.Close()
	}
	if cb := pc.callbacks.PeerConnClosed; cb != nil {
		cb(pc)
	}
}

// Peer definitely has a piece, for purposes of requesting. So it's not sufficient that we think
// they do (known=true).
func (p *Peer) peerHasPiece(piece pieceIndex) bool {
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
)

// Writes a message into the write buffer. Returns whether it's okay to keep writing. Writing is
// done asynchronously, so it may be that we're not able to honour backpressure from this method.
func (pc *PeerConn) write(msg pp.Message) bool {
	torrent.Add(fmt.Sprintf("messages written of type %s", msg.Type.String()), 1)
	// We don't need to track bytes here because the connection's Writer has that behaviour injected
	// (although there's some delay between us buffering the message, and the connection writer
	// flushing it out.).
	notFull := pc.messageWriter.write(msg)
	// Last I checked only Piece messages affect stats, and we don't write those.
	pc.wroteMsg(&msg)
	pc.tickleWriter()
	return notFull
}

func (pc *PeerConn) requestMetadataPiece(index int) {
	eID := pc.PeerExtensionIDs[pp.ExtensionNameMetadata]
	if eID == pp.ExtensionDeleteNumber {
		return
	}
	if index < len(pc.metadataRequests) && pc.metadataRequests[index] {
		return
	}
	pc.logger.WithDefaultLevel(log.Debug).Printf("requesting metadata piece %d", index)
	pc.write(pp.MetadataExtensionRequestMsg(eID, index))
	for index >= len(pc.metadataRequests) {
		pc.metadataRequests = append(pc.metadataRequests, false)
	}
	pc.metadataRequests[index] = true
}

func (pc *PeerConn) requestedMetadataPiece(index int) bool {
	return index < len(pc.metadataRequests) && pc.metadataRequests[index]
}

var (
	interestedMsgLen = len(pp.Message{Type: pp.Interested}.MustMarshalBinary())
	requestMsgLen    = len(pp.Message{Type: pp.Request}.MustMarshalBinary())
	// This is the maximum request count that could fit in the write buffer if it's at or below the
	// low water mark when we run maybeUpdateActualRequestState.
	maxLocalToRemoteRequests = (writeBufferHighWaterLen - writeBufferLowWaterLen - interestedMsgLen) / requestMsgLen
)

// The actual value to use as the maximum outbound requests.
func (p *Peer) nominalMaxRequests() maxRequests {
	return maxInt(1, minInt(p.PeerMaxRequests, p.peakRequests*2, maxLocalToRemoteRequests))
}

func (p *Peer) totalExpectingTime() (ret time.Duration) {
	ret = p.cumulativeExpectedToReceiveChunks
	if !p.lastStartedExpectingToReceiveChunks.IsZero() {
		ret += time.Since(p.lastStartedExpectingToReceiveChunks)
	}
	return
}

func (pc *PeerConn) onPeerSentCancel(r Request) {
	if _, ok := pc.peerRequests[r]; !ok {
		torrent.Add("unexpected cancels received", 1)
		return
	}
	if pc.fastEnabled() {
		pc.reject(r)
	} else {
		delete(pc.peerRequests, r)
	}
}

func (pc *PeerConn) choke(msg messageWriter) (more bool) {
	if pc.choking {
		return true
	}
	pc.choking = true
	more = msg(pp.Message{
		Type: pp.Choke,
	})
	if !pc.fastEnabled() {
		pc.peerRequests = nil
	}
	return
}

func (pc *PeerConn) unchoke(msg func(pp.Message) bool) bool {
	if !pc.choking {
		return true
	}
	pc.choking = false
	return msg(pp.Message{
		Type: pp.Unchoke,
	})
}

func (p *Peer) setInterested(interested bool) bool {
	if p.requestState.Interested == interested {
		return true
	}
	p.requestState.Interested = interested
	if interested {
		p.lastBecameInterested = time.Now()
	} else if !p.lastBecameInterested.IsZero() {
		p.priorInterest += time.Since(p.lastBecameInterested)
	}
	p.updateExpectingChunks()
	// log.Printf("%p: setting interest: %v", cn, interested)
	return p.writeInterested(interested)
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

// The function takes a message to be sent, and returns true if more messages
// are okay.
type messageWriter func(pp.Message) bool

// This function seems to only used by Peer.request. It's all logic checks, so maybe we can no-op it
// when we want to go fast.
func (p *Peer) shouldRequest(r RequestIndex) error {
	pi := p.t.pieceIndexOfRequestIndex(r)
	if p.requestState.Cancelled.Contains(r) {
		return errors.New("request is cancelled and waiting acknowledgement")
	}
	if !p.peerHasPiece(pi) {
		return errors.New("requesting piece peer doesn't have")
	}
	if !p.t.peerIsActive(p) {
		panic("requesting but not in active conns")
	}
	if p.closed.IsSet() {
		panic("requesting when connection is closed")
	}
	if p.t.hashingPiece(pi) {
		panic("piece is being hashed")
	}
	if p.t.pieceQueuedForHash(pi) {
		panic("piece is queued for hash")
	}
	if p.peerChoking && !p.peerAllowedFast.Contains(pi) {
		// This could occur if we made a request with the fast extension, and then got choked and
		// haven't had the request rejected yet.
		if !p.requestState.Requests.Contains(r) {
			panic("peer choking and piece not allowed fast")
		}
	}
	return nil
}

func (p *Peer) mustRequest(r RequestIndex) bool {
	more, err := p.request(r)
	if err != nil {
		panic(err)
	}
	return more
}

func (p *Peer) request(r RequestIndex) (more bool, err error) {
	if err := p.shouldRequest(r); err != nil {
		panic(err)
	}
	if p.requestState.Requests.Contains(r) {
		return true, nil
	}
	if maxRequests(p.requestState.Requests.GetCardinality()) >= p.nominalMaxRequests() {
		return true, errors.New("too many outstanding requests")
	}
	p.requestState.Requests.Add(r)
	if p.validReceiveChunks == nil {
		p.validReceiveChunks = make(map[RequestIndex]int)
	}
	p.validReceiveChunks[r]++
	p.t.requestState[r] = requestState{
		peer: p,
		when: time.Now(),
	}
	p.updateExpectingChunks()
	ppReq := p.t.requestIndexToRequest(r)
	for _, f := range p.callbacks.SentRequest {
		f(PeerRequestEvent{p, ppReq})
	}
	return p.peerImpl._request(ppReq), nil
}

func (pc *PeerConn) _request(r Request) bool {
	return pc.write(pp.Message{
		Type:   pp.Request,
		Index:  r.Index,
		Begin:  r.Begin,
		Length: r.Length,
	})
}

func (p *Peer) cancel(r RequestIndex) {
	if !p.deleteRequest(r) {
		panic("request not existing should have been guarded")
	}
	if p._cancel(r) {
		if !p.requestState.Cancelled.CheckedAdd(r) {
			panic("request already cancelled")
		}
	}
	p.decPeakRequests()
	if p.isLowOnRequests() {
		p.updateRequests("Peer.cancel")
	}
}

func (pc *PeerConn) _cancel(r RequestIndex) bool {
	pc.write(makeCancelMessage(pc.t.requestIndexToRequest(r)))
	// Transmission does not send rejects for received cancels. See
	// https://github.com/transmission/transmission/pull/2275.
	return pc.fastEnabled() && !pc.remoteIsTransmission()
}

func (pc *PeerConn) fillWriteBuffer() {
	if pc.messageWriter.writeBuffer.Len() > writeBufferLowWaterLen {
		// Fully committing to our max requests requires sufficient space (see
		// maxLocalToRemoteRequests). Flush what we have instead. We also prefer always to make
		// requests than to do PEX or upload, so we short-circuit before handling those. Any update
		// request reason will not be cleared, so we'll come right back here when there's space. We
		// can't do this in maybeUpdateActualRequestState because it's a method on Peer and has no
		// knowledge of write buffers.
	}
	pc.maybeUpdateActualRequestState()
	if pc.pex.IsEnabled() {
		if flow := pc.pex.Share(pc.write); !flow {
			return
		}
	}
	pc.upload(pc.write)
}

func (pc *PeerConn) have(piece pieceIndex) {
	if pc.sentHaves.Get(bitmap.BitIndex(piece)) {
		return
	}
	pc.write(pp.Message{
		Type:  pp.Have,
		Index: pp.Integer(piece),
	})
	pc.sentHaves.Add(bitmap.BitIndex(piece))
}

func (pc *PeerConn) postBitfield() {
	if pc.sentHaves.Len() != 0 {
		panic("bitfield must be first have-related message sent")
	}
	if !pc.t.haveAnyPieces() {
		return
	}
	pc.write(pp.Message{
		Type:     pp.Bitfield,
		Bitfield: pc.t.bitfield(),
	})
	pc.sentHaves = bitmap.Bitmap{pc.t._completedPieces.Clone()}
}

// Sets a reason to update requests, and if there wasn't already one, handle it.
func (p *Peer) updateRequests(reason string) {
	if p.needRequestUpdate != "" {
		return
	}
	if reason != peerUpdateRequestsTimerReason && !p.isLowOnRequests() {
		return
	}
	p.needRequestUpdate = reason
	p.handleUpdateRequests()
}

func (pc *PeerConn) handleUpdateRequests() {
	// The writer determines the request state as needed when it can write.
	pc.tickleWriter()
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

func (p *Peer) peerPiecesChanged() {
	p.t.maybeDropMutuallyCompletePeer(p)
}

func (pc *PeerConn) raisePeerMinPieces(newMin pieceIndex) {
	if newMin > pc.peerMinPieces {
		pc.peerMinPieces = newMin
	}
}

func (pc *PeerConn) peerSentHave(piece pieceIndex) error {
	if pc.t.haveInfo() && piece >= pc.t.numPieces() || piece < 0 {
		return errors.New("invalid piece")
	}
	if pc.peerHasPiece(piece) {
		return nil
	}
	pc.raisePeerMinPieces(piece + 1)
	if !pc.peerHasPiece(piece) {
		pc.t.incPieceAvailability(piece)
	}
	pc._peerPieces.Add(uint32(piece))
	if pc.t.wantPieceIndex(piece) {
		pc.updateRequests("have")
	}
	pc.peerPiecesChanged()
	return nil
}

func (pc *PeerConn) peerSentBitfield(bf []bool) error {
	if len(bf)%8 != 0 {
		panic("expected bitfield length divisible by 8")
	}
	// We know that the last byte means that at most the last 7 bits are wasted.
	pc.raisePeerMinPieces(pieceIndex(len(bf) - 7))
	if pc.t.haveInfo() && len(bf) > int(pc.t.numPieces()) {
		// Ignore known excess pieces.
		bf = bf[:pc.t.numPieces()]
	}
	bm := boolSliceToBitmap(bf)
	if pc.t.haveInfo() && pieceIndex(bm.GetCardinality()) == pc.t.numPieces() {
		pc.onPeerHasAllPieces()
		return nil
	}
	if !bm.IsEmpty() {
		pc.raisePeerMinPieces(pieceIndex(bm.Maximum()) + 1)
	}
	shouldUpdateRequests := false
	if pc.peerSentHaveAll {
		if !pc.t.deleteConnWithAllPieces(&pc.Peer) {
			panic(pc)
		}
		pc.peerSentHaveAll = false
		if !pc._peerPieces.IsEmpty() {
			panic("if peer has all, we expect no individual peer pieces to be set")
		}
	} else {
		bm.Xor(&pc._peerPieces)
	}
	pc.peerSentHaveAll = false
	// bm is now 'on' for pieces that are changing
	bm.Iterate(func(x uint32) bool {
		pi := pieceIndex(x)
		if pc._peerPieces.Contains(x) {
			// Then we must be losing this piece
			pc.t.decPieceAvailability(pi)
		} else {
			if !shouldUpdateRequests && pc.t.wantPieceIndex(pieceIndex(x)) {
				shouldUpdateRequests = true
			}
			// We must be gaining this piece
			pc.t.incPieceAvailability(pieceIndex(x))
		}
		return true
	})
	// Apply the changes. If we had everything previously, this should be empty, so xor is the same
	// as or.
	pc._peerPieces.Xor(&bm)
	if shouldUpdateRequests {
		pc.updateRequests("bitfield")
	}
	// We didn't guard this before, I see no reason to do it now.
	pc.peerPiecesChanged()
	return nil
}

func (pc *PeerConn) onPeerHasAllPieces() {
	t := pc.t
	if t.haveInfo() {
		pc._peerPieces.Iterate(func(x uint32) bool {
			t.decPieceAvailability(pieceIndex(x))
			return true
		})
	}
	t.addConnWithAllPieces(&pc.Peer)
	pc.peerSentHaveAll = true
	pc._peerPieces.Clear()
	if !pc.t._pendingPieces.IsEmpty() {
		pc.updateRequests("Peer.onPeerHasAllPieces")
	}
	pc.peerPiecesChanged()
}

func (pc *PeerConn) onPeerSentHaveAll() error {
	pc.onPeerHasAllPieces()
	return nil
}

func (pc *PeerConn) peerSentHaveNone() error {
	if pc.peerSentHaveAll {
		pc.t.decPeerPieceAvailability(&pc.Peer)
	}
	pc._peerPieces.Clear()
	pc.peerSentHaveAll = false
	pc.peerPiecesChanged()
	return nil
}

func (pc *PeerConn) requestPendingMetadata() {
	if pc.t.haveInfo() {
		return
	}
	if pc.PeerExtensionIDs[pp.ExtensionNameMetadata] == 0 {
		// Peer doesn't support this.
		return
	}
	// Request metadata pieces that we don't have in a random order.
	var pending []int
	for index := 0; index < pc.t.metadataPieceCount(); index++ {
		if !pc.t.haveMetadataPiece(index) && !pc.requestedMetadataPiece(index) {
			pending = append(pending, index)
		}
	}
	rand.Shuffle(len(pending), func(i, j int) { pending[i], pending[j] = pending[j], pending[i] })
	for _, i := range pending {
		pc.requestMetadataPiece(i)
	}
}

func (pc *PeerConn) wroteMsg(msg *pp.Message) {
	torrent.Add(fmt.Sprintf("messages written of type %s", msg.Type.String()), 1)
	if msg.Type == pp.Extended {
		for name, id := range pc.PeerExtensionIDs {
			if id != msg.ExtendedID {
				continue
			}
			torrent.Add(fmt.Sprintf("Extended messages written for protocol %q", name), 1)
		}
	}
	pc.allStats(func(cs *ConnStats) { cs.wroteMsg(msg) })
}

// After handshake, we know what Torrent and Client stats to include for a
// connection.
func (p *Peer) postHandshakeStats(f func(*ConnStats)) {
	t := p.t
	f(&t.stats)
	f(&t.cl.stats)
}

// All ConnStats that include this connection. Some objects are not known
// until the handshake is complete, after which it's expected to reconcile the
// differences.
func (p *Peer) allStats(f func(*ConnStats)) {
	f(&p._stats)
	if p.reconciledHandshakeStats {
		p.postHandshakeStats(f)
	}
}

func (pc *PeerConn) wroteBytes(n int64) {
	pc.allStats(add(n, func(cs *ConnStats) *Count { return &cs.BytesWritten }))
}

func (p *Peer) readBytes(n int64) {
	p.allStats(add(n, func(cs *ConnStats) *Count { return &cs.BytesRead }))
}

// Returns whether the connection could be useful to us. We're seeding and
// they want data, we don't have metainfo and they can provide it, etc.
func (p *Peer) useful() bool {
	t := p.t
	if p.closed.IsSet() {
		return false
	}
	if !t.haveInfo() {
		return p.supportsExtension("ut_metadata")
	}
	if t.seeding() && p.peerInterested {
		return true
	}
	if p.peerHasWantedPieces() {
		return true
	}
	return false
}

func (p *Peer) lastHelpful() (ret time.Time) {
	ret = p.lastUsefulChunkReceived
	if p.t.seeding() && p.lastChunkSent.After(ret) {
		ret = p.lastChunkSent
	}
	return
}

func (pc *PeerConn) fastEnabled() bool {
	return pc.PeerExtensionBytes.SupportsFast() && pc.t.cl.config.Extensions.SupportsFast()
}

func (pc *PeerConn) reject(r Request) {
	if !pc.fastEnabled() {
		panic("fast not enabled")
	}
	pc.write(r.ToMsg(pp.Reject))
	delete(pc.peerRequests, r)
}

func (pc *PeerConn) maximumPeerRequestChunkLength() (_ Option[int]) {
	uploadRateLimiter := pc.t.cl.config.UploadRateLimiter
	if uploadRateLimiter.Limit() == rate.Inf {
		return
	}
	return Some(uploadRateLimiter.Burst())
}

// startFetch is for testing purposes currently.
func (pc *PeerConn) onReadRequest(r Request, startFetch bool) error {
	requestedChunkLengths.Add(strconv.FormatUint(r.Length.Uint64(), 10), 1)
	if _, ok := pc.peerRequests[r]; ok {
		torrent.Add("duplicate requests received", 1)
		if pc.fastEnabled() {
			return errors.New("received duplicate request with fast enabled")
		}
		return nil
	}
	if pc.choking {
		torrent.Add("requests received while choking", 1)
		if pc.fastEnabled() {
			torrent.Add("requests rejected while choking", 1)
			pc.reject(r)
		}
		return nil
	}
	// TODO: What if they've already requested this?
	if len(pc.peerRequests) >= localClientReqq {
		torrent.Add("requests received while queue full", 1)
		if pc.fastEnabled() {
			pc.reject(r)
		}
		// BEP 6 says we may close here if we choose.
		return nil
	}
	if opt := pc.maximumPeerRequestChunkLength(); opt.Ok && int(r.Length) > opt.Value {
		err := fmt.Errorf("peer requested chunk too long (%v)", r.Length)
		pc.logger.Levelf(log.Warning, err.Error())
		if pc.fastEnabled() {
			pc.reject(r)
			return nil
		} else {
			return err
		}
	}
	if !pc.t.havePiece(pieceIndex(r.Index)) {
		// TODO: Tell the peer we don't have the piece, and reject this request.
		requestsReceivedForMissingPieces.Add(1)
		return fmt.Errorf("peer requested piece we don't have: %v", r.Index.Int())
	}
	// Check this after we know we have the piece, so that the piece length will be known.
	if r.Begin+r.Length > pc.t.pieceLength(pieceIndex(r.Index)) {
		torrent.Add("bad requests received", 1)
		return errors.New("bad Request")
	}
	if pc.peerRequests == nil {
		pc.peerRequests = make(map[Request]*peerRequestState, localClientReqq)
	}
	value := &peerRequestState{}
	pc.peerRequests[r] = value
	if startFetch {
		// TODO: Limit peer request data read concurrency.
		go pc.peerRequestDataReader(r, value)
	}
	return nil
}

func (pc *PeerConn) peerRequestDataReader(r Request, prs *peerRequestState) {
	b, err := readPeerRequestData(r, pc)
	pc.locker().Lock()
	defer pc.locker().Unlock()
	if err != nil {
		pc.peerRequestDataReadFailed(err, r)
	} else {
		if b == nil {
			panic("data must be non-nil to trigger send")
		}
		torrent.Add("peer request data read successes", 1)
		prs.data = b
		// This might be required for the error case too (#752 and #753).
		pc.tickleWriter()
	}
}

// If this is maintained correctly, we might be able to support optional synchronous reading for
// chunk sending, the way it used to work.
func (pc *PeerConn) peerRequestDataReadFailed(err error, r Request) {
	torrent.Add("peer request data read failures", 1)
	logLevel := log.Warning
	if pc.t.hasStorageCap() {
		// It's expected that pieces might drop. See
		// https://github.com/anacrolix/torrent/issues/702#issuecomment-1000953313.
		logLevel = log.Debug
	}
	pc.logger.WithDefaultLevel(logLevel).Printf("error reading chunk for peer Request %v: %v", r, err)
	if pc.t.closed.IsSet() {
		return
	}
	i := pieceIndex(r.Index)
	if pc.t.pieceComplete(i) {
		// There used to be more code here that just duplicated the following break. Piece
		// completions are currently cached, so I'm not sure how helpful this update is, except to
		// pull any completion changes pushed to the storage backend in failed reads that got us
		// here.
		pc.t.updatePieceCompletion(i)
	}
	// We've probably dropped a piece from storage, but there's no way to communicate this to the
	// peer. If they ask for it again, we kick them allowing us to send them updated piece states if
	// we reconnect. TODO: Instead, we could just try to update them with Bitfield or HaveNone and
	// if they kick us for breaking protocol, on reconnect we will be compliant again (at least
	// initially).
	if pc.fastEnabled() {
		pc.reject(r)
	} else {
		if pc.choking {
			// If fast isn't enabled, I think we would have wiped all peer requests when we last
			// choked, and requests while we're choking would be ignored. It could be possible that
			// a peer request data read completed concurrently to it being deleted elsewhere.
			pc.logger.WithDefaultLevel(log.Warning).Printf("already choking peer, requests might not be rejected correctly")
		}
		// Choking a non-fast peer should cause them to flush all their requests.
		pc.choke(pc.write)
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

func (pc *PeerConn) logProtocolBehaviour(level log.Level, format string, arg ...interface{}) {
	pc.logger.WithContextText(fmt.Sprintf(
		"peer id %q, ext v %q", pc.PeerID, pc.PeerClientName.Load(),
	)).SkipCallers(1).Levelf(level, format, arg...)
}

// Processes incoming BitTorrent wire-protocol messages. The client lock is held upon entry and
// exit. Returning will end the connection.
func (pc *PeerConn) mainReadLoop() (err error) {
	defer func() {
		if err != nil {
			torrent.Add("connection.mainReadLoop returned with error", 1)
		} else {
			torrent.Add("connection.mainReadLoop returned with no error", 1)
		}
	}()
	t := pc.t
	cl := t.cl

	decoder := pp.Decoder{
		R:         bufio.NewReaderSize(pc.r, 1<<17),
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
		if cb := pc.callbacks.ReadMessage; cb != nil && err == nil {
			cb(pc, &msg)
		}
		if t.closed.IsSet() || pc.closed.IsSet() {
			return nil
		}
		if err != nil {
			return err
		}
		pc.lastMessageReceived = time.Now()
		if msg.Keepalive {
			receivedKeepalives.Add(1)
			continue
		}
		messageTypesReceived.Add(msg.Type.String(), 1)
		if msg.Type.FastExtension() && !pc.fastEnabled() {
			runSafeExtraneous(func() { torrent.Add("fast messages received when extension is disabled", 1) })
			return fmt.Errorf("received fast extension message (type=%v) but extension is disabled", msg.Type)
		}
		switch msg.Type {
		case pp.Choke:
			if pc.peerChoking {
				break
			}
			if !pc.fastEnabled() {
				pc.deleteAllRequests("choked by non-fast PeerConn")
			} else {
				// We don't decrement pending requests here, let's wait for the peer to either
				// reject or satisfy the outstanding requests. Additionally, some peers may unchoke
				// us and resume where they left off, we don't want to have piled on to those chunks
				// in the meanwhile. I think a peer's ability to abuse this should be limited: they
				// could let us request a lot of stuff, then choke us and never reject, but they're
				// only a single peer, our chunk balancing should smooth over this abuse.
			}
			pc.peerChoking = true
			pc.updateExpectingChunks()
		case pp.Unchoke:
			if !pc.peerChoking {
				// Some clients do this for some reason. Transmission doesn't error on this, so we
				// won't for consistency.
				pc.logProtocolBehaviour(log.Debug, "received unchoke when already unchoked")
				break
			}
			pc.peerChoking = false
			preservedCount := 0
			pc.requestState.Requests.Iterate(func(x RequestIndex) bool {
				if !pc.peerAllowedFast.Contains(pc.t.pieceIndexOfRequestIndex(x)) {
					preservedCount++
				}
				return true
			})
			if preservedCount != 0 {
				// TODO: Yes this is a debug log but I'm not happy with the state of the logging lib
				// right now.
				pc.logger.Levelf(log.Debug,
					"%v requests were preserved while being choked (fast=%v)",
					preservedCount,
					pc.fastEnabled())

				torrent.Add("requestsPreservedThroughChoking", int64(preservedCount))
			}
			if !pc.t._pendingPieces.IsEmpty() {
				pc.updateRequests("unchoked")
			}
			pc.updateExpectingChunks()
		case pp.Interested:
			pc.peerInterested = true
			pc.tickleWriter()
		case pp.NotInterested:
			pc.peerInterested = false
			// We don't clear their requests since it isn't clear in the spec.
			// We'll probably choke them for this, which will clear them if
			// appropriate, and is clearly specified.
		case pp.Have:
			err = pc.peerSentHave(pieceIndex(msg.Index))
		case pp.Bitfield:
			err = pc.peerSentBitfield(msg.Bitfield)
		case pp.Request:
			r := newRequestFromMessage(&msg)
			err = pc.onReadRequest(r, true)
		case pp.Piece:
			pc.doChunkReadStats(int64(len(msg.Piece)))
			err = pc.receiveChunk(&msg)
			if len(msg.Piece) == int(t.chunkSize) {
				t.chunkPool.Put(&msg.Piece)
			}
			if err != nil {
				err = fmt.Errorf("receiving chunk: %w", err)
			}
		case pp.Cancel:
			req := newRequestFromMessage(&msg)
			pc.onPeerSentCancel(req)
		case pp.Port:
			ipa, ok := tryIpPortFromNetAddr(pc.RemoteAddr)
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
			log.Fmsg("peer suggested piece %d", msg.Index).AddValues(pc, msg.Index).LogLevel(log.Debug, pc.t.logger)
			pc.updateRequests("suggested")
		case pp.HaveAll:
			err = pc.onPeerSentHaveAll()
		case pp.HaveNone:
			err = pc.peerSentHaveNone()
		case pp.Reject:
			req := newRequestFromMessage(&msg)
			if !pc.remoteRejectedRequest(pc.t.requestIndexFromRequest(req)) {
				pc.logger.Printf("received invalid reject [request=%v, peer=%v]", req, pc)
				err = fmt.Errorf("received invalid reject [request=%v]", req)
			}
		case pp.AllowedFast:
			torrent.Add("allowed fasts received", 1)
			log.Fmsg("peer allowed fast: %d", msg.Index).AddValues(pc).LogLevel(log.Debug, pc.t.logger)
			pc.updateRequests("PeerConn.mainReadLoop allowed fast")
		case pp.Extended:
			err = pc.onReadExtendedMsg(msg.ExtendedID, msg.ExtendedPayload)
		default:
			err = fmt.Errorf("received unknown message type: %#v", msg.Type)
		}
		if err != nil {
			return err
		}
	}
}

// Returns true if it was valid to reject the request.
func (p *Peer) remoteRejectedRequest(r RequestIndex) bool {
	if p.deleteRequest(r) {
		p.decPeakRequests()
	} else if !p.requestState.Cancelled.CheckedRemove(r) {
		return false
	}
	if p.isLowOnRequests() {
		p.updateRequests("Peer.remoteRejectedRequest")
	}
	p.decExpectedChunkReceive(r)
	return true
}

func (p *Peer) decExpectedChunkReceive(r RequestIndex) {
	count := p.validReceiveChunks[r]
	if count == 1 {
		delete(p.validReceiveChunks, r)
	} else if count > 1 {
		p.validReceiveChunks[r] = count - 1
	} else {
		panic(r)
	}
}

func (pc *PeerConn) onReadExtendedMsg(id pp.ExtensionNumber, payload []byte) (err error) {
	defer func() {
		// TODO: Should we still do this?
		if err != nil {
			// These clients use their own extension IDs for outgoing message
			// types, which is incorrect.
			if bytes.HasPrefix(pc.PeerID[:], []byte("-SD0100-")) || strings.HasPrefix(string(pc.PeerID[:]), "-XL0012-") {
				err = nil
			}
		}
	}()
	t := pc.t
	cl := t.cl
	switch id {
	case pp.HandshakeExtendedID:
		var d pp.ExtendedHandshakeMessage
		if err := bencode.Unmarshal(payload, &d); err != nil {
			pc.logger.Printf("error parsing extended handshake message %q: %s", payload, err)
			return fmt.Errorf("unmarshalling extended handshake payload: %w", err)
		}
		if cb := pc.callbacks.ReadExtendedHandshake; cb != nil {
			cb(pc, &d)
		}
		// c.logger.WithDefaultLevel(log.Debug).Printf("received extended handshake message:\n%s", spew.Sdump(d))
		if d.Reqq != 0 {
			pc.PeerMaxRequests = d.Reqq
		}
		pc.PeerClientName.Store(d.V)
		if pc.PeerExtensionIDs == nil {
			pc.PeerExtensionIDs = make(map[pp.ExtensionName]pp.ExtensionNumber, len(d.M))
		}
		pc.PeerListenPort = d.Port
		pc.PeerPrefersEncryption = d.Encryption
		for name, id := range d.M {
			if _, ok := pc.PeerExtensionIDs[name]; !ok {
				peersSupportingExtension.Add(
					// expvar.Var.String must produce valid JSON. "ut_payme\xeet_address" was being
					// entered here which caused problems later when unmarshalling.
					strconv.Quote(string(name)),
					1)
			}
			pc.PeerExtensionIDs[name] = id
		}
		if d.MetadataSize != 0 {
			if err = t.setMetadataSize(d.MetadataSize); err != nil {
				return fmt.Errorf("setting metadata size to %d: %w", d.MetadataSize, err)
			}
		}
		pc.requestPendingMetadata()
		if !t.cl.config.DisablePEX {
			t.pex.Add(pc) // we learnt enough now
			pc.pex.Init(pc)
		}
		return nil
	case metadataExtendedId:
		err := cl.gotMetadataExtensionMsg(payload, t, pc)
		if err != nil {
			return fmt.Errorf("handling metadata extension message: %w", err)
		}
		return nil
	case pexExtendedId:
		if !pc.pex.IsEnabled() {
			return nil // or hang-up maybe?
		}
		return pc.pex.Recv(payload)
	default:
		return fmt.Errorf("unexpected extended message ID: %v", id)
	}
}

// Set both the Reader and Writer for the connection from a single ReadWriter.
func (pc *PeerConn) setRW(rw io.ReadWriter) {
	pc.r = rw
	pc.w = rw
}

// Returns the Reader and Writer as a combined ReadWriter.
func (pc *PeerConn) rw() io.ReadWriter {
	return struct {
		io.Reader
		io.Writer
	}{pc.r, pc.w}
}

func (p *Peer) doChunkReadStats(size int64) {
	p.allStats(func(cs *ConnStats) { cs.receivedChunk(size) })
}

// Handle a received chunk from a peer.
func (p *Peer) receiveChunk(msg *pp.Message) error {
	chunksReceived.Add("total", 1)

	ppReq := newRequestFromMessage(msg)
	req := p.t.requestIndexFromRequest(ppReq)
	t := p.t

	if p.bannableAddr.Ok {
		t.smartBanCache.RecordBlock(p.bannableAddr.Value, req, msg.Piece)
	}

	if p.peerChoking {
		chunksReceived.Add("while choked", 1)
	}

	if p.validReceiveChunks[req] <= 0 {
		chunksReceived.Add("unexpected", 1)
		return errors.New("received unexpected chunk")
	}
	p.decExpectedChunkReceive(req)

	if p.peerChoking && p.peerAllowedFast.Contains(pieceIndex(ppReq.Index)) {
		chunksReceived.Add("due to allowed fast", 1)
	}

	// The request needs to be deleted immediately to prevent cancels occurring asynchronously when
	// have actually already received the piece, while we have the Client unlocked to write the data
	// out.
	intended := false
	{
		if p.requestState.Requests.Contains(req) {
			for _, f := range p.callbacks.ReceivedRequested {
				f(PeerMessageEvent{p, msg})
			}
		}
		// Request has been satisfied.
		if p.deleteRequest(req) || p.requestState.Cancelled.CheckedRemove(req) {
			intended = true
			if !p.peerChoking {
				p._chunksReceivedWhileExpecting++
			}
			if p.isLowOnRequests() {
				p.updateRequests("Peer.receiveChunk deleted request")
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
		p.allStats(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadWasted }))
		return nil
	}

	piece := &t.pieces[ppReq.Index]

	p.allStats(add(1, func(cs *ConnStats) *Count { return &cs.ChunksReadUseful }))
	p.allStats(add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulData }))
	if intended {
		p.piecesReceivedSinceLastRequestUpdate++
		p.allStats(add(int64(len(msg.Piece)), func(cs *ConnStats) *Count { return &cs.BytesReadUsefulIntendedData }))
	}
	for _, f := range p.t.cl.config.Callbacks.ReceivedUsefulData {
		f(ReceivedUsefulDataEvent{p, msg})
	}
	p.lastUsefulChunkReceived = time.Now()

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
		p.logger.WithDefaultLevel(log.Error).Printf("writing received chunk %v: %v", req, err)
		t.pendRequest(req)
		// Necessary to pass TestReceiveChunkStorageFailureSeederFastExtensionDisabled. I think a
		// request update runs while we're writing the chunk that just failed. Then we never do a
		// fresh update after pending the failed request.
		p.updateRequests("Peer.receiveChunk error writing chunk")
		t.onWriteChunkErr(err)
		return nil
	}

	p.onDirtiedPiece(pieceIndex(ppReq.Index))

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

func (p *Peer) onDirtiedPiece(piece pieceIndex) {
	if p.peerTouchedPieces == nil {
		p.peerTouchedPieces = make(map[pieceIndex]struct{})
	}
	p.peerTouchedPieces[piece] = struct{}{}
	ds := &p.t.pieces[piece].dirtiers
	if *ds == nil {
		*ds = make(map[*Peer]struct{})
	}
	(*ds)[p] = struct{}{}
}

func (pc *PeerConn) uploadAllowed() bool {
	if pc.t.cl.config.NoUpload {
		return false
	}
	if pc.t.dataUploadDisallowed {
		return false
	}
	if pc.t.seeding() {
		return true
	}
	if !pc.peerHasWantedPieces() {
		return false
	}
	// Don't upload more than 100 KiB more than we download.
	if pc._stats.BytesWrittenData.Int64() >= pc._stats.BytesReadData.Int64()+100<<10 {
		return false
	}
	return true
}

func (pc *PeerConn) setRetryUploadTimer(delay time.Duration) {
	if pc.uploadTimer == nil {
		pc.uploadTimer = time.AfterFunc(delay, pc.tickleWriter)
	} else {
		pc.uploadTimer.Reset(delay)
	}
}

// Also handles choking and unchoking of the remote peer.
func (pc *PeerConn) upload(msg func(pp.Message) bool) bool {
	// Breaking or completing this loop means we don't want to upload to the
	// peer anymore, and we choke them.
another:
	for pc.uploadAllowed() {
		// We want to upload to the peer.
		if !pc.unchoke(msg) {
			return false
		}
		for r, state := range pc.peerRequests {
			if state.data == nil {
				continue
			}
			res := pc.t.cl.config.UploadRateLimiter.ReserveN(time.Now(), int(r.Length))
			if !res.OK() {
				panic(fmt.Sprintf("upload rate limiter burst size < %d", r.Length))
			}
			delay := res.Delay()
			if delay > 0 {
				res.Cancel()
				pc.setRetryUploadTimer(delay)
				// Hard to say what to return here.
				return true
			}
			more := pc.sendChunk(r, msg, state)
			delete(pc.peerRequests, r)
			if !more {
				return false
			}
			goto another
		}
		return true
	}
	return pc.choke(msg)
}

func (pc *PeerConn) drop() {
	pc.t.dropConnection(pc)
}

func (pc *PeerConn) ban() {
	pc.t.cl.banPeerIP(pc.remoteIp())
}

func (p *Peer) netGoodPiecesDirtied() int64 {
	return p._stats.PiecesDirtiedGood.Int64() - p._stats.PiecesDirtiedBad.Int64()
}

func (p *Peer) peerHasWantedPieces() bool {
	if all, _ := p.peerHasAllPieces(); all {
		return !p.t.haveAllPieces() && !p.t._pendingPieces.IsEmpty()
	}
	if !p.t.haveInfo() {
		return !p.peerPieces().IsEmpty()
	}
	return p.peerPieces().Intersects(&p.t._pendingPieces)
}

// Returns true if an outstanding request is removed. Cancelled requests should be handled
// separately.
func (p *Peer) deleteRequest(r RequestIndex) bool {
	if !p.requestState.Requests.CheckedRemove(r) {
		return false
	}
	for _, f := range p.callbacks.DeletedRequest {
		f(PeerRequestEvent{p, p.t.requestIndexToRequest(r)})
	}
	p.updateExpectingChunks()
	if p.t.requestingPeer(r) != p {
		panic("only one peer should have a given request at a time")
	}
	delete(p.t.requestState, r)
	// c.t.iterPeers(func(p *Peer) {
	// 	if p.isLowOnRequests() {
	// 		p.updateRequests("Peer.deleteRequest")
	// 	}
	// })
	return true
}

func (p *Peer) deleteAllRequests(reason string) {
	if p.requestState.Requests.IsEmpty() {
		return
	}
	p.requestState.Requests.IterateSnapshot(func(x RequestIndex) bool {
		if !p.deleteRequest(x) {
			panic("request should exist")
		}
		return true
	})
	p.assertNoRequests()
	p.t.iterPeers(func(p *Peer) {
		if p.isLowOnRequests() {
			p.updateRequests(reason)
		}
	})
	return
}

func (p *Peer) assertNoRequests() {
	if !p.requestState.Requests.IsEmpty() {
		panic(p.requestState.Requests.GetCardinality())
	}
}

func (p *Peer) cancelAllRequests() {
	p.requestState.Requests.IterateSnapshot(func(x RequestIndex) bool {
		p.cancel(x)
		return true
	})
	p.assertNoRequests()
	return
}

// This is called when something has changed that should wake the writer, such as putting stuff into
// the writeBuffer, or changing some state that the writer can act on.
func (pc *PeerConn) tickleWriter() {
	pc.messageWriter.writeCond.Broadcast()
}

func (pc *PeerConn) sendChunk(r Request, msg func(pp.Message) bool, state *peerRequestState) (more bool) {
	pc.lastChunkSent = time.Now()
	return msg(pp.Message{
		Type:  pp.Piece,
		Index: r.Index,
		Begin: r.Begin,
		Piece: state.data,
	})
}

func (pc *PeerConn) setTorrent(t *Torrent) {
	if pc.t != nil {
		panic("connection already associated with a torrent")
	}
	pc.t = t
	pc.logger.WithDefaultLevel(log.Debug).Printf("set torrent=%v", t)
	t.reconcileHandshakeStats(pc)
}

func (p *Peer) peerPriority() (peerPriority, error) {
	return bep40Priority(p.remoteIpPort(), p.localPublicAddr)
}

func (p *Peer) remoteIp() net.IP {
	host, _, _ := net.SplitHostPort(p.RemoteAddr.String())
	return net.ParseIP(host)
}

func (p *Peer) remoteIpPort() IpPort {
	ipa, _ := tryIpPortFromNetAddr(p.RemoteAddr)
	return IpPort{ipa.IP, uint16(ipa.Port)}
}

func (pc *PeerConn) pexPeerFlags() pp.PexPeerFlags {
	f := pp.PexPeerFlags(0)
	if pc.PeerPrefersEncryption {
		f |= pp.PexPrefersEncryption
	}
	if pc.outgoing {
		f |= pp.PexOutgoingConn
	}
	if pc.utp() {
		f |= pp.PexSupportsUtp
	}
	return f
}

// This returns the address to use if we want to dial the peer again. It incorporates the peer's
// advertised listen port.
func (pc *PeerConn) dialAddr() PeerRemoteAddr {
	if !pc.outgoing && pc.PeerListenPort != 0 {
		switch addr := pc.RemoteAddr.(type) {
		case *net.TCPAddr:
			dialAddr := *addr
			dialAddr.Port = pc.PeerListenPort
			return &dialAddr
		case *net.UDPAddr:
			dialAddr := *addr
			dialAddr.Port = pc.PeerListenPort
			return &dialAddr
		}
	}
	return pc.RemoteAddr
}

func (pc *PeerConn) pexEvent(t pexEventType) pexEvent {
	f := pc.pexPeerFlags()
	addr := pc.dialAddr()
	return pexEvent{t, addr, f, nil}
}

func (pc *PeerConn) String() string {
	return fmt.Sprintf("%T %p [id=%q, exts=%v, v=%q]", pc, pc, pc.PeerID, pc.PeerExtensionBytes, pc.PeerClientName.Load())
}

func (p *Peer) trust() connectionTrust {
	return connectionTrust{p.trusted, p.netGoodPiecesDirtied()}
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
func (pc *PeerConn) PeerPieces() *roaring.Bitmap {
	pc.locker().RLock()
	defer pc.locker().RUnlock()
	return pc.newPeerPieces()
}

// Returns a new Bitmap that includes bits for all pieces the peer could have based on their claims.
func (p *Peer) newPeerPieces() *roaring.Bitmap {
	// TODO: Can we use copy on write?
	ret := p.peerPieces().Clone()
	if all, _ := p.peerHasAllPieces(); all {
		if p.t.haveInfo() {
			ret.AddRange(0, bitmap.BitRange(p.t.numPieces()))
		} else {
			ret.AddRange(0, bitmap.ToEnd)
		}
	}
	return ret
}

func (p *Peer) stats() *ConnStats {
	return &p._stats
}

func (p *Peer) TryAsPeerConn() (*PeerConn, bool) {
	pc, ok := p.peerImpl.(*PeerConn)
	return pc, ok
}

func (p *Peer) uncancelledRequests() uint64 {
	return p.requestState.Requests.GetCardinality()
}

func (pc *PeerConn) remoteIsTransmission() bool {
	return bytes.HasPrefix(pc.PeerID[:], []byte("-TR")) && pc.PeerID[7] == '-'
}
