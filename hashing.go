package torrent

import (
	"cmp"
	"hash"
	"slices"
	"sync"

	"github.com/anacrolix/missinggo/v2/panicif"

	"github.com/anacrolix/torrent/merkle"
)

type zeroPrefixHasher interface {
	hash.Cloner
}

type zeroPrefixHashCacheEntry struct {
	prefixLen int64
	hash      zeroPrefixHasher
}

type zeroPrefixHashCache struct {
	mu sync.Mutex
	// Sorted ascending by prefixLen. Must be seeded with a zero-length entry, so every positive
	// prefixLen has a smaller entry to extend from.
	entries []zeroPrefixHashCacheEntry
}

// newZeroPrefixHashCache returns a cache seeded with a fresh (zero-length) hasher to extend from.
func newZeroPrefixHashCache(seed zeroPrefixHasher) *zeroPrefixHashCache {
	return &zeroPrefixHashCache{
		entries: []zeroPrefixHashCacheEntry{{prefixLen: 0, hash: seed}},
	}
}

// Shared across torrents: the hash of a run of zeroes depends only on the algorithm and length, not
// the torrent.
var (
	// For v1 piece hashes (SHA-1), e.g. the zero padding of piece-aligned pieces.
	sha1HashCache = newZeroPrefixHashCache(pieceHash.New().(zeroPrefixHasher))
	// For v2 piece hashes (merkle/SHA-256).
	v2HashCache = newZeroPrefixHashCache(merkle.NewHash())
)

func cloneHasher(h zeroPrefixHasher) zeroPrefixHasher {
	clone, err := h.Clone()
	// Clone only errors for hashes that can't always be cloned, which we don't use here.
	panicif.Err(err)
	return clone
}

func (me *zeroPrefixHashCache) search(prefixLen int64) (i int, found bool) {
	return slices.BinarySearchFunc(
		me.entries,
		prefixLen,
		func(e zeroPrefixHashCacheEntry, target int64) int {
			return cmp.Compare(e.prefixLen, target)
		},
	)
}

// Returns a hasher that has absorbed prefixLen zero bytes. The returned hasher is the caller's to
// mutate; newly computed prefixes are memoized for reuse.
func (me *zeroPrefixHashCache) clonePrefix(prefixLen int64) zeroPrefixHasher {
	me.mu.Lock()
	defer me.mu.Unlock()
	i, found := me.search(prefixLen)
	if found {
		return cloneHasher(me.entries[i].hash)
	}
	// The largest entry smaller than prefixLen. The seeded zero-length entry guarantees this exists
	// for prefixLen > 0.
	panicif.Zero(i)
	pred := me.entries[i-1]
	// Extend a clone of the predecessor up to prefixLen by absorbing the missing zeroes.
	extended := cloneHasher(pred.hash)
	missing := prefixLen - pred.prefixLen
	written, err := writeZeroes(extended, missing)
	panicif.Err(err)
	panicif.NotEq(written, missing)
	// Memoize for next time, keeping entries sorted.
	me.entries = slices.Insert(me.entries, i, zeroPrefixHashCacheEntry{
		prefixLen: prefixLen,
		hash:      extended,
	})
	// Return a separate clone so the caller can mutate it independently of the cached entry.
	return cloneHasher(extended)
}
