package indexed

import (
	g "github.com/anacrolix/generics"
)

type triggerFunc[R any] func(old, new g.Option[R])

type CompareFunc[T any] func(a, b T) int

type MapRecord[K, V any] struct {
	Key   K
	Value V
}

func (me MapRecord[K, V]) Flip() MapRecord[V, K] {
	return MapRecord[V, K]{Key: me.Value, Value: me.Key}
}
