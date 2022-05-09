package torrent

import (
	"testing"

	"github.com/RoaringBitmap/roaring"
)

func BenchmarkUndirtiedChunksIter(b *testing.B) {
	var bitmap roaring.Bitmap
	a := undirtiedChunksIter{
		TorrentDirtyChunks: &bitmap,
		StartRequestIndex:  69,
		EndRequestIndex:    420,
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		a.Iter(func(chunkIndex chunkIndexType) {

		})
	}
}
