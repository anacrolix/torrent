package torrent

import (
	"bytes"
	"io"
	"time"

	"github.com/anacrolix/chansync"
	"github.com/anacrolix/log"
	"github.com/anacrolix/sync"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

func (c *PeerConn) initMessageWriter() {
	w := &c.messageWriter
	*w = peerConnMsgWriter{
		fillWriteBuffer: func() {
			c.locker().Lock()
			defer c.locker().Unlock()
			if c.closed.IsSet() {
				return
			}
			c.fillWriteBuffer()
		},
		closed: &c.closed,
		logger: c.logger,
		w:      c.w,
		keepAlive: func() bool {
			c.locker().RLock()
			defer c.locker().RUnlock()
			return c.useful()
		},
		writeBuffer: new(bytes.Buffer),
	}
}

func (c *PeerConn) startMessageWriter() {
	c.initMessageWriter()
	go c.messageWriterRunner()
}

func (c *PeerConn) messageWriterRunner() {
	defer c.locker().Unlock()
	defer c.close()
	defer c.locker().Lock()
	c.messageWriter.run(c.t.cl.config.KeepAliveTimeout)
}

type peerConnMsgWriter struct {
	// Must not be called with the local mutex held, as it will call back into the write method.
	fillWriteBuffer func()
	closed          *chansync.SetOnce
	logger          log.Logger
	w               io.Writer
	keepAlive       func() bool

	mu        sync.Mutex
	writeCond chansync.BroadcastCond
	// Pointer so we can swap with the "front buffer".
	writeBuffer *bytes.Buffer
}

// Routine that writes to the peer. Some of what to write is buffered by
// activity elsewhere in the Client, and some is determined locally when the
// connection is writable.
func (cn *peerConnMsgWriter) run(keepAliveTimeout time.Duration) {
	lastWrite := time.Now()
	keepAliveTimer := time.NewTimer(keepAliveTimeout)
	frontBuf := new(bytes.Buffer)
	for {
		if cn.closed.IsSet() {
			return
		}
		cn.fillWriteBuffer()
		keepAlive := cn.keepAlive()
		cn.mu.Lock()
		if cn.writeBuffer.Len() == 0 && time.Since(lastWrite) >= keepAliveTimeout && keepAlive {
			cn.writeBuffer.Write(pp.Message{Keepalive: true}.MustMarshalBinary())
			torrent.Add("written keepalives", 1)
		}
		if cn.writeBuffer.Len() == 0 {
			writeCond := cn.writeCond.Signaled()
			cn.mu.Unlock()
			select {
			case <-cn.closed.Done():
			case <-writeCond:
			case <-keepAliveTimer.C:
			}
			continue
		}
		// Flip the buffers.
		frontBuf, cn.writeBuffer = cn.writeBuffer, frontBuf
		cn.mu.Unlock()
		if frontBuf.Len() == 0 {
			panic("expected non-empty front buffer")
		}
		var err error
		for frontBuf.Len() != 0 {
			// Limit write size for WebRTC. See https://github.com/pion/datachannel/issues/59.
			next := frontBuf.Next(1<<16 - 1)
			var n int
			n, err = cn.w.Write(next)
			if err == nil && n != len(next) {
				panic("expected full write")
			}
		}
		if err != nil {
			cn.logger.WithDefaultLevel(log.Debug).Printf("error writing: %v", err)
			return
		}
		lastWrite = time.Now()
		keepAliveTimer.Reset(keepAliveTimeout)
	}
}

func (cn *peerConnMsgWriter) write(msg pp.Message) bool {
	cn.mu.Lock()
	defer cn.mu.Unlock()
	cn.writeBuffer.Write(msg.MustMarshalBinary())
	cn.writeCond.Broadcast()
	return !cn.writeBufferFull()
}

func (cn *peerConnMsgWriter) writeBufferFull() bool {
	return cn.writeBuffer.Len() >= writeBufferHighWaterLen
}
