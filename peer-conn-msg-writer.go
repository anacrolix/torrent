package torrent

import (
	"bytes"
	"io"
	"time"

	"github.com/anacrolix/log"
	"github.com/anacrolix/sync"

	"github.com/anacrolix/torrent/internal/chansync"
	pp "github.com/anacrolix/torrent/peer_protocol"
)

func (pc *PeerConn) startWriter() {
	w := &pc.messageWriter
	*w = peerConnMsgWriter{
		fillWriteBuffer: func() {
			pc.locker().Lock()
			defer pc.locker().Unlock()
			if pc.closed.IsSet() {
				return
			}
			pc.fillWriteBuffer()
		},
		closed: &pc.closed,
		logger: pc.logger,
		w:      pc.w,
		keepAlive: func() bool {
			pc.locker().Lock()
			defer pc.locker().Unlock()
			return pc.useful()
		},
		writeBuffer: new(bytes.Buffer),
	}
	go func() {
		defer pc.locker().Unlock()
		defer pc.close()
		defer pc.locker().Lock()
		pc.messageWriter.run(time.Minute)
	}()
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
	var (
		lastWrite      time.Time = time.Now()
		keepAliveTimer *time.Timer
	)
	keepAliveTimer = time.AfterFunc(keepAliveTimeout, func() {
		cn.mu.Lock()
		defer cn.mu.Unlock()
		if time.Since(lastWrite) >= keepAliveTimeout {
			cn.writeCond.Broadcast()
		}
		keepAliveTimer.Reset(keepAliveTimeout)
	})
	cn.mu.Lock()
	defer cn.mu.Unlock()
	defer keepAliveTimer.Stop()
	frontBuf := new(bytes.Buffer)
	for {
		if cn.closed.IsSet() {
			return
		}
		if cn.writeBuffer.Len() == 0 {
			func() {
				cn.mu.Unlock()
				defer cn.mu.Lock()
				cn.fillWriteBuffer()
			}()
		}
		if cn.writeBuffer.Len() == 0 && time.Since(lastWrite) >= keepAliveTimeout && cn.keepAlive() {
			cn.writeBuffer.Write(pp.Message{Keepalive: true}.MustMarshalBinary())
			torrent.Add("written keepalives", 1)
		}
		if cn.writeBuffer.Len() == 0 {
			writeCond := cn.writeCond.Signaled()
			cn.mu.Unlock()
			select {
			case <-cn.closed.Done():
			case <-writeCond:
			}
			cn.mu.Lock()
			continue
		}
		// Flip the buffers.
		frontBuf, cn.writeBuffer = cn.writeBuffer, frontBuf
		cn.mu.Unlock()
		n, err := cn.w.Write(frontBuf.Bytes())
		cn.mu.Lock()
		if n != 0 {
			lastWrite = time.Now()
			keepAliveTimer.Reset(keepAliveTimeout)
		}
		if err != nil {
			cn.logger.WithDefaultLevel(log.Debug).Printf("error writing: %v", err)
			return
		}
		if n != frontBuf.Len() {
			panic("short write")
		}
		frontBuf.Reset()
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
