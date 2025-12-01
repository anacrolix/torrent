package indexed

import (
	"github.com/anacrolix/btree"
)

type btreeIterator[R any] struct {
	btree.MapIterator[R, struct{}]
	version int
}
