package smartban

import (
	"sync"
)

type Cache[Peer, BlockKey, Hash comparable] struct {
	Hash func([]byte) Hash

	lock   sync.RWMutex
	blocks map[BlockKey]map[Peer]Hash
}

type Block[Key any] struct {
	Key  Key
	Data []byte
}

func (me *Cache[Peer, BlockKey, Hash]) Init() {
	me.blocks = make(map[BlockKey]map[Peer]Hash)
}

func (me *Cache[Peer, BlockKey, Hash]) RecordBlock(peer Peer, key BlockKey, data []byte) {
	hash := me.Hash(data)
	me.lock.Lock()
	defer me.lock.Unlock()
	peers := me.blocks[key]
	if peers == nil {
		peers = make(map[Peer]Hash)
		me.blocks[key] = peers
	}
	peers[peer] = hash
}

func (me *Cache[Peer, BlockKey, Hash]) CheckBlock(key BlockKey, data []byte) (bad []Peer) {
	correct := me.Hash(data)
	me.lock.RLock()
	defer me.lock.RUnlock()
	for peer, hash := range me.blocks[key] {
		if hash != correct {
			bad = append(bad, peer)
		}
	}
	return
}

func (me *Cache[Peer, BlockKey, Hash]) ForgetBlock(key BlockKey) {
	me.lock.Lock()
	defer me.lock.Unlock()
	delete(me.blocks, key)
}
