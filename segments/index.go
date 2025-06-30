package segments

import (
	"iter"
	"sort"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
)

func NewIndex(segments LengthIter) (ret Index) {
	var start Length
	for l, ok := segments(); ok; l, ok = segments() {
		ret.segments = append(ret.segments, Extent{start, l})
		start += l
	}
	return
}

type Index struct {
	segments []Extent
}

func NewIndexFromSegments(segments []Extent) Index {
	return Index{segments}
}

func (me Index) iterSegments() func() (Extent, bool) {
	var lastEnd g.Option[Int]
	return func() (ret Extent, ok bool) {
		if len(me.segments) == 0 {
			return
		}
		cur := me.segments[0]
		me.segments = me.segments[1:]
		ret.Start = cur.Start - lastEnd.UnwrapOr(cur.Start)
		ret.Length = cur.Length
		lastEnd.Set(cur.End())
		ok = true
		return
	}
}

// Returns true if the callback returns false early, or extents are found in the index for all parts
// of the given extent. TODO: This might not handle discontiguous extents. To be tested. Needed for
// BitTorrent v2 possibly.
func (me Index) Locate(e Extent, output Callback) bool {
	first := sort.Search(len(me.segments), func(i int) bool {
		_e := me.segments[i]
		return _e.End() > e.Start
	})
	if first == len(me.segments) {
		return e.Length == 0
	}
	e.Start -= me.segments[first].Start
	// The extent is before the first segment.
	if e.Start < 0 {
		e.Length += e.Start
		e.Start = 0
	}
	me.segments = me.segments[first:]
	return ScanConsecutive(me.iterSegments(), e, func(i int, e Extent) bool {
		return output(i+first, e)
	})
}

func (me Index) LocateIter(e Extent) iter.Seq2[int, Extent] {
	return func(yield func(int, Extent) bool) {
		first := sort.Search(len(me.segments), func(i int) bool {
			_e := me.segments[i]
			return _e.End() > e.Start
		})
		if first == len(me.segments) {
			return
		}
		e.Start -= me.segments[first].Start
		// The extent is before the first segment.
		if e.Start < 0 {
			e.Length += e.Start
			e.Start = 0
		}
		me.segments = me.segments[first:]
		ScanConsecutive(me.iterSegments(), e, func(i int, e Extent) bool {
			return yield(i+first, e)
		})
	}
}

type IndexAndOffset struct {
	Index  int
	Offset int64
}

// Returns the Extent that contains the given extent, if it exists. Panics if Extents overlap on the
// offset.
func (me Index) LocateOffset(off int64) (ret g.Option[IndexAndOffset]) {
	// I think an Extent needs to have a non-zero to match against it? That's what this method is
	// defining.
	for i, e := range me.LocateIter(Extent{off, 1}) {
		panicif.True(ret.Ok)
		panicif.NotEq(e.Length, 1)
		ret.Set(IndexAndOffset{
			Index:  i,
			Offset: e.Start,
		})
	}
	return
}

func (me Index) Index(i int) Extent {
	return me.segments[i]
}
