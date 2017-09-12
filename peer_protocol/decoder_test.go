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
	msg := Message{
		Type:  Piece,
		Index: 0,
		Begin: 1,
		Piece: make([]byte, 1<<14),
	}
	b, err := msg.MarshalBinary()
	require.NoError(t, err)
	t.SetBytes(int64(len(b)))
	go func() {
		for {
			n, err := w.Write(b)
			require.Equal(t, len(b), n)
			require.NoError(t, err)
		}
	}()
	d := Decoder{
		R:         bufio.NewReader(r),
		MaxLength: 1 << 18,
		Pool: &sync.Pool{
			New: func() interface{} {
				return make([]byte, 1<<14)
			},
		},
	}
	for range iter.N(t.N) {
		var msg Message
		require.NoError(t, d.Decode(&msg))
		d.Pool.Put(msg.Piece)
	}
}
