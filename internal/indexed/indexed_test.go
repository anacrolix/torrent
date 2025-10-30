package indexed

import (
	"cmp"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"
)

func TestUpdateOrCreate(t *testing.T) {
	var a Map[string, int]
	a.Init(cmp.Compare)
	created := a.UpdateOrCreate("a", func(old int) int {
		return old + 1
	})
	a.OnValueChange(func(key string, old, new g.Option[int]) {
		i, _ := a.Get(key)
		if i == 0 {
			a.Delete(key)
		}
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
