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

type Locater func(Extent, Callback) bool
