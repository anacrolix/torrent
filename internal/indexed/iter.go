package indexed

import (
	"iter"

	g "github.com/anacrolix/generics"
)

type Iter[R any] iter.Seq[R]

func (me Iter[R]) First() (_ g.Option[R]) {
	for r := range me {
		return g.Some(r)
	}
	return
}
