package nestedmaps

import (
	"testing"

	qt "github.com/frankban/quicktest"

	g "github.com/anacrolix/generics"
)

func TestNestedMaps(t *testing.T) {
	c := qt.New(t)
	var nest map[string]map[*int]map[byte][]int64
	intKey := g.PtrTo(420)
	var root = Begin(&nest)
	var first = Next(root, "answer")
	var second = Next(first, intKey)
	var last = Next(second, 69)
	c.Assert(root.Exists(), qt.IsFalse)
	c.Assert(first.Exists(), qt.IsFalse)
	c.Assert(second.Exists(), qt.IsFalse)
	c.Assert(last.Exists(), qt.IsFalse)
	last.Set([]int64{4, 8, 15, 16, 23, 42})
	c.Assert(root.Exists(), qt.IsTrue)
	c.Assert(first.Exists(), qt.IsTrue)
	c.Assert(second.Exists(), qt.IsTrue)
	c.Assert(last.Exists(), qt.IsTrue)
	c.Assert(Next(second, 70).Exists(), qt.IsFalse)
	secondIntKey := g.PtrTo(1337)
	secondPath := Next(Next(Next(Begin(&nest), "answer"), secondIntKey), 42)
	secondPath.Set(nil)
	c.Assert(secondPath.Exists(), qt.IsTrue)
	last.Delete()
	c.Assert(last.Exists(), qt.IsFalse)
	c.Assert(second.Exists(), qt.IsFalse)
	c.Assert(root.Exists(), qt.IsTrue)
	c.Assert(first.Exists(), qt.IsTrue)
	// See if we get panics deleting an already deleted item.
	last.Delete()
	secondPath.Delete()
	c.Assert(root.Exists(), qt.IsFalse)
}
