package smartban

import (
	"iter"
	"sync"

	g "github.com/anacrolix/generics"
)

type Cache[Peer, BlockKey, Hash comparable] struct {
	Hash func([]byte) Hash

	// Wonder if we should make this an atomic.
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

func (me *Cache[Peer, BlockKey, Hash]) ForgetBlockSeq(seq iter.Seq[BlockKey]) {
	me.lock.Lock()
	defer me.lock.Unlock()
	if len(me.blocks) == 0 {
		return
	}
	for key := range seq {
		delete(me.blocks, key)
	}
}

// Returns whether any block in the sequence has at least once peer recorded.
func (me *Cache[Peer, BlockKey, Hash]) HasPeerForBlocks(seq iter.Seq[BlockKey]) bool {
	me.lock.RLock()
	defer me.lock.RUnlock()
	if len(me.blocks) == 0 {
		return false
	}
	for key := range seq {
		if len(me.blocks[key]) != 0 {
			return true
		}
	}
	return false
}

func (me *Cache[Peer, BlockKey, Hash]) HasBlocks() bool {
	me.lock.RLock()
	defer me.lock.RUnlock()
	return len(me.blocks) != 0
}
