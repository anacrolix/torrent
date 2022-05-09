package torrent

import (
	"github.com/anacrolix/torrent/typed-roaring"
)

// Return the number of bits set in the range. To do this we need the rank of the item before the
// first, and the rank of the last item. An off-by-one minefield. Hopefully I haven't missed
// something in roaring's API that provides this.
func roaringBitmapRangeCardinality[T typedRoaring.BitConstraint](bm interface{ Rank(T) uint64 }, start, end T) (card uint64) {
	card = bm.Rank(end - 1)
	if start != 0 {
		card -= bm.Rank(start - 1)
	}
	return
}
