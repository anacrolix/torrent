package torrent

import (
	"container/list"
	"encoding"
	"log"
	"net"
	"sync"
	"time"

	"bitbucket.org/anacrolix/go.torrent/peer_protocol"
)

// Maintains the state of a connection with a peer.
type connection struct {
	Socket net.Conn
	closed bool
	mu     sync.Mutex // Only for closing.
	post   chan peer_protocol.Message
	write  chan []byte

	// Stuff controlled by the local peer.
	Interested bool
	Choked     bool
	Requests   map[request]struct{}

	// Stuff controlled by the remote peer.
	PeerId          [20]byte
	PeerInterested  bool
	PeerChoked      bool
	PeerRequests    map[request]struct{}
	PeerExtensions  [8]byte
	PeerPieces      []bool
	PeerMaxRequests int // Maximum pending requests the peer allows.
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
	c.SetInterested(true)
	if c.PeerChoked {
		return false
	}
	if c.RequestPending(chunk) {
		return true
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
						log.Print("optimized cancel! %q", msg)
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
