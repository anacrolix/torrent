package torrent

import (
	"bytes"
	"net/netip"

	g "github.com/anacrolix/generics"
	"github.com/anacrolix/sync"
	"golang.org/x/sync/semaphore"

	"github.com/anacrolix/torrent/smartban"
)

type bannableAddr = netip.Addr

type smartBanCache = smartban.Cache[bannableAddr, RequestIndex, uint64]

type pool struct {
	mu      sync.RWMutex
	buffers map[int64]*sync.Pool
	semMax  *semaphore.Weighted
}

type buffer struct {
	size int64
	*bytes.Buffer
}

func (b buffer) Close() error {
	if b.Buffer != nil {
		bufPool.put(b)
	}

	return nil
}

func (p *pool) get(size int64) buffer {
	p.mu.RLock()
	pool, ok := p.buffers[size]
	p.mu.RUnlock()

	if !ok {
		pool = &sync.Pool{
			New: func() interface{} {
				// round to the nearest MinRead boundary otherwise io.Copy is going
				// to result in the buffer getting grown ti around 2x its required size
				return bytes.NewBuffer(make([]byte, 0, (size/bytes.MinRead+1)*bytes.MinRead))
			},
		}

		p.mu.Lock()
		p.buffers[size] = pool
		p.mu.Unlock()
	}

	return buffer{size, pool.Get().(*bytes.Buffer)}
}

func (p *pool) put(b buffer) {
	p.mu.RLock()
	pool, ok := p.buffers[b.size]
	p.mu.RUnlock()

	if ok {
		b.Reset()
		pool.Put(b.Buffer)
		p.semMax.Release(b.size)
	}
}

var bufPool = &pool{
	buffers: map[int64]*sync.Pool{},
}

type blockCheckingWriter struct {
	cache        *smartBanCache
	requestIndex RequestIndex
	// Peers that didn't match blocks written now.
	badPeers    map[bannableAddr]struct{}
	blockBuffer buffer
	chunkSize   int
}

func (me *blockCheckingWriter) checkBlock() {
	b := me.blockBuffer.Next(me.chunkSize)
	for _, peer := range me.cache.CheckBlock(me.requestIndex, b) {
		g.MakeMapIfNilAndSet(&me.badPeers, peer, struct{}{})
	}
	me.requestIndex++
}

func (me *blockCheckingWriter) checkFullBlocks() {
	for me.blockBuffer.Len() >= me.chunkSize {
		me.checkBlock()
	}
}

func (me *blockCheckingWriter) Write(b []byte) (int, error) {
	n, err := me.blockBuffer.Write(b)
	if err != nil {
		// bytes.Buffer.Write should never fail.
		panic(err)
	}
	me.checkFullBlocks()
	return n, err
}

// Check any remaining block data. Terminal pieces or piece sizes that don't divide into the chunk
// size cleanly may leave fragments that should be checked.
func (me *blockCheckingWriter) Flush() {
	for me.blockBuffer.Len() != 0 {
		me.checkBlock()
	}

	me.blockBuffer.Close()
}
