package segments

import (
	"iter"
)

type Int = int64

type Length = Int

type Extent struct {
	Start, Length Int
}

func (e Extent) End() Int {
	return e.Start + e.Length
}

type (
	Callback              = func(segmentIndex int, segmentBounds Extent) bool
	LengthIter            = iter.Seq[Length]
	ConsecutiveExtentIter = iter.Seq[Extent]
)

// TODO: Does this handle discontiguous extents?
func scanConsecutive(haystack ConsecutiveExtentIter, needle Extent) iter.Seq[Extent] {
	return func(yield func(Extent) bool) {
		// Extents have been found in the haystack, and we're waiting for the needle to end. This is
		// kind of for backwards compatibility for some tests that expect to have zero-length extents.
		startedNeedle := false
		next, stop := iter.Pull(haystack)
		defer stop()
		for needle.Length != 0 {
			l, ok := next()
			if !ok {
				return
			}

			e1 := Extent{
				Start: max(needle.Start-l.Start, 0),
			}
			e1.Length = max(min(l.Length, needle.End()-l.Start)-e1.Start, 0)
			needle.Start = max(0, needle.Start-l.End())
			needle.Length -= e1.Length + l.Start
			if e1.Length > 0 || (startedNeedle && needle.Length != 0) {
				if !yield(e1) {
					return
				}
				startedNeedle = true
			}
		}
	}
}

type Locater func(Extent, Callback) bool
