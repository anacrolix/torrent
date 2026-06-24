package peer_protocol

import (
	"bufio"
	"bytes"
	"io"
	"testing"

	"github.com/go-quicktest/qt"

	chunkpool "github.com/anacrolix/torrent/internal/chunk-pool"
)

func BenchmarkDecodePieces(t *testing.B) {
	const pieceLen = 1 << 14
	inputMsg := Message{
		Type:  Piece,
		Index: 0,
		Begin: 1,
		Piece: make([]byte, pieceLen),
	}
	b := inputMsg.MustMarshalBinary()
	t.SetBytes(int64(len(b)))
	var r bytes.Reader
	// Try to somewhat emulate what torrent.Client would do. But the goal is to get decoding as fast
	// as possible and let consumers apply their own adjustments.
	d := Decoder{
		R:         bufio.NewReaderSize(&r, 1<<10),
		MaxLength: 1 << 18,
		Pool: chunkpool.New(pieceLen),
	}
	t.ReportAllocs()
	t.ResetTimer()
	for i := 0; i < t.N; i += 1 {
		r.Reset(b)
		var msg Message
		err := d.Decode(&msg)
		if err != nil {
			t.Fatal(err)
		}
		// This is very expensive, and should be discovered in tests rather than a benchmark.
		if false {
			qt.Assert(t, qt.DeepEquals(msg, inputMsg))
		}
		// Recycle via the pooled pointer the decoder handed back, avoiding a slice-header alloc.
		d.Pool.Put(msg.PiecePtr)
	}
}

func TestDecodeShortPieceEOF(t *testing.T) {
	r, w := io.Pipe()
	go func() {
		w.Write(Message{Type: Piece, Piece: make([]byte, 1)}.MustMarshalBinary())
		w.Close()
	}()
	d := Decoder{
		R:         bufio.NewReader(r),
		MaxLength: 1 << 15,
		Pool: chunkpool.New(2),
	}
	var m Message
	qt.Assert(t, qt.IsNil(d.Decode(&m)))
	qt.Check(t, qt.HasLen(m.Piece, 1))
	qt.Check(t, qt.ErrorIs(d.Decode(&m), io.EOF))
}

func TestDecodeOverlongPiece(t *testing.T) {
	r, w := io.Pipe()
	go func() {
		w.Write(Message{Type: Piece, Piece: make([]byte, 3)}.MustMarshalBinary())
		w.Close()
	}()
	d := Decoder{
		R:         bufio.NewReader(r),
		MaxLength: 1 << 15,
		Pool: chunkpool.New(2),
	}
	var m Message
	qt.Assert(t, qt.IsNotNil(d.Decode(&m)))
}
