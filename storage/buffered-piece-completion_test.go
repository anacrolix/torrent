package storage

import (
	"sync"
	"testing"

	g "github.com/anacrolix/generics"
	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

type recordingPieceCompletion struct {
	mu              sync.Mutex
	state           map[metainfo.PieceKey]bool
	batch           []PieceCompletionChange
	persistent      bool
	deletedTorrents []metainfo.Hash
}

func (me *recordingPieceCompletion) Get(pk metainfo.PieceKey) (c Completion, err error) {
	me.mu.Lock()
	defer me.mu.Unlock()
	if me.state == nil {
		return
	}
	value, ok := me.state[pk]
	if !ok {
		return
	}
	c.Ok = true
	c.Complete = value
	return
}

func (me *recordingPieceCompletion) Set(pk metainfo.PieceKey, complete g.Option[bool]) error {
	if !complete.Ok {
		me.mu.Lock()
		delete(me.state, pk)
		me.mu.Unlock()
		return nil
	}
	return me.SetBatch([]PieceCompletionChange{{
		Key:      pk,
		Complete: complete.Value,
	}})
}

func (me *recordingPieceCompletion) SetBatch(changes []PieceCompletionChange) error {
	me.mu.Lock()
	defer me.mu.Unlock()
	if me.state == nil {
		me.state = make(map[metainfo.PieceKey]bool)
	}
	for _, change := range changes {
		me.state[change.Key] = change.Complete
		me.batch = append(me.batch, change)
	}
	return nil
}

func (me *recordingPieceCompletion) Close() error {
	return nil
}

func (me *recordingPieceCompletion) Persistent() bool {
	return me.persistent
}

func (me *recordingPieceCompletion) DeleteTorrent(infoHash metainfo.Hash) error {
	me.mu.Lock()
	defer me.mu.Unlock()
	for key := range me.state {
		if key.InfoHash == infoHash {
			delete(me.state, key)
		}
	}
	me.deletedTorrents = append(me.deletedTorrents, infoHash)
	return nil
}

func TestBufferedPieceCompletionDefersTrueUntilCheckpoint(t *testing.T) {
	underlying := &recordingPieceCompletion{}
	pc := newBufferedPieceCompletion(underlying)
	key := metainfo.PieceKey{
		InfoHash: metainfo.HashBytes([]byte("a")),
		Index:    1,
	}

	qt.Assert(t, qt.IsNil(pc.Set(key, g.Some(true))))

	runtimeValue, err := pc.Get(key)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(runtimeValue.Ok))
	qt.Assert(t, qt.IsTrue(runtimeValue.Complete))

	persistedValue, err := underlying.Get(key)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsFalse(persistedValue.Ok))

	checkpointer, ok := pc.(PieceCompletionCheckpointer)
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.IsNil(checkpointer.Checkpoint([]metainfo.PieceKey{key})))

	persistedValue, err = underlying.Get(key)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(persistedValue.Ok))
	qt.Assert(t, qt.IsTrue(persistedValue.Complete))

	underlying.mu.Lock()
	defer underlying.mu.Unlock()
	qt.Assert(t, qt.HasLen(underlying.batch, 1))
	qt.Assert(t, qt.DeepEquals(underlying.batch[0], PieceCompletionChange{
		Key:      key,
		Complete: true,
	}))
}

func TestBufferedPieceCompletionPersistsFalseImmediately(t *testing.T) {
	underlying := &recordingPieceCompletion{}
	pc := newBufferedPieceCompletion(underlying)
	key := metainfo.PieceKey{
		InfoHash: metainfo.HashBytes([]byte("b")),
		Index:    2,
	}

	qt.Assert(t, qt.IsNil(pc.Set(key, g.Some(true))))
	qt.Assert(t, qt.IsNil(pc.Set(key, g.Some(false))))

	persistedValue, err := underlying.Get(key)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(persistedValue.Ok))
	qt.Assert(t, qt.IsFalse(persistedValue.Complete))

	checkpointer, ok := pc.(PieceCompletionCheckpointer)
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.IsNil(checkpointer.Checkpoint([]metainfo.PieceKey{key})))

	underlying.mu.Lock()
	defer underlying.mu.Unlock()
	qt.Assert(t, qt.HasLen(underlying.batch, 1))
	qt.Assert(t, qt.IsFalse(underlying.batch[0].Complete))
}

