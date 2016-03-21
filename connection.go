package torrent

import (
	"bufio"
	"bytes"
	"container/list"
	"encoding"
	"errors"
	"expvar"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/anacrolix/missinggo"
	"github.com/anacrolix/missinggo/prioritybitmap"

	"github.com/anacrolix/torrent/bencode"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

var optimizedCancels = expvar.NewInt("optimizedCancels")

type peerSource byte

const (
	peerSourceTracker  = '\x00' // It's the default.
	peerSourceIncoming = 'I'
	peerSourceDHT      = 'H'
	peerSourcePEX      = 'X'
)

// Maintains the state of a connection with a peer.
type connection struct {
	t         *torrent
	conn      net.Conn
	rw        io.ReadWriter // The real slim shady
	encrypted bool
	Discovery peerSource
	uTP       bool
	closed    missinggo.Event
	post      chan pp.Message
	writeCh   chan []byte

	UnwantedChunksReceived int
	UsefulChunksReceived   int
	chunksSent             int

	lastMessageReceived     time.Time
	completedHandshake      time.Time
	lastUsefulChunkReceived time.Time
	lastChunkSent           time.Time

	// Stuff controlled by the local peer.
	Interested       bool
	Choked           bool
	Requests         map[request]struct{}
	requestsLowWater int
	// Indexed by metadata piece, set to true if posted and pending a
	// response.
	metadataRequests []bool
	sentHaves        []bool

	// Stuff controlled by the remote peer.
	PeerID             [20]byte
	PeerInterested     bool
	PeerChoked         bool
	PeerRequests       map[request]struct{}
	PeerExtensionBytes peerExtensionBytes
	// Whether the peer has the given piece. nil if they've not sent any
	// related messages yet.
	PeerPieces []bool
	peerHasAll bool
	// Pieces we've accepted chunks for from the peer.
	peerTouchedPieces map[int]struct{}

	PeerMaxRequests  int // Maximum pending requests the peer allows.
	PeerExtensionIDs map[string]byte
	PeerClientName   string

	pieceInclination  []int
	pieceRequestOrder prioritybitmap.PriorityBitmap
}

func newConnection() (c *connection) {
	c = &connection{
		Choked:          true,
		PeerChoked:      true,
		PeerMaxRequests: 250,

		writeCh: make(chan []byte),
		post:    make(chan pp.Message),
	}
	return
}

func (cn *connection) remoteAddr() net.Addr {
	return cn.conn.RemoteAddr()
}

func (cn *connection) localAddr() net.Addr {
	return cn.conn.LocalAddr()
}

func (cn *connection) supportsExtension(ext string) bool {
	_, ok := cn.PeerExtensionIDs[ext]
	return ok
}

func (cn *connection) completedString(t *torrent) string {
	if cn.PeerPieces == nil && !cn.peerHasAll {
		return "?"
	}
	return fmt.Sprintf("%d/%d", func() int {
		if cn.peerHasAll {
			if t.haveInfo() {
				return t.numPieces()
			}
			return -1
		}
		ret := 0
		for _, b := range cn.PeerPieces {
			if b {
				ret++
			}
		}
		return ret
	}(), func() int {
		if cn.peerHasAll || cn.PeerPieces == nil {
			if t.haveInfo() {
				return t.numPieces()
			}
			return -1
		}
		return len(cn.PeerPieces)
	}())
}

// Correct the PeerPieces slice length. Return false if the existing slice is
// invalid, such as by receiving badly sized BITFIELD, or invalid HAVE
// messages.
func (cn *connection) setNumPieces(num int) error {
	if cn.peerHasAll {
		return nil
	}
	if cn.PeerPieces == nil {
		return nil
	}
	if len(cn.PeerPieces) == num {
	} else if len(cn.PeerPieces) < num {
		cn.PeerPieces = append(cn.PeerPieces, make([]bool, num-len(cn.PeerPieces))...)
	} else if len(cn.PeerPieces) <= (num+7)/8*8 {
		for _, have := range cn.PeerPieces[num:] {
			if have {
				return errors.New("peer has invalid piece")
			}
		}
		cn.PeerPieces = cn.PeerPieces[:num]
	} else {
		return fmt.Errorf("peer bitfield is excessively long: expected %d, have %d", num, len(cn.PeerPieces))
	}
	if len(cn.PeerPieces) != num {
		panic("wat")
	}
	return nil
}

func eventAgeString(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return fmt.Sprintf("%.2fs ago", time.Now().Sub(t).Seconds())
}

func (cn *connection) connectionFlags() (ret string) {
	c := func(b byte) {
		ret += string([]byte{b})
	}
	if cn.encrypted {
		c('E')
	}
	if cn.Discovery != 0 {
		c(byte(cn.Discovery))
	}
	if cn.uTP {
		c('T')
	}
	return
}

// Inspired by https://trac.transmissionbt.com/wiki/PeerStatusText
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

func (cn *connection) String() string {
	var buf bytes.Buffer
	cn.WriteStatus(&buf, nil)
	return buf.String()
}

func (cn *connection) WriteStatus(w io.Writer, t *torrent) {
	// \t isn't preserved in <pre> blocks?
	fmt.Fprintf(w, "%+q: %s-%s\n", cn.PeerID, cn.localAddr(), cn.remoteAddr())
	fmt.Fprintf(w, "    last msg: %s, connected: %s, last useful chunk: %s\n",
		eventAgeString(cn.lastMessageReceived),
		eventAgeString(cn.completedHandshake),
		eventAgeString(cn.lastUsefulChunkReceived))
	fmt.Fprintf(w,
		"    %s completed, %d pieces touched, good chunks: %d/%d-%d reqq: %d-%d, flags: %s\n",
		cn.completedString(t),
		len(cn.peerTouchedPieces),
		cn.UsefulChunksReceived,
		cn.UnwantedChunksReceived+cn.UsefulChunksReceived,
		cn.chunksSent,
		len(cn.Requests),
		len(cn.PeerRequests),
		cn.statusFlags(),
	)
}

func (c *connection) Close() {
	c.closed.Set()
	c.discardPieceInclination()
	c.pieceRequestOrder.Clear()
	// TODO: This call blocks sometimes, why?
	go c.conn.Close()
}

func (c *connection) PeerHasPiece(piece int) bool {
	if c.peerHasAll {
		return true
	}
	if piece >= len(c.PeerPieces) {
		return false
	}
	return c.PeerPieces[piece]
}

func (c *connection) Post(msg pp.Message) {
	select {
	case c.post <- msg:
		postedMessageTypes.Add(strconv.FormatInt(int64(msg.Type), 10), 1)
	case <-c.closed.C():
	}
}

func (c *connection) RequestPending(r request) bool {
	_, ok := c.Requests[r]
	return ok
}

func (c *connection) requestMetadataPiece(index int) {
	eID := c.PeerExtensionIDs["ut_metadata"]
	if eID == 0 {
		return
	}
	if index < len(c.metadataRequests) && c.metadataRequests[index] {
		return
	}
	c.Post(pp.Message{
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
	for index >= len(c.metadataRequests) {
		c.metadataRequests = append(c.metadataRequests, false)
	}
	c.metadataRequests[index] = true
}

func (c *connection) requestedMetadataPiece(index int) bool {
	return index < len(c.metadataRequests) && c.metadataRequests[index]
}

// The actual value to use as the maximum outbound requests.
func (c *connection) nominalMaxRequests() (ret int) {
	ret = c.PeerMaxRequests
	if ret > 64 {
		ret = 64
	}
	return
}

// Returns true if more requests can be sent.
func (c *connection) Request(chunk request) bool {
	if len(c.Requests) >= c.nominalMaxRequests() {
		return false
	}
	if !c.PeerHasPiece(int(chunk.Index)) {
		return true
	}
	if c.RequestPending(chunk) {
		return true
	}
	c.SetInterested(true)
	if c.PeerChoked {
		return false
	}
	if c.Requests == nil {
		c.Requests = make(map[request]struct{}, c.PeerMaxRequests)
	}
	c.Requests[chunk] = struct{}{}
	c.requestsLowWater = len(c.Requests) / 2
	c.Post(pp.Message{
		Type:   pp.Request,
		Index:  chunk.Index,
		Begin:  chunk.Begin,
		Length: chunk.Length,
	})
	return true
}

// Returns true if an unsatisfied request was canceled.
func (c *connection) Cancel(r request) bool {
	if c.Requests == nil {
		return false
	}
	if _, ok := c.Requests[r]; !ok {
		return false
	}
	delete(c.Requests, r)
	c.Post(pp.Message{
		Type:   pp.Cancel,
		Index:  r.Index,
		Begin:  r.Begin,
		Length: r.Length,
	})
	return true
}

// Returns true if an unsatisfied request was canceled.
func (c *connection) PeerCancel(r request) bool {
	if c.PeerRequests == nil {
		return false
	}
	if _, ok := c.PeerRequests[r]; !ok {
		return false
	}
	delete(c.PeerRequests, r)
	return true
}

func (c *connection) Choke() {
	if c.Choked {
		return
	}
	c.Post(pp.Message{
		Type: pp.Choke,
	})
	c.PeerRequests = nil
	c.Choked = true
}

func (c *connection) Unchoke() {
	if !c.Choked {
		return
	}
	c.Post(pp.Message{
		Type: pp.Unchoke,
	})
	c.Choked = false
}

func (c *connection) SetInterested(interested bool) {
	if c.Interested == interested {
		return
	}
	c.Post(pp.Message{
		Type: func() pp.MessageType {
			if interested {
				return pp.Interested
			} else {
				return pp.NotInterested
			}
		}(),
	})
	c.Interested = interested
}

var (
	// Track connection writer buffer writes and flushes, to determine its
	// efficiency.
	connectionWriterFlush = expvar.NewInt("connectionWriterFlush")
	connectionWriterWrite = expvar.NewInt("connectionWriterWrite")
)

// Writes buffers to the socket from the write channel.
func (conn *connection) writer() {
	defer func() {
		conn.t.cl.mu.Lock()
		defer conn.t.cl.mu.Unlock()
		conn.Close()
	}()
	// Reduce write syscalls.
	buf := bufio.NewWriter(conn.rw)
	for {
		if buf.Buffered() == 0 {
			// There's nothing to write, so block until we get something.
			select {
			case b, ok := <-conn.writeCh:
				if !ok {
					return
				}
				connectionWriterWrite.Add(1)
				_, err := buf.Write(b)
				if err != nil {
					return
				}
			case <-conn.closed.C():
				return
			}
		} else {
			// We already have something to write, so flush if there's nothing
			// more to write.
			select {
			case b, ok := <-conn.writeCh:
				if !ok {
					return
				}
				connectionWriterWrite.Add(1)
				_, err := buf.Write(b)
				if err != nil {
					return
				}
			case <-conn.closed.C():
				return
			default:
				connectionWriterFlush.Add(1)
				err := buf.Flush()
				if err != nil {
					return
				}
			}
		}
	}
}

func (conn *connection) writeOptimizer(keepAliveDelay time.Duration) {
	defer close(conn.writeCh) // Responsible for notifying downstream routines.
	pending := list.New()     // Message queue.
	var nextWrite []byte      // Set to nil if we need to need to marshal the next message.
	timer := time.NewTimer(keepAliveDelay)
	defer timer.Stop()
	lastWrite := time.Now()
	for {
		write := conn.writeCh // Set to nil if there's nothing to write.
		if pending.Len() == 0 {
			write = nil
		} else if nextWrite == nil {
			var err error
			nextWrite, err = pending.Front().Value.(encoding.BinaryMarshaler).MarshalBinary()
			if err != nil {
				panic(err)
			}
		}
	event:
		select {
		case <-timer.C:
			if pending.Len() != 0 {
				break
			}
			keepAliveTime := lastWrite.Add(keepAliveDelay)
			if time.Now().Before(keepAliveTime) {
				timer.Reset(keepAliveTime.Sub(time.Now()))
				break
			}
			pending.PushBack(pp.Message{Keepalive: true})
			postedKeepalives.Add(1)
		case msg, ok := <-conn.post:
			if !ok {
				return
			}
			if msg.Type == pp.Cancel {
				for e := pending.Back(); e != nil; e = e.Prev() {
					elemMsg := e.Value.(pp.Message)
					if elemMsg.Type == pp.Request && msg.Index == elemMsg.Index && msg.Begin == elemMsg.Begin && msg.Length == elemMsg.Length {
						pending.Remove(e)
						optimizedCancels.Add(1)
						break event
					}
				}
			}
			pending.PushBack(msg)
		case write <- nextWrite:
			pending.Remove(pending.Front())
			nextWrite = nil
			lastWrite = time.Now()
			if pending.Len() == 0 {
				timer.Reset(keepAliveDelay)
			}
		case <-conn.closed.C():
			return
		}
	}
}

func (cn *connection) Have(piece int) {
	for piece >= len(cn.sentHaves) {
		cn.sentHaves = append(cn.sentHaves, false)
	}
	if cn.sentHaves[piece] {
		return
	}
	cn.Post(pp.Message{
		Type:  pp.Have,
		Index: pp.Integer(piece),
	})
	cn.sentHaves[piece] = true
}

func (cn *connection) Bitfield(haves []bool) {
	if cn.sentHaves != nil {
		panic("bitfield must be first have-related message sent")
	}
	cn.Post(pp.Message{
		Type:     pp.Bitfield,
		Bitfield: haves,
	})
	cn.sentHaves = haves
}

func (c *connection) updateRequests() {
	if !c.t.haveInfo() {
		return
	}
	if c.Interested {
		if c.PeerChoked {
			return
		}
		if len(c.Requests) > c.requestsLowWater {
			return
		}
	}
	c.fillRequests()
	if len(c.Requests) == 0 && !c.PeerChoked {
		// So we're not choked, but we don't want anything right now. We may
		// have completed readahead, and the readahead window has not rolled
		// over to the next piece. Better to stay interested in case we're
		// going to want data in the near future.
		c.SetInterested(!c.t.haveAllPieces())
	}
}

func (c *connection) fillRequests() {
	c.pieceRequestOrder.IterTyped(func(piece int) (more bool) {
		if c.t.cl.config.Debug && c.t.havePiece(piece) {
			panic(piece)
		}
		return c.requestPiecePendingChunks(piece)
	})
}

func (c *connection) requestPiecePendingChunks(piece int) (again bool) {
	return c.t.connRequestPiecePendingChunks(c, piece)
}

func (c *connection) stopRequestingPiece(piece int) {
	c.pieceRequestOrder.Remove(piece)
}

func (c *connection) updatePiecePriority(piece int) {
	tpp := c.t.piecePriority(piece)
	if !c.PeerHasPiece(piece) {
		tpp = PiecePriorityNone
	}
	if tpp == PiecePriorityNone {
		c.stopRequestingPiece(piece)
		return
	}
	prio := c.getPieceInclination()[piece]
	switch tpp {
	case PiecePriorityNormal:
	case PiecePriorityReadahead:
		prio -= c.t.numPieces()
	case PiecePriorityNext, PiecePriorityNow:
		prio -= 2 * c.t.numPieces()
	default:
		panic(tpp)
	}
	prio += piece
	c.pieceRequestOrder.Set(piece, prio)
	c.updateRequests()
}

func (c *connection) getPieceInclination() []int {
	if c.pieceInclination == nil {
		c.pieceInclination = c.t.getConnPieceInclination()
	}
	return c.pieceInclination
}

func (c *connection) discardPieceInclination() {
	if c.pieceInclination == nil {
		return
	}
	c.t.putPieceInclination(c.pieceInclination)
	c.pieceInclination = nil
}

func (c *connection) peerGotPiece(piece int) error {
	if !c.peerHasAll {
		if c.t.haveInfo() {
			if c.PeerPieces == nil {
				c.PeerPieces = make([]bool, c.t.numPieces())
			}
		} else {
			for piece >= len(c.PeerPieces) {
				c.PeerPieces = append(c.PeerPieces, false)
			}
		}
		if piece >= len(c.PeerPieces) {
			return errors.New("peer got out of range piece index")
		}
		c.PeerPieces[piece] = true
	}
	c.updatePiecePriority(piece)
	return nil
}
