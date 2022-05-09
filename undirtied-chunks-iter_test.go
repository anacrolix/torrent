package torrent

import (
	"testing"

	typedRoaring "github.com/anacrolix/torrent/typed-roaring"
)

func BenchmarkUndirtiedChunksIter(b *testing.B) {
	var bitmap typedRoaring.Bitmap[RequestIndex]
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
