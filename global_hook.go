package torrent

import "expvar"

type expvarHookMap struct {
	m          *expvar.Map
	addHandler func(key string, delta int64)
}

func newExpvarHookMap(name string) *expvarHookMap {
	return &expvarHookMap{m: expvar.NewMap(name)}
}

func (e *expvarHookMap) String() string {
	return e.m.String()
}

func (e *expvarHookMap) Set(key string, value expvar.Var) {
	e.m.Set(key, value)
}

func (e *expvarHookMap) Add(key string, delta int64) {
	if e.addHandler != nil {
		e.addHandler(key, delta)
	}
	e.m.Add(key, delta)
}

func (e *expvarHookMap) HandleAddHook(handler func(key string, delta int64)) {
	e.addHandler = handler
}

type expvarHookInt struct {
	i          *expvar.Int
	addHandler func(delta int64)
}

func newExpvarHookInt(name string) *expvarHookInt {
	return &expvarHookInt{i: expvar.NewInt(name)}
}

func (e *expvarHookInt) String() string {
	return e.i.String()
}

func (e *expvarHookInt) Add(delta int64) {
	if e.addHandler != nil {
		e.addHandler(delta)
	}
	e.i.Add(delta)
}

func (e *expvarHookInt) HandleAddHook(handler func(delta int64)) {
	e.addHandler = handler
}
