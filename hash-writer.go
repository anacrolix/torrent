package torrent

import (
	"hash"

	"github.com/anacrolix/torrent/internal/zeroio"
)

type hashWriter struct {
	leadingZeroes    int64
	startedNonZeroes bool
	hashCache        *zeroPrefixHashCache
	activeHash       hash.Hash
}

func (w *hashWriter) Write(p []byte) (n int, err error) {
	n = len(p)
	if !w.startedNonZeroes {
		// Leading zeroes are accumulated rather than hashed, so skip past any zero prefix in this
		// buffer to find where the real data starts.
		i := zeroio.FirstNonZero(p)
		w.leadingZeroes += int64(i)
		if i == len(p) {
			// Still all zeroes; keep deferring.
			return
		}
		// First non-zero byte: materialize the hash with the accumulated zero prefix already absorbed,
		// cheaply via the cache, then hash the remainder.
		w.activeHash = w.hashCache.clonePrefix(w.leadingZeroes)
		w.startedNonZeroes = true
		p = p[i:]
	}
	_, err = w.activeHash.Write(p)
	return
}

// WriteZeroes implements zeroio.ZeroesWriter, so callers (e.g. storage writing piece holes) can hand
// us a run of zeroes without materializing them: leading zeroes are accumulated, and once real data
// has started they're written into the active hash.
func (w *hashWriter) WriteZeroes(count int64) (written int64, err error) {
	if !w.startedNonZeroes {
		w.leadingZeroes += count
		return count, nil
	}
	// No fast path, not zeroio.WriteZeroes: activeHash is a plain hash, and going through the dispatch
	// would risk re-entering this method.
	return zeroio.WriteZeroesNoFastPath(w.activeHash, count)
}

var _ zeroio.ZeroesWriter = (*hashWriter)(nil)

// materialize returns the underlying hash with everything written so far absorbed. If only zeroes
// were written, the hash is obtained cheaply from the cache with the leading zeroes already absorbed.
func (w *hashWriter) materialize() hash.Hash {
	if !w.startedNonZeroes {
		w.activeHash = w.hashCache.clonePrefix(w.leadingZeroes)
		w.startedNonZeroes = true
	}
	return w.activeHash
}
