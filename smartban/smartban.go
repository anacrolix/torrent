package smartban

import (
	"sync"

	g "github.com/anacrolix/generics"
)

type Cache[Peer, BlockKey, Hash comparable] struct {
	Hash func([]byte) Hash

	lock   sync.RWMutex
	blocks map[BlockKey][]peerAndHash[Peer, Hash]
}

type Block[Key any] struct {
	Key  Key
	Data []byte
}

type peerAndHash[Peer, Hash any] struct {
	Peer Peer
	Hash Hash
}

func (me *Cache[Peer, BlockKey, Hash]) Init() {
	g.MakeMap(&me.blocks)
}

func (me *Cache[Peer, BlockKey, Hash]) RecordBlock(peer Peer, key BlockKey, data []byte) {
	hash := me.Hash(data)
	me.lock.Lock()
	defer me.lock.Unlock()
	peers := me.blocks[key]
	peers = append(peers, peerAndHash[Peer, Hash]{peer, hash})
	me.blocks[key] = peers
}

func (me *Cache[Peer, BlockKey, Hash]) CheckBlock(key BlockKey, data []byte) (bad []Peer) {
	correct := me.Hash(data)
	me.lock.RLock()
	defer me.lock.RUnlock()
	for _, item := range me.blocks[key] {
		if item.Hash != correct {
			bad = append(bad, item.Peer)
		}
	}
	return
}

func (me *Cache[Peer, BlockKey, Hash]) ForgetBlock(key BlockKey) {
	me.lock.Lock()
	defer me.lock.Unlock()
	delete(me.blocks, key)
}

func (me *Cache[Peer, BlockKey, Hash]) HasBlocks() bool {
	me.lock.RLock()
	defer me.lock.RUnlock()
	return len(me.blocks) != 0
}
