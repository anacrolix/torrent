package torrent

import (
	"github.com/anacrolix/torrent/typed-roaring"
)

// Use an iterator to jump between dirty bits.
type undirtiedChunksIter struct {
	TorrentDirtyChunks *typedRoaring.Bitmap[RequestIndex]
	StartRequestIndex  RequestIndex
	EndRequestIndex    RequestIndex
}

func (me *undirtiedChunksIter) Iter(f func(chunkIndexType)) {
	it := me.TorrentDirtyChunks.Iterator()
	startIndex := me.StartRequestIndex
	endIndex := me.EndRequestIndex
	it.AdvanceIfNeeded(startIndex)
	lastDirty := startIndex - 1
	for it.HasNext() {
		next := it.Next()
		if next >= endIndex {
			break
		}
		for index := lastDirty + 1; index < next; index++ {
			f(index - startIndex)
		}
		lastDirty = next
	}
	for index := lastDirty + 1; index < endIndex; index++ {
		f(index - startIndex)
	}
	return
}
