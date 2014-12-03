package pieceordering

import (
	"github.com/glenn-brown/skiplist"
)

type Instance struct {
	sl        *skiplist.T
	pieceKeys map[int]int
}

func New() *Instance {
	return &Instance{
		sl: skiplist.New(),
	}
}

// Add the piece with the given key. No other piece can have the same key. If
// the piece is already present, change its key.
func (me *Instance) SetPiece(piece, key int) {
	if existingKey, ok := me.pieceKeys[piece]; ok {
		if existingKey == key {
			return
		}
		if me.sl.Remove(existingKey).Value.(int) != piece {
			panic("piecekeys map lied to us")
		}
	}
	me.sl.Insert(key, piece)
	if me.pieceKeys == nil {
		me.pieceKeys = make(map[int]int)
	}
	me.pieceKeys[piece] = key
}

func (me *Instance) RemovePiece(piece int) {
	key, ok := me.pieceKeys[piece]
	if !ok {
		return
	}
	el := me.sl.Remove(key)
	if el == nil {
		panic("element not present but should be")
	}
	if me.sl.Remove(key) != nil {
		panic("duplicate key")
	}
	delete(me.pieceKeys, piece)
}

func (me Instance) First() Element {
	e := me.sl.Front()
	if e == nil {
		return nil
	}
	return element{e}
}

type Element interface {
	Piece() int
	Next() Element
}

type element struct {
	sle *skiplist.Element
}

func (e element) Next() Element {
	sle := e.sle.Next()
	if sle == nil {
		return nil
	}
	return element{sle}
}

func (e element) Piece() int {
	return e.sle.Value.(int)
}
