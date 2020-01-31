package torrent

import (
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sync/atomic"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

// ConnStats various connection-level metrics. At the Torrent level these are
// aggregates. Chunks are messages with data payloads. Data is actual torrent
// content without any overhead. Useful is something we needed locally.
// Unwanted is something we didn't ask for (but may still be useful). Written
// is things sent to the peer, and Read is stuff received from them.
type ConnStats struct {
	// Total bytes on the wire. Includes handshakes and encryption.
	BytesWritten     count
	BytesWrittenData count

	BytesRead           count
	BytesReadData       count
	BytesReadUsefulData count

	ChunksWritten count

	ChunksRead       count
	ChunksReadUseful count
	ChunksReadWasted count

	MetadataChunksRead count

	// Number of pieces data was written to, that subsequently passed verification.
	PiecesDirtiedGood count
	// Number of pieces data was written to, that subsequently failed
	// verification. Note that a connection may not have been the sole dirtier
	// of a piece.
	PiecesDirtiedBad count
}

// Copy returns a copy of the connection stats.
func (t *ConnStats) Copy() (ret ConnStats) {
	for i := 0; i < reflect.TypeOf(ConnStats{}).NumField(); i++ {
		n := reflect.ValueOf(t).Elem().Field(i).Addr().Interface().(*count).Int64()
		reflect.ValueOf(&ret).Elem().Field(i).Addr().Interface().(*count).Add(n)
	}
	return
}

type count struct {
	n int64
}

var _ fmt.Stringer = (*count)(nil)

func (t *count) Add(n int64) {
	atomic.AddInt64(&t.n, n)
}

func (t *count) Int64() int64 {
	return atomic.LoadInt64(&t.n)
}

func (t *count) String() string {
	return fmt.Sprintf("%v", t.Int64())
}

func (t *count) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.n)
}

func (t *ConnStats) wroteMsg(msg *pp.Message) {
	// TODO: Track messages and not just chunks.
	switch msg.Type {
	case pp.Piece:
		t.ChunksWritten.Add(1)
		t.BytesWrittenData.Add(int64(len(msg.Piece)))
	}
}

func (t *ConnStats) readMsg(msg *pp.Message) {
	// We want to also handle extended metadata pieces here, but we wouldn't
	// have decoded the extended payload yet.
	switch msg.Type {
	case pp.Piece:
		t.ChunksRead.Add(1)
		t.BytesReadData.Add(int64(len(msg.Piece)))
	}
}

func (t *ConnStats) incrementPiecesDirtiedGood() {
	t.PiecesDirtiedGood.Add(1)
}

func (t *ConnStats) incrementPiecesDirtiedBad() {
	t.PiecesDirtiedBad.Add(1)
}

func add(n int64, f func(*ConnStats) *count) func(*ConnStats) {
	return func(cs *ConnStats) {
		p := f(cs)
		p.Add(n)
	}
}

type connStatsReadWriter struct {
	rw io.ReadWriter
	c  *connection
}

func (me connStatsReadWriter) Write(b []byte) (n int, err error) {
	n, err = me.rw.Write(b)
	me.c.wroteBytes(int64(n))
	return
}

func (me connStatsReadWriter) Read(b []byte) (n int, err error) {
	n, err = me.rw.Read(b)
	me.c.readBytes(int64(n))
	return
}
