package torrent

import (
	typedRoaring "github.com/anacrolix/torrent/typed-roaring"
)

func iterBitmapUnsetInRange[T typedRoaring.BitConstraint](
	it *typedRoaring.Iterator[T],
	start, end T,
	f func(T),
) {
	it.AdvanceIfNeeded(start)
	lastDirty := start - 1
	for it.HasNext() {
		next := it.Next()
		if next >= end {
			break
		}
		for index := lastDirty + 1; index < next; index++ {
			f(index)
		}
		lastDirty = next
	}
	for index := lastDirty + 1; index < end; index++ {
		f(index)
	}
}
