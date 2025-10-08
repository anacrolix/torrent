package indexed

import (
	"cmp"
	"slices"
	"testing"
	"time"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/generics/option"
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
	var idx Table2[overdueRecord, int]
	idx.Init(func(a, b MapRecord[overdueRecord, int]) int {
		return cmp.Or(overdueRecordIndexCompare(a.Key, b.Key), cmp.Compare(a.Value, b.Value))
	})
	a.OnChange(func(old, new g.Option[MapRecord[int, overdueRecord]]) {
		oldFlipped := option.Map(MapRecord[int, overdueRecord].Flip, old)
		newFlipped := option.Map(MapRecord[int, overdueRecord].Flip, new)
		idx.Change(oldFlipped, newFlipped)
	})
	qt.Assert(t, qt.CmpEquals(nil, slices.Collect(a.IterFirst())))
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
	itered := slices.Collect(a.IterFirst())
	qt.Assert(t, qt.HasLen(itered, len(rows)))
	iteredPks := slices.Collect(idx.IterSecond())
	qt.Assert(t, qt.CmpEquals([]int{0, 5, 1, 2, 6, 3, 4, 7}, iteredPks))
	var overdue []int
	for rowid := range idx.IterFirstRangeToSecond(overdueRecord{}, overdueRecord{when: time.Now().Add(1)}) {
		overdue = append(overdue, rowid)
	}
	qt.Assert(t, qt.CmpEquals(overdue, []int{6, 3}))
	for _, pk := range overdue {
		a.Update(pk, func(r overdueRecord) overdueRecord {
			r.overdue = true
			return r
		})
	}
	qt.Assert(t, qt.CmpEquals([]int{0, 5, 6, 1, 3, 2, 4, 7}, slices.Collect(idx.IterSecond())))
}

type orderedPrimaryKey[T constraints.Ordered] struct {
	inner T
}

func (me orderedPrimaryKey[T]) Compare(other orderedPrimaryKey[T]) int {
	return cmp.Compare(me.inner, other.inner)
}

func TestCreateOrUpdate(t *testing.T) {
	var a Table[int]
	a.Init(cmp.Compare[int])
	a.CreateOrUpdate(1, func(r int) int {
		panicif.NotEq(r, 0)
		return r
	})
}
