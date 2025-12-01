package torrent

import (
	"iter"
	"sync"
	"unique"
	"weak"
)

type torrentsByShortHash interface {
	Get(key shortInfohash) (*Torrent, bool)
	IterKeys() iter.Seq[shortInfohash]
	Init()
}

type syncMapTorrentsByShortHash struct {
	inner sync.Map
}

type (
	syncMapTorrentsByShortHashKey   = unique.Handle[shortInfohash]
	syncMapTorrentsByShortHashValue = weak.Pointer[Torrent]
)

// sync.Map is zero initialized, but we want this function in case we switch implementations.
func (me *syncMapTorrentsByShortHash) Init() {}

func (me *syncMapTorrentsByShortHash) IterKeys(yield func(shortInfohash) bool) {
	// Do we have to check the values for weak pointers? Probably should to keep the map clean
	// to speed up iteration. I wonder if it will introduce overhead to forSkeys.
	for key := range me.iter() {
		if !yield(key) {
			return
		}
	}
}

func (me *syncMapTorrentsByShortHash) iter() iter.Seq2[shortInfohash, *Torrent] {
	return func(yield func(shortInfohash, *Torrent) bool) {
		me.inner.Range(func(key, value any) bool {
			uk := key.(syncMapTorrentsByShortHashKey)
			v, ok := me.derefValueOrDelete(uk, value.(syncMapTorrentsByShortHashValue))
			if !ok {
				// Current value was lost, move on.
				return true
			}
			return yield(uk.Value(), v)
		})
	}
}

func (me *syncMapTorrentsByShortHash) IsEmpty() bool {
	for range me.iter() {
		return false
	}
	return true
}

func (me *syncMapTorrentsByShortHash) derefValueOrDelete(
	key syncMapTorrentsByShortHashKey,
	wp syncMapTorrentsByShortHashValue,
) (*Torrent, bool) {
	t := wp.Value()
	if t != nil {
		return t, true
	}
	me.inner.CompareAndDelete(key, wp)
	return nil, false
}

func (me *syncMapTorrentsByShortHash) Get(ih shortInfohash) (t *Torrent, ok bool) {
	key := unique.Make(ih)
	v, ok := me.inner.Load(key)
	if !ok {
		return
	}
	wp := v.(syncMapTorrentsByShortHashValue)
	return me.derefValueOrDelete(key, wp)
}

// Returns true if the key was newly inserted.
func (me *syncMapTorrentsByShortHash) Set(ih shortInfohash, t *Torrent) bool {
	_, loaded := me.inner.Swap(unique.Make(ih), weak.Make(t))
	return !loaded
}

// Returns true if the key was newly inserted.
func (me *syncMapTorrentsByShortHash) Delete(ih shortInfohash) {
	me.inner.Delete(unique.Make(ih))
}
