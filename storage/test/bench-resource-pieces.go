package test_storage

import (
	"bytes"
	"io"
	"io/ioutil"
	"math/rand"
	"sync"
	"testing"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/bradfitz/iter"
	qt "github.com/frankban/quicktest"
)

const chunkSize = 1 << 14

func BenchmarkPieceMarkComplete(b *testing.B, ci storage.ClientImpl, pieceSize int64, numPieces int, capacity int64) {
	c := qt.New(b)
	ti, err := ci.OpenTorrent(nil, metainfo.Hash{})
	c.Assert(err, qt.IsNil)
	defer ti.Close()
	info := &metainfo.Info{
		Pieces:      make([]byte, numPieces*metainfo.HashSize),
		PieceLength: pieceSize,
		Length:      pieceSize * int64(numPieces),
	}
	rand.Read(info.Pieces)
	data := make([]byte, pieceSize)
	oneIter := func() {
		for pieceIndex := range iter.N(numPieces) {
			pi := ti.Piece(info.Piece(pieceIndex))
			rand.Read(data)
			var wg sync.WaitGroup
			for off := int64(0); off < int64(len(data)); off += chunkSize {
				wg.Add(1)
				go func(off int64) {
					defer wg.Done()
					n, err := pi.WriteAt(data[off:off+chunkSize], off)
					if err != nil {
						panic(err)
					}
					if n != chunkSize {
						panic(n)
					}
				}(off)
			}
			wg.Wait()
			if capacity == 0 {
				pi.MarkNotComplete()
			}
			// This might not apply if users of this benchmark don't cache with the expected capacity.
			c.Assert(pi.Completion(), qt.Equals, storage.Completion{Complete: false, Ok: true})
			c.Assert(pi.MarkComplete(), qt.IsNil)
			c.Assert(pi.Completion(), qt.Equals, storage.Completion{true, true})
			readData, err := ioutil.ReadAll(io.NewSectionReader(pi, 0, int64(len(data))))
			c.Assert(err, qt.IsNil)
			c.Assert(len(readData), qt.Equals, len(data))
			c.Assert(bytes.Equal(readData, data), qt.IsTrue)
		}
	}
	// Fill the cache
	if capacity > 0 {
		for range iter.N(int((capacity + info.TotalLength() - 1) / info.TotalLength())) {
			oneIter()
		}
	}
	b.ResetTimer()
	for range iter.N(b.N) {
		oneIter()
	}
}
