package torrent

import (
	"testing"

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
