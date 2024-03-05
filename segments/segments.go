package segments

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
	LengthIter            = func() (Length, bool)
	ConsecutiveExtentIter = func() (Extent, bool)
)

// Returns true if callback returns false early, or all segments in the haystack for the needle are
// found.
func Scan(haystack LengthIter, needle Extent, callback Callback) bool {
	return ScanConsecutive(
		func() (Extent, bool) {
			l, ok := haystack()
			return Extent{0, l}, ok
		},
		needle,
		callback,
	)
}

// Returns true if callback returns false early, or all segments in the haystack for the needle are
// found.
func ScanConsecutive(haystack ConsecutiveExtentIter, needle Extent, callback Callback) bool {
	i := 0
	// Extents have been found in the haystack and we're waiting for the needle to end. This is kind
	// of for backwards compatibility for some tests that expect to have zero-length extents.
	startedNeedle := false
	for needle.Length != 0 {
		l, ok := haystack()
		if !ok {
			return false
		}

		e1 := Extent{
			Start: max(needle.Start-l.Start, 0),
		}
		e1.Length = max(min(l.Length, needle.End()-l.Start)-e1.Start, 0)
		needle.Start = max(0, needle.Start-l.End())
		needle.Length -= e1.Length + l.Start
		if e1.Length > 0 || (startedNeedle && needle.Length != 0) {
			if !callback(i, e1) {
				return true
			}
			startedNeedle = true
		}
		i++
	}
	return true
}

func LocaterFromLengthIter(li LengthIter) Locater {
	return func(e Extent, c Callback) bool {
		return Scan(li, e, c)
	}
}

type Locater func(Extent, Callback) bool
