package torrent

import (
	"io"
	"sync"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

// Various connection-level metrics. At the Torrent level these are
// aggregates. Chunks are messages with data payloads. Data is actual torrent
// content without any overhead. Useful is something we needed locally.
// Unwanted is something we didn't ask for (but may still be useful). Written
// is things sent to the peer, and Read is stuff received from them.
type ConnStats struct {
	// Total bytes on the wire. Includes handshakes and encryption.
	BytesWritten int64
	BytesRead    int64

	// The rest of the stats only occur on connections after handshakes.

	ChunksWritten int64

	ChunksRead         int64
	ChunksReadUseful   int64
	ChunksReadUnwanted int64

	DataBytesWritten    int64
	DataBytesRead       int64
	UsefulDataBytesRead int64

	// Number of pieces data was written to, that subsequently passed verification.
	GoodPiecesDirtied int64
	// Number of pieces data was written to, that subsequently failed
	// verification. Note that a connection may not have been the sole dirtier
	// of a piece.
	BadPiecesDirtied int64
}

func (cs *ConnStats) wroteMsg(msg *pp.Message) {
	switch msg.Type {
	case pp.Piece:
		cs.ChunksWritten++
		cs.DataBytesWritten += int64(len(msg.Piece))
	}
}

func (cs *ConnStats) readMsg(msg *pp.Message) {
	switch msg.Type {
	case pp.Piece:
		cs.ChunksRead++
		cs.DataBytesRead += int64(len(msg.Piece))
	}
}

func (cs *ConnStats) wroteBytes(n int64) {
	cs.BytesWritten += n
}

func (cs *ConnStats) readBytes(n int64) {
	cs.BytesRead += n
}

type connStatsReadWriter struct {
	rw io.ReadWriter
	l  sync.Locker
	c  *connection
}

func (me connStatsReadWriter) Write(b []byte) (n int, err error) {
	n, err = me.rw.Write(b)
	me.l.Lock()
	me.c.wroteBytes(int64(n))
	me.l.Unlock()
	return
}

func (me connStatsReadWriter) Read(b []byte) (n int, err error) {
	n, err = me.rw.Read(b)
	me.l.Lock()
	me.c.readBytes(int64(n))
	me.l.Unlock()
	return
}
