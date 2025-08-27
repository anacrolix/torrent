package storage

import (
	"iter"
	"sync"

	g "github.com/anacrolix/generics"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/types/infohash"
)

type mapPieceCompletion struct {
	// TODO: Generics. map of InfoHash to *memoryTorrentJustComplete.
	m sync.Map
}

func (me *mapPieceCompletion) Persistent() bool {
	return false
}

type (
	justComplete              = g.Option[bool]
	memoryTorrentJustComplete struct {
		mu    sync.RWMutex
		state []justComplete
	}
)

func (me *memoryTorrentJustComplete) Get(i int) justComplete {
	me.mu.RLock()
	defer me.mu.RUnlock()
	return me.getLocked(i)
}

func (me *memoryTorrentJustComplete) getLocked(i int) justComplete {
	if i >= len(me.state) {
		return g.None[bool]()
	}
	return me.state[i]
}

func (me *memoryTorrentJustComplete) Set(i int, complete bool) {
	me.mu.Lock()
	defer me.mu.Unlock()
	for i >= len(me.state) {
		me.state = append(me.state, g.None[bool]())
	}
	me.state[i].Set(complete)
}

func (me *memoryTorrentJustComplete) GetRange(begin, end int) iter.Seq[justComplete] {
	me.mu.RLock()
	return func(yield func(justComplete) bool) {
		defer me.mu.RUnlock()
		for i := begin; i < end; i++ {
			if !yield(me.getLocked(i)) {
				return
			}
		}
	}
}

var _ interface {
	PieceCompletion
	PieceCompletionGetRanger
} = (*mapPieceCompletion)(nil)

func NewMapPieceCompletion() PieceCompletion {
	return &mapPieceCompletion{}
}

func (me *mapPieceCompletion) Close() error {
	me.m.Clear()
	return nil
}

func (me *mapPieceCompletion) Get(pk metainfo.PieceKey) (c Completion, err error) {
	v, ok := me.m.Load(pk.InfoHash)
	if !ok {
		return
	}
	jcs := v.(*memoryTorrentJustComplete)
	c.Complete, c.Ok = jcs.Get(pk.Index).AsTuple()
	return
}

func (me *mapPieceCompletion) Set(pk metainfo.PieceKey, complete bool) error {
	v, ok := me.m.Load(pk.InfoHash)
	if !ok {
		v, _ = me.m.LoadOrStore(pk.InfoHash, &memoryTorrentJustComplete{})
	}
	t := v.(*memoryTorrentJustComplete)
	t.Set(pk.Index, complete)
	return nil
}

func (me *mapPieceCompletion) GetRange(ih infohash.T, begin, end int) iter.Seq[Completion] {
	return func(yield func(Completion) bool) {
		v, ok := me.m.Load(ih)
		if !ok {
			return
		}
		t := v.(*memoryTorrentJustComplete)
		for jc := range t.GetRange(begin, end) {
			if !yield(Completion{
				Err:      nil,
				Ok:       jc.Ok,
				Complete: jc.Value,
			}) {
				return
			}
		}
	}
}
