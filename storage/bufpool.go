package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"

	"golang.org/x/sync/semaphore"
)

type pool struct {
	mu      sync.RWMutex
	buffers map[int64]*sync.Pool
}

type PooledBuffer interface {
	io.ReadWriteCloser
	// bytes.Buffer
	Cap() int
	Len() int
	Next(n int) []byte
	Bytes() []byte

	poolSize() int64
}

type buffer struct {
	size int64
	pool *pool
	*bytes.Buffer
}

func (b buffer) poolSize() int64 {
	return b.size
}

func (b buffer) Put() bool {
	if b.Buffer != nil {
		return b.pool.put(b)
	}

	return false
}

var ErrNotInPool = errors.New("buffer not in pool")

func (b buffer) Close() error {
	if b.Buffer == nil {
		return nil
	}

	if b.Put() {
		return nil
	}

	return ErrNotInPool
}

type BufferPool interface {
	Get(ctx context.Context, size int64) (PooledBuffer, error)
}

func (p *pool) Get(ctx context.Context, size int64) (PooledBuffer, error) {
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

	return buffer{size, p, pool.Get().(*bytes.Buffer)}, nil
}

func (p *pool) put(b buffer) bool {
	p.mu.RLock()
	pool, ok := p.buffers[b.size]
	p.mu.RUnlock()

	if !ok {
		return false
	}

	b.Reset()
	pool.Put(b.Buffer)
	return true
}

func NewBufferPool() BufferPool {
	return &pool{
		buffers: map[int64]*sync.Pool{},
	}
}

type limitedPool struct {
	buffers BufferPool
	semMax  *semaphore.Weighted
}

type limitedBuffer struct {
	PooledBuffer
	semMax *semaphore.Weighted
}

func (b limitedBuffer) Close() error {
	if b.PooledBuffer.Close() == nil {
		b.semMax.Release(b.poolSize())
	}

	return nil
}

func (p *limitedPool) Get(ctx context.Context, size int64) (PooledBuffer, error) {
	if err := p.semMax.Acquire(ctx, size); err != nil {
		return nil, err
	}

	buff, err := p.buffers.Get(ctx, size)

	if err != nil {
		return nil, err
	}

	return limitedBuffer{buff, p.semMax}, nil
}

func NewLimitedBufferPool(pool BufferPool, limit int64) BufferPool {
	return &limitedPool{
		buffers: pool,
		semMax:  semaphore.NewWeighted(limit),
	}
}
