package segments

import (
	qt "github.com/frankban/quicktest"
	"testing"
)

func LengthIterFromSlice(ls []Length) LengthIter {
	return func() (Length, bool) {
		switch len(ls) {
		case 0:
			return -1, false
		default:
			l := ls[0]
			ls = ls[1:]
			return l, true
		}
	}
}

type ScanCallbackValue struct {
	Index int
	Extent
}

type collectExtents []ScanCallbackValue

func (me *collectExtents) scanCallback(i int, e Extent) bool {
	*me = append(*me, ScanCallbackValue{
		Index:  i,
		Extent: e,
	})
	return true
}

type newLocater func(LengthIter) Locater

func assertLocate(
	t *testing.T,
	nl newLocater,
	ls []Length,
	needle Extent,
	firstExpectedIndex int,
	expectedExtents []Extent,
) {
	var actual collectExtents
	var expected collectExtents
	for i, e := range expectedExtents {
		expected.scanCallback(firstExpectedIndex+i, e)
	}
	nl(LengthIterFromSlice(ls))(needle, actual.scanCallback)
	qt.Check(t, actual, qt.DeepEquals, expected)
}

func testLocater(t *testing.T, newLocater newLocater) {
	assertLocate(t, newLocater,
		[]Length{1, 0, 2, 0, 3},
		Extent{2, 2},
		2,
		[]Extent{{1, 1}, {0, 0}, {0, 1}})
	assertLocate(t, newLocater,
		[]Length{1, 0, 2, 0, 3},
		Extent{6, 2},
		2,
		[]Extent{})
	assertLocate(t, newLocater,
		[]Length{1652, 1514, 1554, 1618, 1546, 129241752, 1537}, // 128737588
		Extent{0, 16384},
		0,
		[]Extent{
			{0, 1652},
			{0, 1514},
			{0, 1554},
			{0, 1618},
			{0, 1546},
			{0, 8500},
		})
	assertLocate(t, newLocater,
		[]Length{1652, 1514, 1554, 1618, 1546, 129241752, 1537, 1536, 1551}, // 128737588
		Extent{129236992, 16384},
		5,
		[]Extent{
			{129229108, 12644},
			{0, 1537},
			{0, 1536},
			{0, 667},
		})
}

func TestScan(t *testing.T) {
	testLocater(t, LocaterFromLengthIter)
}

func TestIndex(t *testing.T) {
	testLocater(t, func(li LengthIter) Locater {
		return NewIndex(li).Locate
	})
}
