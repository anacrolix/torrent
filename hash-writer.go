package torrent

import (
	"hash"
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
		i := firstNonZero(p)
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

func (w *hashWriter) writeZeroes(n int64) (err error) {
	if !w.startedNonZeroes {
		w.leadingZeroes += n
		return nil
	}
	_, err = writeZeroes(w.activeHash, n)
	return
}

// materialize returns the underlying hash with everything written so far absorbed. If only zeroes
// were written, the hash is obtained cheaply from the cache with the leading zeroes already absorbed.
func (w *hashWriter) materialize() hash.Hash {
	if !w.startedNonZeroes {
		w.activeHash = w.hashCache.clonePrefix(w.leadingZeroes)
		w.startedNonZeroes = true
	}
	return w.activeHash
}
