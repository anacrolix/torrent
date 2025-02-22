package transactions

type Dispatcher[S any] struct {
	txns map[Key]S
}

func (me *Dispatcher[S]) NumActive() int {
	return len(me.txns)
}

func (me *Dispatcher[S]) Add(key Key, state S) {
	_, ok := me.txns[key]
	if ok {
		panic(key)
	}
	if me.txns == nil {
		me.txns = make(map[Key]S)
	}
	me.txns[key] = state
}

func (me *Dispatcher[S]) Pop(key Key) S {
	s, ok := me.txns[key]
	if !ok {
		panic(key)
	}
	delete(me.txns, key)
	return s
}

func (me *Dispatcher[S]) Have(key Key) bool {
	_, ok := me.txns[key]
	return ok
}

func (me *Dispatcher[S]) Delete(key Key) bool {
	have := me.Have(key)
	delete(me.txns, key)
	return have
}
