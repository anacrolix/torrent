package nestedmaps

import (
	"testing"

	g "github.com/anacrolix/generics"
	qt "github.com/go-quicktest/qt"
)

func TestNestedMaps(t *testing.T) {
	var nest map[string]map[*int]map[byte][]int64
	intKey := g.PtrTo(420)
	var root = Begin(&nest)
	var first = Next(root, "answer")
	var second = Next(first, intKey)
	var last = Next(second, 69)
	qt.Assert(t, qt.IsFalse(root.Exists()))
	qt.Assert(t, qt.IsFalse(first.Exists()))
	qt.Assert(t, qt.IsFalse(second.Exists()))
	qt.Assert(t, qt.IsFalse(last.Exists()))
	last.Set([]int64{4, 8, 15, 16, 23, 42})
	qt.Assert(t, qt.IsTrue(root.Exists()))
	qt.Assert(t, qt.IsTrue(first.Exists()))
	qt.Assert(t, qt.IsTrue(second.Exists()))
	qt.Assert(t, qt.IsTrue(last.Exists()))
	qt.Assert(t, qt.IsFalse(Next(second, 70).Exists()))
	secondIntKey := g.PtrTo(1337)
	secondPath := Next(Next(Next(Begin(&nest), "answer"), secondIntKey), 42)
	secondPath.Set(nil)
	qt.Assert(t, qt.IsTrue(secondPath.Exists()))
	last.Delete()
	qt.Assert(t, qt.IsFalse(last.Exists()))
	qt.Assert(t, qt.IsFalse(second.Exists()))
	qt.Assert(t, qt.IsTrue(root.Exists()))
	qt.Assert(t, qt.IsTrue(first.Exists()))
	// See if we get panics deleting an already deleted item.
	last.Delete()
	secondPath.Delete()
	qt.Assert(t, qt.IsFalse(root.Exists()))
}
