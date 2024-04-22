package torrent

import (
	"bytes"
	"testing"

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

const (
	// 4M
	MsgLength4M = 4 * 1024 * 1024
	// 1M
	MsgLength8M = 1 * 1024 * 1024
	// 512K
	MsgLength512K = 512 * 1024
)

func runBenchmarkWriteToBuffer(b *testing.B, length int64) {
	writer := &peerConnMsgWriter{
		writeBuffer: &bytes.Buffer{},
	}
	msg := PieceMsg(MsgLength4M)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		writer.writeBuffer.Reset()
		b.StartTimer()
		writer.writeToBuffer(msg)
	}
}

func BenchmarkWriteToBuffer4M(b *testing.B) {
	runBenchmarkWriteToBuffer(b, MsgLength4M)
}

func BenchmarkWriteToBuffer8M(b *testing.B) {
	runBenchmarkWriteToBuffer(b, MsgLength8M)
}

func BenchmarkWriteToBuffer512K(b *testing.B) {
	runBenchmarkWriteToBuffer(b, MsgLength512K)
}

func runBenchmarkMarshalBinaryWrite(b *testing.B, length int64) {
	writer := &peerConnMsgWriter{
		writeBuffer: &bytes.Buffer{},
	}
	msg := PieceMsg(length)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		writer.writeBuffer.Reset()
		b.StartTimer()
		writer.writeBuffer.Write(msg.MustMarshalBinary())
	}
}

func BenchmarkMarshalBinaryWrite4M(b *testing.B) {
	runBenchmarkMarshalBinaryWrite(b, MsgLength4M)
}

func BenchmarkMarshalBinaryWrite8M(b *testing.B) {
	runBenchmarkMarshalBinaryWrite(b, MsgLength8M)
}

func BenchmarkMarshalBinaryWrite512K(b *testing.B) {
	runBenchmarkMarshalBinaryWrite(b, MsgLength512K)
}
