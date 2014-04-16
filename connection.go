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
	post   chan encoding.BinaryMarshaler
	write  chan []byte

	// Stuff controlled by the local peer.
	Interested bool
	Choked     bool
	Requests   map[Request]struct{}

	// Stuff controlled by the remote peer.
	PeerId         [20]byte
	PeerInterested bool
	PeerChoked     bool
	PeerRequests   map[Request]struct{}
	PeerExtensions [8]byte
	PeerPieces     []bool
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

func (c *connection) Post(msg encoding.BinaryMarshaler) {
	c.post <- msg
}

// Returns true if more requests can be sent.
func (c *connection) Request(chunk Request) bool {
	if len(c.Requests) >= maxRequests {
		return false
	}
	if !c.PeerPieces[chunk.Index] {
		return true
	}
	c.SetInterested(true)
	if c.PeerChoked {
		return false
	}
	if _, ok := c.Requests[chunk]; !ok {
		c.Post(peer_protocol.Message{
			Type:   peer_protocol.Request,
			Index:  chunk.Index,
			Begin:  chunk.Begin,
			Length: chunk.Length,
		})
	}
	if c.Requests == nil {
		c.Requests = make(map[Request]struct{}, maxRequests)
	}
	c.Requests[chunk] = struct{}{}
	return true
}

// Returns true if an unsatisfied request was canceled.
func (c *connection) PeerCancel(r Request) bool {
	if c.PeerRequests == nil {
		return false
	}
	if _, ok := c.PeerRequests[r]; !ok {
		return false
	}
	delete(c.PeerRequests, r)
	return true
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

func (conn *connection) writer() {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		if !timer.Reset(time.Minute) {
			<-timer.C
		}
		var b []byte
		select {
		case <-timer.C:
			b = keepAliveBytes[:]
		case b = <-conn.write:
			if b == nil {
				return
			}
		}
		_, err := conn.Socket.Write(b)
		if conn.getClosed() {
			break
		}
		if err != nil {
			log.Print(err)
			break
		}
	}
}

func (conn *connection) writeOptimizer() {
	pending := list.New()
	var nextWrite []byte
	defer close(conn.write)
	for {
		write := conn.write
		if pending.Len() == 0 {
			write = nil
		} else {
			var err error
			nextWrite, err = pending.Front().Value.(encoding.BinaryMarshaler).MarshalBinary()
			if err != nil {
				panic(err)
			}
		}
		select {
		case msg, ok := <-conn.post:
			if !ok {
				return
			}
			pending.PushBack(msg)
		case write <- nextWrite:
			pending.Remove(pending.Front())
		}
	}
}
