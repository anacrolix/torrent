package segments

import (
	"slices"
	"testing"

	"github.com/go-quicktest/qt"
)

func LengthIterFromSlice(ls []Length) LengthIter {
	return slices.Values(ls)
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

func checkContiguous(
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
	qt.Check(t, qt.DeepEquals(actual, expected))
}

func testLocater(t *testing.T, newLocater newLocater) {
	checkContiguous(t, newLocater,
		[]Length{1, 0, 2, 0, 3},
		Extent{2, 2},
		2,
		[]Extent{{1, 1}, {0, 0}, {0, 1}})
	checkContiguous(t, newLocater,
		[]Length{1, 0, 2, 0, 3},
		Extent{6, 2},
		2,
		[]Extent{})
	checkContiguous(t, newLocater,
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
	checkContiguous(t, newLocater,
		[]Length{1652, 1514, 1554, 1618, 1546, 129241752, 1537, 1536, 1551}, // 128737588
		Extent{129236992, 16384},
		5,
		[]Extent{
			{129229108, 12644},
			{0, 1537},
			{0, 1536},
			{0, 667},
		})
	checkContiguous(t, newLocater,
		[]Length{0, 2, 0, 2, 0}, // 128737588
		Extent{1, 2},
		1,
		[]Extent{
			{1, 1},
			{0, 0},
			{0, 1},
		})
	checkContiguous(t, newLocater,
		[]Length{2, 0, 2, 0}, // 128737588
		Extent{1, 3},
		0,
		[]Extent{
			{1, 1},
			{0, 0},
			{0, 2},
		})
	checkContiguous(t, newLocater,
		[]Length{2, 0, 1, 0, 0, 1},
		Extent{3, 2},
		5,
		[]Extent{
			{0, 1},
		})
	checkContiguous(t, newLocater,
		[]Length{2, 0, 1, 0, 0, 1},
		Extent{2, 2},
		2,
		[]Extent{
			{0, 1},
			{0, 0},
			{0, 0},
			{0, 1},
		})
	checkContiguous(t, newLocater,
		[]Length{},
		Extent{1, 1},
		0,
		[]Extent{})
	checkContiguous(t, newLocater,
		[]Length{0},
		Extent{1, 1},
		0,
		[]Extent{})
}

func TestIndexLocateIter(t *testing.T) {
	testLocater(t, func(li LengthIter) Locater {
		index := NewIndex(li)
		return func(extent Extent, callback Callback) bool {
			for i, e := range index.LocateIter(extent) {
				if !callback(i, e) {
					return false
				}
			}
			return true
		}
	})
}
