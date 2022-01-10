package option

type T[V any] struct {
	ok    bool
	value V
}

func (me *T[V]) Ok() bool {
	return me.ok
}

func (me *T[V]) Value() V {
	if !me.ok {
		panic("not set")
	}
	return me.value
}

func Some[V any](value V) T[V] {
	return T[V]{ok: true, value: value}
}
