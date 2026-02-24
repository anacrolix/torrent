package indexed

import (
	"cmp"
	"slices"
	"testing"
	"time"

	"github.com/anacrolix/missinggo/v2/panicif"
	"github.com/anacrolix/torrent/internal/extracmp"
	"github.com/go-quicktest/qt"
	"golang.org/x/exp/constraints"
)

type overdueRecord struct {
	active  bool
	overdue bool
	when    time.Time
}

type overdueRecordPrimaryKey int

func (me overdueRecordPrimaryKey) Compare(other overdueRecordPrimaryKey) int {
	return cmp.Compare(me, other)
}

func overdueRecordIndexCompare(l, r overdueRecord) int {
	return cmp.Or(
		extracmp.CompareBool(l.active, r.active),
		-extracmp.CompareBool(l.overdue, r.overdue),
		l.when.Compare(r.when))
}

func TestOverdue(t *testing.T) {
	var a Map[int, overdueRecord]
	a.Init(func(a, b int) int {
		return cmp.Compare(a, b)
	})
	idx := NewFullMappedIndex(
		&a,
		func(a, b Pair[overdueRecord, int]) int {
			return cmp.Or(overdueRecordIndexCompare(a.Left, b.Left), cmp.Compare(a.Right, b.Right))
		},
		Pair[int, overdueRecord].Flip,
		func() (ret Pair[overdueRecord, int]) {
			ret.Left.overdue = true
			return
		}(),
	)
	qt.Assert(t, qt.CmpEquals(nil, slices.Collect(a.Iter)))
	rows := []overdueRecord{
		{overdue: true},
		{overdue: true, when: time.Now().Add(-time.Minute)},
		{overdue: true, when: time.Now().Add(time.Minute)},
		{overdue: false, when: time.Now().Add(-time.Minute)},
		{overdue: false, when: time.Now().Add(time.Minute)},
		{overdue: true},
		{overdue: false},
		{overdue: false, active: true},
	}
	for i, row := range rows {
		panicif.False(a.Create(i, row))
	}
	itered := slices.Collect(MapPairIterLeft(a.Iter))
	qt.Assert(t, qt.HasLen(itered, len(rows)))
	iteredPks := slices.Collect(MapPairIterRight(idx.Iter))
	qt.Assert(t, qt.CmpEquals([]int{0, 5, 1, 2, 6, 3, 4, 7}, iteredPks))
	var overdue []int
	gte := idx.MinRecord()
	gte.Left.overdue = false
	lt := gte
	lt.Left.when = time.Now().Add(1)
	for rowid := range MapPairIterRight(IterRange(idx, gte, lt)) {
		overdue = append(overdue, rowid)
	}
	qt.Assert(t, qt.CmpEquals(overdue, []int{6, 3}))
	for _, pk := range overdue {
		a.Update(pk, func(r overdueRecord) overdueRecord {
			r.overdue = true
			return r
		})
	}
	qt.Assert(t, qt.CmpEquals([]int{0, 5, 6, 1, 3, 2, 4, 7}, slices.Collect(MapPairIterRight(idx.Iter))))
}

type orderedPrimaryKey[T constraints.Ordered] struct {
	inner T
}

func (me orderedPrimaryKey[T]) Compare(other orderedPrimaryKey[T]) int {
	return cmp.Compare(me.inner, other.inner)
}

func TestEnsureAndUpdate(t *testing.T) {
	var a Table[int]
	a.Init(cmp.Compare[int])
	a.Create(1)
	a.Update(1, func(r int) int {
		// Check the record was created with the necessary key values for discovery.
		panicif.NotEq(r, 1)
		return r
	})
}
