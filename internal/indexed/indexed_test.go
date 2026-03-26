package indexed

import (
	"cmp"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"
)

// Tests that modifying the table from another goroutine during iteration is detected. The goroutine
// completes the modification before the next version check, so the check panics rather than the
// race detector firing — but running with -race would also catch unsynchronized access.
func TestConcurrentModificationDetected(t *testing.T) {
	var a Table[int]
	a.Init(cmp.Compare)
	a.Create(1)
	a.Create(2)
	a.Create(3)

	iterating := make(chan struct{})
	modified := make(chan struct{})

	go func() {
		<-iterating
		a.Create(100)
		close(modified)
	}()

	panicked := func() (panicked bool) {
		defer func() {
			if recover() != nil {
				panicked = true
			}
		}()
		first := true
		for range a.Iter {
			if first {
				first = false
				close(iterating)
				<-modified
			}
		}
		return
	}()

	qt.Assert(t, qt.IsTrue(panicked))
}

func TestUpdateOrCreate(t *testing.T) {
	var a Map[string, int]
	a.Init(cmp.Compare)
	a.AddInsteadOf(func(old, new g.Option[Pair[string, int]]) g.Option[Pair[string, int]] {
		if new.UnwrapOrZeroValue().Right == 0 {
			new.SetNone()
		}
		return new
	})
	created := a.UpdateOrCreate("a", func(old int) int {
		return old + 1
	})
	qt.Assert(t, qt.IsTrue(created))
	v, ok := a.Get("a")
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.Equals(v, 1))
	created = a.UpdateOrCreate("a", func(old int) int {
		return old + 1
	})
	qt.Assert(t, qt.IsFalse(created))
	v, ok = a.Get("a")
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.Equals(v, 2))
	created = a.UpdateOrCreate("a", func(old int) int {
		return 0
	})
	qt.Assert(t, qt.IsFalse(created))
	v, ok = a.Get("a")
	qt.Assert(t, qt.IsFalse(ok))
	qt.Assert(t, qt.Equals(v, 0))
}

type twoIntsOneRow struct {
	key, value int
}

func TestUpdateUnkeyedField(t *testing.T) {
	var a Table[twoIntsOneRow]
	a.Init(func(a, b twoIntsOneRow) int {
		return cmp.Compare(a.key, b.key)
	})
	a.Create(twoIntsOneRow{1, 2})
	a.Create(twoIntsOneRow{2, 3})
	a.Update(twoIntsOneRow{2, 0}, func(r twoIntsOneRow) twoIntsOneRow {
		r.value = 4
		return r
	})
	qt.Assert(t, qt.Equals(a.Len(), 2))
	qt.Check(t, qt.Equals(GetEq(&a, twoIntsOneRow{key: 1}), g.Some(twoIntsOneRow{1, 2})))
	qt.Check(t, qt.Equals(GetEq(&a, twoIntsOneRow{key: 2}), g.Some(twoIntsOneRow{2, 4})))
}
