package torrent

import (
	"bufio"
	"container/list"
	"encoding"
	"errors"
	"expvar"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"bitbucket.org/anacrolix/go.torrent/internal/pieceordering"
	pp "bitbucket.org/anacrolix/go.torrent/peer_protocol"
)

var optimizedCancels = expvar.NewInt("optimizedCancels")

type peerSource byte

const (
	peerSourceIncoming = 'I'
	peerSourceDHT      = 'H'
	peerSourcePEX      = 'X'
)

// Maintains the state of a connection with a peer.
type connection struct {
	Socket    net.Conn
	Discovery peerSource
	uTP       bool
	closing   chan struct{}
	mu        sync.Mutex // Only for closing.
	post      chan pp.Message
	writeCh   chan []byte

	piecePriorities   []int
	pieceRequestOrder *pieceordering.Instance

	UnwantedChunksReceived int
	UsefulChunksReceived   int

	lastMessageReceived     time.Time
	completedHandshake      time.Time
	lastUsefulChunkReceived time.Time

	// Stuff controlled by the local peer.
	Interested       bool
	Choked           bool
	Requests         map[request]struct{}
	requestsLowWater int

	// Stuff controlled by the remote peer.
	PeerID             [20]byte
	PeerInterested     bool
	PeerChoked         bool
	PeerRequests       map[request]struct{}
	PeerExtensionBytes peerExtensionBytes
	// Whether the peer has the given piece. nil if they've not sent any
	// related messages yet.
	PeerPieces       []bool
	PeerMaxRequests  int // Maximum pending requests the peer allows.
	PeerExtensionIDs map[string]int64
	PeerClientName   string
}

func newConnection(sock net.Conn, peb peerExtensionBytes, peerID [20]byte, uTP bool) (c *connection) {
	c = &connection{
		Socket: sock,
		uTP:    uTP,

		Choked:             true,
		PeerChoked:         true,
		PeerMaxRequests:    250,
		PeerExtensionBytes: peb,
		PeerID:             peerID,

		closing: make(chan struct{}),
		writeCh: make(chan []byte),
		post:    make(chan pp.Message),

		completedHandshake: time.Now(),
	}
	go c.writer()
	go c.writeOptimizer(time.Minute)
	return
}

func (cn *connection) pendPiece(piece int, priority piecePriority) {
	if priority == piecePriorityNone {
		cn.pieceRequestOrder.DeletePiece(piece)
		return
	}
	key := cn.piecePriorities[piece]
	// There is overlap here so there's some probabilistic favouring of higher
	// priority pieces.
	switch priority {
	case piecePriorityReadahead:
		key -= len(cn.piecePriorities) / 3
	case piecePriorityNext:
		key -= len(cn.piecePriorities) / 2
	case piecePriorityNow:
		key -= len(cn.piecePriorities)
	}
	// Favour earlier pieces more than later pieces.
	key -= piece / 2
	cn.pieceRequestOrder.SetPiece(piece, key)
}

func (cn *connection) supportsExtension(ext string) bool {
	_, ok := cn.PeerExtensionIDs[ext]
	return ok
}

func (cn *connection) completedString() string {
	if cn.PeerPieces == nil {
		return "?"
	}
	// f := float32(cn.piecesPeerHasCount()) / float32(cn.totalPiecesCount())
	// return fmt.Sprintf("%d%%", int(f*100))
	return fmt.Sprintf("%d/%d", cn.piecesPeerHasCount(), cn.totalPiecesCount())
}

func (cn *connection) totalPiecesCount() int {
	return len(cn.PeerPieces)
}

func (cn *connection) piecesPeerHasCount() (count int) {
	for _, has := range cn.PeerPieces {
		if has {
			count++
		}
	}
	return
}

// Correct the PeerPieces slice length. Return false if the existing slice is
// invalid, such as by receiving badly sized BITFIELD, or invalid HAVE
// messages.
func (cn *connection) setNumPieces(num int) error {
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

func (cn *connection) WriteStatus(w io.Writer) {
	// \t isn't preserved in <pre> blocks?
	fmt.Fprintf(w, "%s\n    %s completed, good chunks: %d/%d reqs: %d-%d, last msg: %s, connected: %s, last useful chunk: %s, flags: ", fmt.Sprintf("%q: %s-%s", cn.PeerID, cn.Socket.LocalAddr(), cn.Socket.RemoteAddr()), cn.completedString(), cn.UsefulChunksReceived, cn.UnwantedChunksReceived+cn.UsefulChunksReceived, len(cn.Requests), len(cn.PeerRequests), eventAgeString(cn.lastMessageReceived), eventAgeString(cn.completedHandshake), eventAgeString(cn.lastUsefulChunkReceived))
	c := func(b byte) {
		fmt.Fprintf(w, "%c", b)
	}
	// Inspired by https://trac.transmissionbt.com/wiki/PeerStatusText
	if len(cn.Requests) != 0 {
		c('D')
	}
	if !cn.Interested {
		c('z')
	}
	if cn.PeerChoked && cn.Interested {
		c('i')
	}
	if !cn.Choked {
		if cn.PeerInterested {
			c('U')
		} else {
			c('u')
		}
	}
	if cn.Discovery != 0 {
		c(byte(cn.Discovery))
	}
	if cn.uTP {
		c('T')
	}
	fmt.Fprintln(w)
}

func (c *connection) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.closing:
		return
	default:
	}
	close(c.closing)
	// TODO: This call blocks sometimes, why?
	go c.Socket.Close()
}

func (c *connection) PeerHasPiece(index pp.Integer) bool {
	if c.PeerPieces == nil {
		return false
	}
	if int(index) >= len(c.PeerPieces) {
		return false
	}
	return c.PeerPieces[index]
}

func (c *connection) Post(msg pp.Message) {
	select {
	case c.post <- msg:
	case <-c.closing:
	}
}

func (c *connection) RequestPending(r request) bool {
	_, ok := c.Requests[r]
	return ok
}

// Returns true if more requests can be sent.
func (c *connection) Request(chunk request) bool {
	if len(c.Requests) >= c.PeerMaxRequests {
		return false
	}
	if !c.PeerHasPiece(chunk.Index) {
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

// Writes buffers to the socket from the write channel.
func (conn *connection) writer() {
	// Reduce write syscalls.
	buf := bufio.NewWriterSize(conn.Socket, 0x8000) // 32 KiB
	// Receives when buf is not empty.
	notEmpty := make(chan struct{}, 1)
	for {
		if buf.Buffered() != 0 {
			// Make sure it's receivable.
			select {
			case notEmpty <- struct{}{}:
			default:
			}
		}
		select {
		case b, ok := <-conn.writeCh:
			if !ok {
				return
			}
			_, err := buf.Write(b)
			if err != nil {
				conn.Close()
				return
			}
		case <-conn.closing:
			return
		case <-notEmpty:
			err := buf.Flush()
			if err != nil {
				return
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
		case <-conn.closing:
			return
		}
	}
}
