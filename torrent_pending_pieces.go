package torrent

import (
	"github.com/elliotchance/orderedmap"

	"github.com/anacrolix/missinggo/v2/prioritybitmap"
)

type stableSet struct {
	om *orderedmap.OrderedMap
}

func (s stableSet) Has(bit int) bool {
	_, ok := s.om.Get(bit)
	return ok
}

func (s stableSet) Delete(bit int) {
	s.om.Delete(bit)
}

func (s stableSet) Len() int {
	return s.om.Len()
}

func (s stableSet) Set(bit int) {
	s.om.Set(bit, struct{}{})
}

func (s stableSet) Range(f func(int) bool) {
	for e := s.om.Front(); e != nil; e = e.Next() {
		if !f(e.Key.(int)) {
			break
		}
	}
}

func priorityBitmapStableNewSet() prioritybitmap.Set {
	return stableSet{om: orderedmap.NewOrderedMap()}
}
