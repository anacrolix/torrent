package transactions

import (
	"encoding/binary"
	"sync"
)

type IdIssuer interface {
	Issue() Id
}

var DefaultIdIssuer varintIdIssuer

type varintIdIssuer struct {
	mu   sync.Mutex
	buf  [binary.MaxVarintLen64]byte
	next uint64
}

func (me *varintIdIssuer) Issue() Id {
	me.mu.Lock()
	n := binary.PutUvarint(me.buf[:], me.next)
	me.next++
	id := string(me.buf[:n])
	me.mu.Unlock()
	return id
}
