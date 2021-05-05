package test_storage

import (
	"bytes"
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
	c := qt.New(b)
	info := &metainfo.Info{
		Pieces:      make([]byte, numPieces*metainfo.HashSize),
		PieceLength: pieceSize,
		Length:      pieceSize * int64(numPieces),
		Name:        "TorrentName",
	}
	ti, err := ci.OpenTorrent(info, metainfo.Hash{})
	c.Assert(err, qt.IsNil)
	tw := storage.Torrent{ti}
	defer tw.Close()
	rand.Read(info.Pieces)
	data := make([]byte, pieceSize)
	readData := make([]byte, pieceSize)
	b.SetBytes(int64(numPieces) * pieceSize)
	oneIter := func() {
		for pieceIndex := range iter.N(numPieces) {
			pi := tw.Piece(info.Piece(pieceIndex))
			rand.Read(data)
			b.StartTimer()
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
			if capacity == 0 {
				pi.MarkNotComplete()
			}
			// This might not apply if users of this benchmark don't cache with the expected capacity.
			c.Assert(pi.Completion(), qt.Equals, storage.Completion{Complete: false, Ok: true})
			c.Assert(pi.MarkComplete(), qt.IsNil)
			c.Assert(pi.Completion(), qt.Equals, storage.Completion{true, true})
			n, err := pi.WriteTo(bytes.NewBuffer(readData[:0]))
			b.StopTimer()
			c.Assert(err, qt.IsNil)
			c.Assert(n, qt.Equals, int64(len(data)))
			c.Assert(bytes.Equal(readData[:n], data), qt.IsTrue)
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
