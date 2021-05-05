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

const (
	ChunkSize        = 1 << 14
	DefaultPieceSize = 2 << 20
	DefaultCapacity  = 0
	DefaultNumPieces = 16
)

func BenchmarkPieceMarkComplete(
	b *testing.B, ci storage.ClientImpl,
	pieceSize int64, numPieces int,
	// This drives any special handling around capacity that may be configured into the storage
	// implementation.
	capacity int64,
) {
	const check = true
	c := qt.New(b)
	info := &metainfo.Info{
		Pieces:      make([]byte, numPieces*metainfo.HashSize),
		PieceLength: pieceSize,
		Length:      pieceSize * int64(numPieces),
		Name:        "TorrentName",
	}
	ti, err := ci.OpenTorrent(info, metainfo.Hash{})
	c.Assert(err, qt.IsNil)
	defer ti.Close()
	rand.Read(info.Pieces)
	data := make([]byte, pieceSize)
	b.SetBytes(int64(numPieces) * pieceSize)
	oneIter := func() {
		for pieceIndex := range iter.N(numPieces) {
			pi := ti.Piece(info.Piece(pieceIndex))
			if check {
				rand.Read(data)
			}
			var wg sync.WaitGroup
			for off := int64(0); off < int64(len(data)); off += ChunkSize {
				wg.Add(1)
				go func(off int64) {
					defer wg.Done()
					n, err := pi.WriteAt(data[off:off+ChunkSize], off)
					if err != nil {
						panic(err)
					}
					if n != ChunkSize {
						panic(n)
					}
				}(off)
			}
			wg.Wait()
			b.StopTimer()
			if capacity == 0 {
				pi.MarkNotComplete()
			}
			b.StartTimer()
			// This might not apply if users of this benchmark don't cache with the expected capacity.
			c.Assert(pi.Completion(), qt.Equals, storage.Completion{Complete: false, Ok: true})
			c.Assert(pi.MarkComplete(), qt.IsNil)
			c.Assert(pi.Completion(), qt.Equals, storage.Completion{true, true})
			if check {
				readData, err := ioutil.ReadAll(io.NewSectionReader(pi, 0, int64(len(data))))
				c.Check(err, qt.IsNil)
				c.Assert(len(readData), qt.Equals, len(data))
				c.Assert(bytes.Equal(readData, data), qt.IsTrue)
			}
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
