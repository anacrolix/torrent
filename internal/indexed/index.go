package indexed

import (
	"github.com/ajwerner/btree"
)

type Index[R any] = *indexImpl[R]

type Iterator[R any] btree.SetIterator[R]
