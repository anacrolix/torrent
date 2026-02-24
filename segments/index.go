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

// Yields segments as extents with Start relative to the previous segment's end.
func (me Index) iterSegments(startIndex int) iter.Seq[Extent] {
	return func(yield func(Extent) bool) {
		var lastEnd g.Option[Int]
		for _, cur := range me.segments[startIndex:] {
			ret := Extent{
				// Why ignore initial start on the first segment?
				Start:  cur.Start - lastEnd.UnwrapOr(cur.Start),
				Length: cur.Length,
			}
			lastEnd.Set(cur.End())
			if !yield(ret) {
				return
			}
		}
	}
}

func (me Index) LocateIter(e Extent) iter.Seq2[int, Extent] {
	return func(yield func(int, Extent) bool) {
		// We find the first segment that ends after the start of the target extent.
		first, eq := slices.BinarySearchFunc(me.segments, e.Start, func(elem Extent, target Int) int {
			return cmp.Compare(elem.End(), target+1)
		})
		//fmt.Printf("binary search for %v in %v returned %v\n", e.Start, me.segments, first)
		if first == len(me.segments) {
			return
		}
		_ = eq
		e.Start -= me.segments[first].Start
		// The extent is before the first segment.
		if e.Start < 0 {
			e.Length += e.Start
			e.Start = 0
		}
		i := first
		for cons := range scanConsecutive(me.iterSegments(first), e) {
			if !yield(i, cons) {
				return
			}
			i++
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
