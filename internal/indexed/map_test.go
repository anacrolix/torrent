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
	rowid   overdueRecordPrimaryKey
	active  bool
	overdue bool
	when    time.Time
}

type overdueRecordPrimaryKey int

func (me overdueRecordPrimaryKey) Compare(other overdueRecordPrimaryKey) int {
	return cmp.Compare(me, other)
}

func TestOverdue(t *testing.T) {
	var a = NewMap(
		TableOps[overdueRecordPrimaryKey, overdueRecord]{
			PrimaryKey: func(r *overdueRecord) *overdueRecordPrimaryKey {
				return &r.rowid
			},
			ComparePrimaryKey: overdueRecordPrimaryKey.Compare,
		})
	idx := a.AddIndex(
		func(l, r overdueRecord) int {
			return cmp.Or(
				extracmp.CompareBool(l.active, r.active),
				-extracmp.CompareBool(l.overdue, r.overdue),
				l.when.Compare(r.when))
		})
	qt.Assert(t, qt.CmpEquals(nil, slices.Collect(a.Iter())))
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
	for i := range rows {
		rows[i].rowid = overdueRecordPrimaryKey(i)
		a.Create(rows[i])
	}
	itered := slices.Collect(a.Iter())
	qt.Assert(t, qt.HasLen(itered, len(rows)))
	iteredPks := slices.Collect(a.IterIndexPrimaryKeys(idx))
	qt.Assert(t, qt.CmpEquals([]overdueRecordPrimaryKey{0, 5, 1, 2, 6, 3, 4, 7}, iteredPks))
	var overdue []overdueRecordPrimaryKey
	for r := range idx.IterRange(overdueRecord{}, overdueRecord{when: time.Now().Add(1)}) {
		overdue = append(overdue, r.rowid)
	}
	qt.Assert(t, qt.CmpEquals(overdue, []overdueRecordPrimaryKey{6, 3}))
	for _, pk := range overdue {
		a.Update(pk, func(r overdueRecord) overdueRecord {
			r.overdue = true
			return r
		})
	}
	qt.Assert(t, qt.CmpEquals([]overdueRecordPrimaryKey{0, 5, 6, 1, 3, 2, 4, 7}, slices.Collect(a.IterIndexPrimaryKeys(idx))))
}

type orderedPrimaryKey[T constraints.Ordered] struct {
	inner T
}

func (me orderedPrimaryKey[T]) Compare(other orderedPrimaryKey[T]) int {
	return cmp.Compare(me.inner, other.inner)
}

func TestCreateOrUpdate(t *testing.T) {
	a := NewMap(
		// Should we keep primary key separate? Hmm...
		TableOps[int, int]{
			PrimaryKey: func(r *int) *int {
				return r
			},
			ComparePrimaryKey: cmp.Compare[int],
		},
	)
	a.CreateOrUpdate(1, func(existed bool, r int) int {
		panicif.NotEq(r, 1)
		return r
	})
}
