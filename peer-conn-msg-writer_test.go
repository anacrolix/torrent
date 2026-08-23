package torrent

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/chansync"
	"github.com/anacrolix/log"
	"github.com/dustin/go-humanize"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

func PieceMsg(length int64) pp.Message {
	return pp.Message{
		Type:  pp.Piece,
		Index: pp.Integer(0),
		Begin: pp.Integer(0),
		Piece: make([]byte, length),
	}
}

var benchmarkPieceLengths = []int{defaultChunkSize, 1 << 20, 4 << 20, 8 << 20}

func runBenchmarkWriteToBuffer(b *testing.B, length int64) {
	writer := &peerConnMsgWriter{
		writeBuffer: new(peerConnMsgWriterBuffer),
	}
	msg := PieceMsg(length)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		//b.StopTimer()
		writer.writeBuffer.Reset()
		//b.StartTimer()
		writer.writeToBuffer(msg)
	}
}

func BenchmarkWritePieceMsg(b *testing.B) {
	for _, length := range benchmarkPieceLengths {
		b.Run(humanize.IBytes(uint64(length)), func(b *testing.B) {
			b.Run("ToBuffer", func(b *testing.B) {
				b.SetBytes(int64(length))
				runBenchmarkWriteToBuffer(b, int64(length))
			})
			b.Run("MarshalBinary", func(b *testing.B) {
				b.SetBytes(int64(length))
				runBenchmarkMarshalBinaryWrite(b, int64(length))
			})
		})
	}
}

// A broadcast that lands while fillWriteBuffer is running must not be
// lost. The writer has to install its wakeup channel before filling, so
// that a concurrent Broadcast closes a channel the subsequent select
// observes. See #1070.
func TestMsgWriterBroadcastDuringFillWakesWriter(t *testing.T) {
	var w *peerConnMsgWriter
	var fills atomic.Int32
	w = &peerConnMsgWriter{
		fillWriteBuffer: func() {
			if fills.Add(1) == 1 {
				w.writeCond.Broadcast()
			}
		},
		closed:      new(chansync.SetOnce),
		logger:      log.Default,
		w:           io.Discard,
		keepAlive:   func() bool { return false },
		writeBuffer: new(peerConnMsgWriterBuffer),
	}
	done := make(chan struct{})
	go func() {
		w.run(time.Hour)
		close(done)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for fills.Load() < 2 {
		if time.Now().After(deadline) {
			w.closed.Set()
			w.writeCond.Broadcast()
			t.Fatal("writer never woke after a broadcast during fill: wakeup lost")
		}
		time.Sleep(time.Millisecond)
	}
	w.closed.Set()
	w.writeCond.Broadcast()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("writer run did not return after close")
	}
}

func runBenchmarkMarshalBinaryWrite(b *testing.B, length int64) {
	writer := &peerConnMsgWriter{
		writeBuffer: &peerConnMsgWriterBuffer{},
	}
	msg := PieceMsg(length)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		//b.StopTimer()
		writer.writeBuffer.Reset()
		//b.StartTimer()
		writer.writeBuffer.Write(msg.MustMarshalBinary())
	}
}
