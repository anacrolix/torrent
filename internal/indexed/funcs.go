package indexed

import (
	"fmt"
	"iter"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/internal/amortize"
)

// Iters from a point, assuming that where can only be true consecutively from that point and
// nowhere else.
func IterClusteredWhere[R any](t relation[R], gte R, where func(r R) bool) Iter[R] {
	return func(yield func(R) bool) {
		var first g.Option[R]
		for r := range t.IterFrom(gte) {
			if !where(r) {
				break
			}
			if !first.Ok {
				first.Set(r)
			}
			if !yield(r) {
				break
			}
		}
		checkWhereGotFirst(t, first, where)
	}
}

func checkWhereGotFirst[R any](me relation[R], first g.Option[R], where func(r R) bool) {
	if !amortize.Try() {
		return
	}
	var slowRet g.Option[R]
	for r := range me.Iter() {
		if where(r) {
			slowRet.Set(r)
			break
		}
	}
	if first.Ok != slowRet.Ok || first.Ok && me.GetCmp()(first.Value, slowRet.Value) != 0 {
		fmt.Printf("%#v\n", first.Value)
		fmt.Printf("%#v\n", slowRet.Value)
		panic("herp")
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