func TestBufferedPieceCompletionPersistsUnknownImmediately(t *testing.T) {
	underlying := &recordingPieceCompletion{}
	pc := newBufferedPieceCompletion(underlying)
	key := metainfo.PieceKey{
		InfoHash: metainfo.HashBytes([]byte("c")),
		Index:    3,
	}

	qt.Assert(t, qt.IsNil(underlying.Set(key, g.Some(true))))
	qt.Assert(t, qt.IsNil(pc.Set(key, g.Some(true))))
	qt.Assert(t, qt.IsNil(pc.Set(key, g.Option[bool]{})))

	runtimeValue, err := pc.Get(key)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsFalse(runtimeValue.Ok))

	persistedValue, err := underlying.Get(key)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsFalse(persistedValue.Ok))

	checkpointer, ok := pc.(PieceCompletionCheckpointer)
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.IsNil(checkpointer.Checkpoint([]metainfo.PieceKey{key})))
}

func TestBufferedPieceCompletionCloseFlushesDirtyCompletions(t *testing.T) {
	underlying := &recordingPieceCompletion{}
	pc := newBufferedPieceCompletion(underlying)
	key := metainfo.PieceKey{
		InfoHash: metainfo.HashBytes([]byte("d")),
		Index:    4,
	}

	qt.Assert(t, qt.IsNil(pc.Set(key, g.Some(true))))
	qt.Assert(t, qt.IsNil(pc.Close()))

	persistedValue, err := underlying.Get(key)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(persistedValue.Ok))
	qt.Assert(t, qt.IsTrue(persistedValue.Complete))
}

func TestBufferedPieceCompletionPersistentForwardsUnderlyingState(t *testing.T) {
	underlying := &recordingPieceCompletion{persistent: true}
	pc := newBufferedPieceCompletion(underlying)
	persistenter, ok := pc.(PieceCompletionPersistenter)
	qt.Assert(t, qt.IsTrue(ok))
	qt.Assert(t, qt.IsTrue(persistenter.Persistent()))

	underlying.persistent = false
	qt.Assert(t, qt.IsFalse(persistenter.Persistent()))
}

func TestBufferedPieceCompletionDeleteTorrentClearsOverlayAndUnderlying(t *testing.T) {
	underlying := &recordingPieceCompletion{}
	pc := newBufferedPieceCompletion(underlying)
	deleter, ok := pc.(PieceCompletionTorrentDeleter)
	qt.Assert(t, qt.IsTrue(ok))

	targetHash := metainfo.HashBytes([]byte("target"))
	targetKey := metainfo.PieceKey{InfoHash: targetHash, Index: 1}
	otherKey := metainfo.PieceKey{InfoHash: metainfo.HashBytes([]byte("other")), Index: 2}

	qt.Assert(t, qt.IsNil(pc.Set(targetKey, g.Some(true))))
	qt.Assert(t, qt.IsNil(pc.Set(otherKey, g.Some(true))))
	qt.Assert(t, qt.IsNil(deleter.DeleteTorrent(targetHash)))

	value, err := pc.Get(targetKey)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsFalse(value.Ok))

	value, err = pc.Get(otherKey)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(value.Ok))
	qt.Assert(t, qt.IsTrue(value.Complete))

	underlying.mu.Lock()
	defer underlying.mu.Unlock()
	qt.Assert(t, qt.HasLen(underlying.deletedTorrents, 1))
	qt.Assert(t, qt.Equals(underlying.deletedTorrents[0], targetHash))
}
