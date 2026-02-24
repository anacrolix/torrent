package test_storage

import (
	"bytes"
	"context"
	"math/rand"
	"sync"
	"testing"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

const (
	ChunkSize        = 1 << 14
	DefaultPieceSize = 2 << 20
	DefaultNumPieces = 16
)

// This writes chunks to the storage concurrently, and waits for them all to complete. This matches
// the behaviour from the peer connection read loop.
func BenchmarkPieceMarkComplete(
	b *testing.B, ci storage.ClientImpl,
	pieceSize int64, numPieces int,
	// This drives any special handling around capacity that may be configured into the storage
	// implementation.
	capacity int64,
) {
	info := &metainfo.Info{
		Pieces:      make([]byte, numPieces*metainfo.HashSize),
		PieceLength: pieceSize,
		Length:      pieceSize * int64(numPieces),
		Name:        "TorrentName",
	}
	ti, err := ci.OpenTorrent(context.Background(), info, metainfo.Hash{})
	qt.Assert(b, qt.IsNil(err))
	tw := storage.Torrent{ti}
	defer tw.Close()
	rand.Read(info.Pieces)
	data := make([]byte, pieceSize)
	readData := make([]byte, pieceSize)
	b.SetBytes(int64(numPieces) * pieceSize)
	oneIter := func() {
		for pieceIndex := 0; pieceIndex < numPieces; pieceIndex += 1 {
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
			qt.Assert(b, qt.Equals(pi.Completion(), storage.Completion{Complete: false, Ok: true}))
			qt.Assert(b, qt.IsNil(pi.MarkComplete()))
			qt.Assert(b, qt.Equals(pi.Completion(), storage.Completion{Complete: true, Ok: true}))
			n, err := pi.WriteTo(bytes.NewBuffer(readData[:0]))
			b.StopTimer()
			qt.Check(b, qt.Equals(n, int64(len(data))))
			qt.Assert(b, qt.IsNil(err))
			qt.Assert(b, qt.IsTrue(bytes.Equal(readData[:n], data)))
		}
	}
	// Fill the cache
	if capacity > 0 {
		iterN := int((capacity + info.TotalLength() - 1) / info.TotalLength())
		for i := 0; i < iterN; i += 1 {
			oneIter()
		}
	}
	b.StopTimer()
	b.ResetTimer()
	for i := 0; i < b.N; i += 1 {
		oneIter()
	}
}
