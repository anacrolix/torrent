package indexed

import (
	"iter"

	g "github.com/anacrolix/generics"
)

type tableInterface[R any] interface {
	OnChange(t triggerFunc[R])
	addIndex(index genericRelation, trigger triggerFunc[R])
	relation[R]
}

type relation[R any] interface {
	genericRelation
	GetGte(gte R) g.Option[R]
	Iter(yield func(R) bool)
	// Should this be done using MinRecord to force the logic to be tested?
	IterFrom(gte R) iter.Seq[R]
	MinRecord() R
	GetCmp() CompareFunc[R]
	Poison()
	Unpoison()
}

type genericRelation interface {
	Len() int
}
