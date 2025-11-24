package indexed

import (
	"github.com/ajwerner/btree"
)

type btreeIterator[R any] struct {
	btree.Iterator[R, struct{}]
	version int
}
