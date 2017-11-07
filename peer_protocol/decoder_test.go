package peer_protocol

import (
	"bufio"
	"io"
	"sync"
	"testing"

	"github.com/bradfitz/iter"
	"github.com/stretchr/testify/require"
)

func BenchmarkDecodePieces(t *testing.B) {
	r, w := io.Pipe()
	const pieceLen = 1 << 14
	msg := Message{
		Type:  Piece,
		Index: 0,
		Begin: 1,
		Piece: make([]byte, pieceLen),
	}
	b, err := msg.MarshalBinary()
	require.NoError(t, err)
	t.SetBytes(int64(len(b)))
	defer r.Close()
	go func() {
		defer w.Close()
		for {
			n, err := w.Write(b)
			if err == io.ErrClosedPipe {
				return
			}
			require.NoError(t, err)
			require.Equal(t, len(b), n)
		}
	}()
	d := Decoder{
		// Emulate what package torrent's client would do.
		R:         bufio.NewReader(r),
		MaxLength: 1 << 18,
		Pool: &sync.Pool{
			New: func() interface{} {
				return make([]byte, pieceLen)
			},
		},
	}
	for range iter.N(t.N) {
		var msg Message
		require.NoError(t, d.Decode(&msg))
		// WWJD
		d.Pool.Put(&msg.Piece)
	}
}
