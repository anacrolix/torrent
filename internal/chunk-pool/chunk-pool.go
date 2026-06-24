// Package chunkpool provides a pool of reusable chunk-data buffers, shared between the torrent
// package and the peer wire decoder (including its tests). Buffers are *[]byte so they can be
// returned to the pool without allocating a slice header. Outstanding (gotten but not yet returned)
// buffers are tracked via an expvar so leaks show up as monotonic growth.
package chunkpool

import (
	"expvar"
	"sync"

	"github.com/anacrolix/missinggo/v2/panicif"
)

// Chunk buffers gotten from a Pool but not yet returned. Should hover near zero; monotonic growth
// indicates buffers are being leaked.
var outstanding = expvar.NewInt("torrentChunkBuffersOutstanding")

// A Pool of reusable chunk-data buffers of a fixed size.
type Pool struct {
	pool sync.Pool
	size int
}

// New returns a Pool that allocates size-byte buffers.
func New(size int) *Pool {
	cp := &Pool{}
	cp.SetSize(size)
	return cp
}

// SetSize sets the size of buffers the Pool allocates, discarding any pooled buffers of the old size.
func (cp *Pool) SetSize(size int) {
	cp.size = size
	cp.pool = sync.Pool{
		New: func() any {
			b := make([]byte, size)
			return &b
		},
	}
}

// Get returns a pooled buffer (with capacity for a chunk), counting it as outstanding.
func (cp *Pool) Get() *[]byte {
	outstanding.Add(1)
	return cp.pool.Get().(*[]byte)
}

// Put returns a buffer to the pool, counting it as no longer outstanding.
func (cp *Pool) Put(b *[]byte) {
	panicif.NotEq(cap(*b), cp.size)
	outstanding.Add(-1)
	cp.pool.Put(b)
}
