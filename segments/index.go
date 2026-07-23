package segments

import (
	"cmp"
	"iter"
	"slices"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/missinggo/v2/panicif"
)

func NewIndex(segments LengthIter) (ret Index) {
	var start Length
	for l := range segments {
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

func (me Index) LocateIter(e Extent) iter.Seq2[int, Extent] {
	return func(yield func(int, Extent) bool) {
		// We find the first segment that ends after the start of the target extent.
		first, _ := slices.BinarySearchFunc(me.segments, e.Start, func(elem Extent, target Int) int {
			return cmp.Compare(elem.End(), target+1)
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
		// Inlined scanConsecutive over me.iterSegments(first): walk the segment slice directly to avoid
		// iter.Pull's per-call coroutine allocation. needle, startedNeedle and the per-segment extent
		// math come from scanConsecutive; lastEnd and the relative l.Start come from iterSegments.
		needle := e
		startedNeedle := false
		var lastEnd g.Option[Int]
		for i, cur := range me.segments[first:] {
			if needle.Length == 0 {
				return
			}
			l := Extent{
				Start:  cur.Start - lastEnd.UnwrapOr(cur.Start),
				Length: cur.Length,
			}
			lastEnd.Set(cur.End())

			e1 := Extent{Start: max(needle.Start-l.Start, 0)}
			e1.Length = max(min(l.Length, needle.End()-l.Start)-e1.Start, 0)
			needle.Start = max(0, needle.Start-l.End())
			needle.Length -= e1.Length + l.Start
			if e1.Length > 0 || (startedNeedle && needle.Length != 0) {
				if !yield(first+i, e1) {
					return
				}
				startedNeedle = true
			}
		}
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
