package indexed

import (
	g "github.com/anacrolix/generics"
)

type Index[R any] interface {
	relation[R]
	SelectFirstIf(gte R, filter func(r R) bool) g.Option[R]
	SelectFirstWhere(gte R, filter func(r R) bool) g.Option[R]
	GetFirst() (R, bool)
}
