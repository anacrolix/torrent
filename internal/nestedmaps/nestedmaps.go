package nestedmaps

type next[NK comparable, M ~map[NK]NV, NV any] struct {
	last Path[M]
	key  NK
}

func (me next[NK, CV, NV]) Exists() bool {
	_, ok := me.last.Get()[me.key]
	return ok
}

func (me next[NK, CV, NV]) Get() NV {
	return me.last.Get()[me.key]
}

func (me next[NK, CV, NV]) Set(value NV) {
	if me.last.Get() == nil {
		me.last.Set(make(CV))
	}
	me.last.Get()[me.key] = value
}

func (me next[NK, CV, NV]) Delete() {
	m := me.last.Get()
	delete(m, me.key)
	if len(m) == 0 {
		me.last.Delete()
	}
}

func Next[K comparable, M ~map[K]V, V any](
	last Path[M],
	key K,
) Path[V] {
	ret := next[K, M, V]{}
	ret.last = last
	ret.key = key
	return ret
}

type root[K comparable, V any, M ~map[K]V] struct {
	m *M
}

func (me root[K, V, M]) Exists() bool {
	return *me.m != nil
}

func (me root[K, V, M]) Get() M {
	return *me.m
}

func (me root[K, V, M]) Set(value M) {
	*me.m = value
}

func (me root[K, V, M]) Delete() {
	*me.m = nil
}

func Begin[K comparable, M ~map[K]V, V any](m *M) Path[M] {
	ret := root[K, V, M]{}
	ret.m = m
	return ret
}

type Path[V any] interface {
	Set(V)
	Get() V
	Exists() bool
	Delete()
}
