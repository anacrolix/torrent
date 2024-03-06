package segments

import (
	"sort"

	g "github.com/anacrolix/generics"
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
// of the given extent.
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
