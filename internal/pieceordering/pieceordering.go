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
		me.removeKeyPiece(existingKey, piece)
	}
	me.sl.Insert(key, piece)
	if me.pieceKeys == nil {
		me.pieceKeys = make(map[int]int)
	}
	me.pieceKeys[piece] = key
}

func (me *Instance) removeKeyPiece(key, piece int) {
	if me.sl.Remove(key).Value.(int) != piece {
		panic("piecekeys map lied to us")
	}
	if me.sl.Remove(key) != nil {
		panic("duplicate key")
	}
}

func (me *Instance) DeletePiece(piece int) {
	key, ok := me.pieceKeys[piece]
	if !ok {
		return
	}
	me.removeKeyPiece(key, piece)
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
