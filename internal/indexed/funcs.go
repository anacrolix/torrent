package indexed

import (
	"fmt"
	"iter"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/internal/amortize"
	"github.com/google/go-cmp/cmp"
)

// Iters from a point, assuming that where can only be true consecutively from that point and
// nowhere else.
func IterClusteredWhere[R any](t relation[R], gte R, where func(r R) bool) Iter[R] {
	return func(yield func(R) bool) {
		first := true
		// TODO: This function is allocating..
		checkFirst := func(r g.Option[R]) {
			if !first {
				return
			}
			checkWhereGotFirst(t, r, where)
			first = false
		}
		for r := range t.IterFrom(gte) {
			if !where(r) {
				break
			}
			checkFirst(g.Some(r))
			if !yield(r) {
				// Mustn't do first check after yielding, as the table could have changed.
				return
			}
		}
		checkFirst(g.None[R]())
	}
}

func checkWhereGotFirst[R any](me relation[R], first g.Option[R], where func(r R) bool) {
	if !amortize.Try() {
		return
	}
	var slowRet g.Option[R]
	for r := range me.Iter {
		if where(r) {
			slowRet.Set(r)
			break
		}
	}
	if first.Ok != slowRet.Ok || first.Ok && me.GetCmp()(first.Value, slowRet.Value) != 0 {
		fmt.Printf("%#v\n", first.Value)
		fmt.Printf("%#v\n", slowRet.Value)
		fmt.Printf("diff: %s\n", cmp.Diff(first.Value, slowRet.Value))
		panic("iterating where got different first value than caller")
	}
}

func IterRange[R any](me relation[R], gte, lt R) iter.Seq[R] {
	return func(yield func(R) bool) {
		for r := range me.IterFrom(gte) {
			if me.GetCmp()(r, lt) >= 0 {
				break
			}
			if !yield(r) {
				break
			}
		}
	}
}

func FirstInRange[R any](me relation[R], gte, lt R) (ret g.Option[R]) {
	ret = me.GetGte(gte)
	if ret.Ok && me.GetCmp()(ret.Value, lt) >= 0 {
		ret.SetNone()
	}
	return
}

// Gets a row that compares equal to the given key.
func GetEq[R any](r relation[R], key R) (eq g.Option[R]) {
	eq = r.GetGte(key)
	if !eq.Ok {
		return
	}
	if r.GetCmp()(key, eq.Value) != 0 {
		eq.SetNone()
	}
	return
}
