package storage

import (
	"sync"

	"github.com/anacrolix/torrent/metainfo"
)

// Wraps a persistent completion store so runtime completion changes can become immediately visible
// without forcing per-piece durability work on the hot download path.
type bufferedPieceCompletion struct {
	underlying PieceCompletion

	mu      sync.RWMutex
	overlay map[metainfo.PieceKey]bool
	dirty   map[metainfo.PieceKey]struct{}
}

var _ interface {
	PieceCompletion
	PieceCompletionCheckpointer
	PieceCompletionPersistenter
	PieceCompletionTorrentDeleter
} = (*bufferedPieceCompletion)(nil)

func newBufferedPieceCompletion(underlying PieceCompletion) PieceCompletion {
	return &bufferedPieceCompletion{
		underlying: underlying,
	}
}

func (me *bufferedPieceCompletion) Persistent() bool {
	// Runtime updates are checkpointed in batches instead of requiring synchronous durability for
	// every piece completion event.
	return false
}

func (me *bufferedPieceCompletion) Get(pk metainfo.PieceKey) (c Completion, err error) {
	me.mu.RLock()
	value, ok := me.overlay[pk]
	me.mu.RUnlock()
	if ok {
		c.Ok = true
		c.Complete = value
		return
	}
	return me.underlying.Get(pk)
}

func (me *bufferedPieceCompletion) Set(pk metainfo.PieceKey, complete bool) error {
	me.mu.Lock()
	if me.overlay == nil {
		me.overlay = make(map[metainfo.PieceKey]bool)
	}
	me.overlay[pk] = complete
	if complete {
		if me.dirty == nil {
			me.dirty = make(map[metainfo.PieceKey]struct{})
		}
		me.dirty[pk] = struct{}{}
		me.mu.Unlock()
		return nil
	}
	delete(me.dirty, pk)
	me.mu.Unlock()

	if err := me.persistChanges([]PieceCompletionChange{{
		Key:      pk,
		Complete: false,
	}}); err != nil {
		return err
	}

	me.mu.Lock()
	// Underlying storage now matches the runtime view for this key.
	if current, ok := me.overlay[pk]; ok && !current {
		delete(me.overlay, pk)
	}
	me.mu.Unlock()
	return nil
}

func (me *bufferedPieceCompletion) Checkpoint(keys []metainfo.PieceKey) error {
	me.mu.RLock()
	changes := make([]PieceCompletionChange, 0, len(keys))
	for _, key := range keys {
		if _, ok := me.dirty[key]; !ok {
			continue
		}
		value, ok := me.overlay[key]
		if !ok {
			continue
		}
		changes = append(changes, PieceCompletionChange{
			Key:      key,
			Complete: value,
		})
	}
	me.mu.RUnlock()

	if len(changes) == 0 {
		return nil
	}
	if err := me.persistChanges(changes); err != nil {
		return err
	}

	me.mu.Lock()
	for _, change := range changes {
		delete(me.dirty, change.Key)
		if current, ok := me.overlay[change.Key]; ok && current == change.Complete {
			delete(me.overlay, change.Key)
		}
	}
	me.mu.Unlock()
	return nil
}

func (me *bufferedPieceCompletion) Close() error {
	me.mu.RLock()
	keys := make([]metainfo.PieceKey, 0, len(me.dirty))
	for key := range me.dirty {
		keys = append(keys, key)
	}
	me.mu.RUnlock()
	if err := me.Checkpoint(keys); err != nil {
		return err
	}
	return me.underlying.Close()
}

func (me *bufferedPieceCompletion) DeleteTorrent(infoHash metainfo.Hash) error {
	me.mu.Lock()
	for key := range me.overlay {
		if key.InfoHash == infoHash {
			delete(me.overlay, key)
		}
	}
	for key := range me.dirty {
		if key.InfoHash == infoHash {
			delete(me.dirty, key)
		}
	}
	me.mu.Unlock()

	if deleter, ok := me.underlying.(PieceCompletionTorrentDeleter); ok {
		return deleter.DeleteTorrent(infoHash)
	}
	return nil
}

func (me *bufferedPieceCompletion) persistChanges(changes []PieceCompletionChange) error {
	if batchSetter, ok := me.underlying.(PieceCompletionBatchSetter); ok {
		return batchSetter.SetBatch(changes)
	}
	for _, change := range changes {
		if err := me.underlying.Set(change.Key, change.Complete); err != nil {
			return err
		}
	}
	return nil
}
