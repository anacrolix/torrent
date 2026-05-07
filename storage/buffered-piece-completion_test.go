package storage

import (
	"sync"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

type recordingPieceCompletion struct {
	mu    sync.Mutex
	state map[metainfo.PieceKey]bool
	batch []PieceCompletionChange
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

func (me *recordingPieceCompletion) Set(pk metainfo.PieceKey, complete bool) error {
	return me.SetBatch([]PieceCompletionChange{{
		Key:      pk,
		Complete: complete,
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

func TestBufferedPieceCompletionDefersTrueUntilCheckpoint(t *testing.T) {
	underlying := &recordingPieceCompletion{}
	pc := newBufferedPieceCompletion(underlying)
	key := metainfo.PieceKey{
		InfoHash: metainfo.HashBytes([]byte("a")),
		Index:    1,
	}

	qt.Assert(t, qt.IsNil(pc.Set(key, true)))

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
}

func TestBufferedPieceCompletionPersistsFalseImmediately(t *testing.T) {
	underlying := &recordingPieceCompletion{}
	pc := newBufferedPieceCompletion(underlying)
	key := metainfo.PieceKey{
		InfoHash: metainfo.HashBytes([]byte("b")),
		Index:    2,
	}

	qt.Assert(t, qt.IsNil(pc.Set(key, true)))
	qt.Assert(t, qt.IsNil(pc.Set(key, false)))

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
