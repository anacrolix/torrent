package indexed

import (
	"iter"
)

type tableInterface[R any] interface {
	OnChange(t triggerFunc[R])
	addIndex(index genericRelation, trigger triggerFunc[R])
	relation[R]
}

type relation[R any] interface {
	genericRelation
	Iter() iter.Seq[R]
	// Should this be done using MinRecord to force the logic to be tested?
	IterFrom(gte R) iter.Seq[R]
	MinRecord() R
	SetMinRecord(R)
	GetCmp() CompareFunc[R]
}

type genericRelation interface {
	Len() int
}
