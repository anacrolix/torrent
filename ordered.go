package torrent

import (
	"container/list"
)

type OrderedList struct {
	list     *list.List
	lessFunc func(a, b interface{}) bool
}

func (me *OrderedList) Len() int {
	return me.list.Len()
}

func NewList(lessFunc func(a, b interface{}) bool) *OrderedList {
	return &OrderedList{
		list:     list.New(),
		lessFunc: lessFunc,
	}
}

func (me *OrderedList) ValueChanged(e *list.Element) {
	for prev := e.Prev(); prev != nil && me.lessFunc(e.Value, prev.Value); prev = e.Prev() {
		me.list.MoveBefore(e, prev)
	}
	for next := e.Next(); next != nil && me.lessFunc(next.Value, e.Value); next = e.Next() {
		me.list.MoveAfter(e, next)
	}
}

func (me *OrderedList) Insert(value interface{}) (ret *list.Element) {
	ret = me.list.PushFront(value)
	me.ValueChanged(ret)
	return
}

func (me *OrderedList) Front() *list.Element {
	return me.list.Front()
}
