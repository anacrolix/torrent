package segments

import (
	"testing"

	"github.com/stretchr/testify/assert"
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

func assertLocate(t *testing.T, nl newLocater, ls []Length, needle Extent, firstExpectedIndex int, expectedExtents []Extent) {
	var actual collectExtents
	var expected collectExtents
	for i, e := range expectedExtents {
		expected.scanCallback(firstExpectedIndex+i, e)
	}
	nl(LengthIterFromSlice(ls))(needle, actual.scanCallback)
	assert.EqualValues(t, expected, actual)
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
}

func TestScan(t *testing.T) {
	testLocater(t, LocaterFromLengthIter)
}

func TestIndex(t *testing.T) {
	testLocater(t, func(li LengthIter) Locater {
		return NewIndex(li).Locate
	})
}
