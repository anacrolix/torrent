package torrent

import (
	"testing"
	"time"

	"github.com/anacrolix/chansync"
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

// BroadcastCond intentionally drops a broadcast when no Signaled channel is
// armed. Force the refill/broadcast interleaving that used to strand the
// writer: the first refill broadcasts while the buffer is empty, and only a
// correctly pre-armed run loop can observe it and perform the second refill.
func TestPeerConnMsgWriterArmsWakeupBeforeRefill(t *testing.T) {
	closed := new(chansync.SetOnce)
	secondRefill := make(chan struct{})
	exited := make(chan struct{})
	fillCalls := 0

	var writer *peerConnMsgWriter
	writer = &peerConnMsgWriter{
		closed:      closed,
		keepAlive:   func() bool { return false },
		writeBuffer: new(peerConnMsgWriterBuffer),
	}
	writer.fillWriteBuffer = func() {
		fillCalls++
		switch fillCalls {
		case 1:
			writer.writeCond.Broadcast()
		case 2:
			close(secondRefill)
			closed.Set()
		}
	}

	go func() {
		defer close(exited)
		writer.run(time.Hour)
	}()
	t.Cleanup(func() {
		closed.Set()
		select {
		case <-exited:
		case <-time.After(time.Second):
			t.Error("peer message writer did not exit after test cleanup")
		}
	})

	select {
	case <-secondRefill:
	case <-time.After(time.Second):
		t.Fatal("broadcast during refill was lost before the writer armed its condition")
	}
}
