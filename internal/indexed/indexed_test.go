package indexed

import (
	"cmp"
	"slices"
	"testing"
	"time"

	"github.com/anacrolix/torrent/internal/extracmp"
	"github.com/go-quicktest/qt"
)

type overdueRecordValues struct {
	active  bool
	overdue bool
	when    time.Time
}

type overdueRecordPrimaryKey int

func (me overdueRecordPrimaryKey) Compare(other overdueRecordPrimaryKey) int {
	return cmp.Compare(me, other)
}

type overdueRecord = Record[overdueRecordPrimaryKey, overdueRecordValues]

func TestOverdue(t *testing.T) {
	a := NewMap(func(l, r overdueRecord) int {
		return cmp.Or(
			extracmp.CompareBool(l.Values.active, r.Values.active),
			-extracmp.CompareBool(l.Values.overdue, r.Values.overdue),
			l.Values.when.Compare(r.Values.when))
	})
	qt.Assert(t, qt.CmpEquals(nil, slices.Collect(a.Iter())))
	rows := []overdueRecordValues{
		{overdue: true},
		{overdue: true, when: time.Now().Add(-time.Minute)},
		{overdue: true, when: time.Now().Add(time.Minute)},
		{overdue: false, when: time.Now().Add(-time.Minute)},
		{overdue: false, when: time.Now().Add(time.Minute)},
		{overdue: true},
		{overdue: false},
		{overdue: false, active: true},
	}
	for id, row := range rows {
		a.Upsert(overdueRecordPrimaryKey(id), row)
	}
	itered := slices.Collect(a.Iter())
	qt.Assert(t, qt.HasLen(itered, len(rows)))
	iteredPks := slices.Collect(a.IterPrimaryKeys())
	qt.Assert(t, qt.CmpEquals([]overdueRecordPrimaryKey{0, 5, 1, 2, 6, 3, 4, 7}, iteredPks))
	it := a.Iterator()
	it.SeekGE(overdueRecord{Values: overdueRecordValues{overdue: false}})
	var overdue []overdueRecordPrimaryKey
	for ; it.Valid(); it.Next() {
		if it.Cur().Values.when.After(time.Now()) {
			break
		}
		overdue = append(overdue, it.Cur().PrimaryKey)
	}
	qt.Assert(t, qt.CmpEquals(overdue, []overdueRecordPrimaryKey{6, 3}))
	qt.Assert(t, qt.Equals(it.Value(), struct{}{}))
	for _, pk := range overdue {
		a.Update(pk, func(values *overdueRecordValues) {
			values.overdue = true
		})
	}
	qt.Assert(t, qt.CmpEquals([]overdueRecordPrimaryKey{0, 5, 6, 1, 3, 2, 4, 7}, slices.Collect(a.IterPrimaryKeys())))
}
