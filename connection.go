package torrent

import (
	"container/list"
	"encoding"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"

	"bitbucket.org/anacrolix/go.torrent/peer_protocol"
)

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
	closed    bool
	mu        sync.Mutex // Only for closing.
	post      chan peer_protocol.Message
	write     chan []byte

	// Stuff controlled by the local peer.
	Interested bool
	Choked     bool
	Requests   map[request]struct{}

	// Stuff controlled by the remote peer.
	PeerId         [20]byte
	PeerInterested bool
	PeerChoked     bool
	PeerRequests   map[request]struct{}
	PeerExtensions [8]byte
	// Whether the peer has the given piece. nil if they've not sent any
	// related messages yet.
	PeerPieces       []bool
	PeerMaxRequests  int // Maximum pending requests the peer allows.
	PeerExtensionIDs map[string]int64
	PeerClientName   string
}

func (cn *connection) completedString() string {
	if cn.PeerPieces == nil {
		return "?"
	}
	f := float32(cn.piecesPeerHasCount()) / float32(cn.totalPiecesCount())
	return fmt.Sprintf("%d%%", int(f*100))
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
	} else if len(cn.PeerPieces) < 8*(num+7)/8 {
		for _, have := range cn.PeerPieces[num:] {
			if have {
				return errors.New("peer has invalid piece")
			}
		}
		cn.PeerPieces = cn.PeerPieces[:num]
	} else {
		return errors.New("peer bitfield is excessively long")
	}
	if len(cn.PeerPieces) != num {
		panic("wat")
	}
	return nil
}

func (cn *connection) WriteStatus(w io.Writer) {
	fmt.Fprintf(w, "%q: %s-%s: %s completed, reqs: %d-%d, flags: ", cn.PeerId, cn.Socket.LocalAddr(), cn.Socket.RemoteAddr(), cn.completedString(), len(cn.Requests), len(cn.PeerRequests))
	c := func(b byte) {
		fmt.Fprintf(w, "%c", b)
	}
	// https://trac.transmissionbt.com/wiki/PeerStatusText
	if cn.PeerInterested && !cn.Choked {
		c('O')
	}
	if len(cn.Requests) != 0 {
		c('D')
	}
	if cn.PeerChoked && cn.Interested {
		c('d')
	}
	if !cn.Choked && cn.PeerInterested {
		c('U')
	} else {
		c('u')
	}
	if !cn.PeerChoked && !cn.Interested {
		c('K')
	}
	if !cn.Choked && !cn.PeerInterested {
		c('?')
	}
	if cn.Discovery != 0 {
		c(byte(cn.Discovery))
	}
	fmt.Fprintln(w)
}

func (c *connection) Close() {
	c.mu.Lock()
	if c.closed {
		return
	}
	c.Socket.Close()
	close(c.post)
	c.closed = true
	c.mu.Unlock()
}

func (c *connection) getClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *connection) PeerHasPiece(index peer_protocol.Integer) bool {
	if c.PeerPieces == nil {
		return false
	}
	if int(index) >= len(c.PeerPieces) {
		return false
	}
	return c.PeerPieces[index]
}

func (c *connection) Post(msg peer_protocol.Message) {
	c.post <- msg
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
	c.Post(peer_protocol.Message{
		Type:   peer_protocol.Request,
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
	c.Post(peer_protocol.Message{
		Type:   peer_protocol.Cancel,
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
	c.Post(peer_protocol.Message{
		Type: peer_protocol.Choke,
	})
	c.Choked = true
}

func (c *connection) Unchoke() {
	if !c.Choked {
		return
	}
	c.Post(peer_protocol.Message{
		Type: peer_protocol.Unchoke,
	})
	c.Choked = false
}

func (c *connection) SetInterested(interested bool) {
	if c.Interested == interested {
		return
	}
	c.Post(peer_protocol.Message{
		Type: func() peer_protocol.MessageType {
			if interested {
				return peer_protocol.Interested
			} else {
				return peer_protocol.NotInterested
			}
		}(),
	})
	c.Interested = interested
}

var (
	// Four consecutive zero bytes that comprise a keep alive on the wire.
	keepAliveBytes [4]byte
)

// Writes buffers to the socket from the write channel.
func (conn *connection) writer() {
	for b := range conn.write {
		_, err := conn.Socket.Write(b)
		// log.Printf("wrote %q to %s", b, conn.Socket.RemoteAddr())
		if err != nil {
			if !conn.getClosed() {
				log.Print(err)
			}
			break
		}
	}
}

func (conn *connection) writeOptimizer(keepAliveDelay time.Duration) {
	defer close(conn.write) // Responsible for notifying downstream routines.
	pending := list.New()   // Message queue.
	var nextWrite []byte    // Set to nil if we need to need to marshal the next message.
	timer := time.NewTimer(keepAliveDelay)
	defer timer.Stop()
	lastWrite := time.Now()
	for {
		write := conn.write // Set to nil if there's nothing to write.
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
			pending.PushBack(peer_protocol.Message{Keepalive: true})
		case msg, ok := <-conn.post:
			if !ok {
				return
			}
			if msg.Type == peer_protocol.Cancel {
				for e := pending.Back(); e != nil; e = e.Prev() {
					elemMsg := e.Value.(peer_protocol.Message)
					if elemMsg.Type == peer_protocol.Request && msg.Index == elemMsg.Index && msg.Begin == elemMsg.Begin && msg.Length == elemMsg.Length {
						pending.Remove(e)
						log.Printf("optimized cancel! %v", msg)
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
		}
	}
}
