package indexed

import (
	"github.com/ajwerner/btree"
)

type btreeIterator[R any] struct {
	btree.MapIterator[R, struct{}]
	version int
}
