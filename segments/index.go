package segments

import (
	"sort"
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

func (me Index) iterSegments() func() (Length, bool) {
	return func() (Length, bool) {
		if len(me.segments) == 0 {
			return 0, false
		} else {
			l := me.segments[0].Length
			me.segments = me.segments[1:]
			return l, true
		}
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
		return false
	}
	e.Start -= me.segments[first].Start
	me.segments = me.segments[first:]
	return Scan(me.iterSegments(), e, func(i int, e Extent) bool {
		return output(i+first, e)
	})
}
