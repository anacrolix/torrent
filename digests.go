package torrent

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/james-lawrence/torrent/internal/errorsx"
	"github.com/james-lawrence/torrent/metainfo"
)

func newDigestsFromTorrent(t *torrent) digests {
	return newDigests(
		t.storage,
		t.piece,
		func(idx int, cause error) {
			// log.Printf("hashed %d - %v\n", idx, cause)
			// log.Printf("hashed %p %d / %d - %v", t.chunks, idx+1, t.numPieces(), cause)
			t.chunks.Hashed(uint64(idx), cause)

			t.event.Broadcast()
			t.cln.event.Broadcast() // cause the client to detect completed torrents.
			t.pieceStateChanges.Publish(idx)
		},
	)
}

func newDigests(iora io.ReaderAt, retrieve func(int) *metainfo.Piece, complete func(int, error)) digests {
	if iora == nil {
		panic("digests require a storage implementation")
	}

	// log.Printf("new digest %T\n", iora)
	return digests{
		ReaderAt: iora,
		retrieve: retrieve,
		complete: complete,
		pending:  newBitQueue(),
		c:        sync.NewCond(&sync.Mutex{}),
	}
}

// digests is responsible correctness of received data.
type digests struct {
	ReaderAt io.ReaderAt
	retrieve func(int) *metainfo.Piece
	complete func(int, error)
	// marks whether digest is actively processing.
	reaping int64
	// cache of the pieces that need to be verified.
	pending *bitQueue
	c       *sync.Cond
}

// Enqueue a piece to check its completed digest.
func (t *digests) Enqueue(idx uint64) {
	t.pending.Push(int(idx))
	t.verify()
}

// wait for the digests to be complete
func (t *digests) Wait() {
	t.c.L.Lock()
	defer t.c.L.Unlock()

	for c := t.pending.Count(); c > 0; c = t.pending.Count() {
		t.c.Wait()
	}
}

func (t *digests) verify() {
	if atomic.AddInt64(&t.reaping, 1) > int64(runtime.NumCPU()) {
		atomic.AddInt64(&t.reaping, -1)
		return
	}

	go func() {
		for idx, ok := t.pending.Pop(); ok; idx, ok = t.pending.Pop() {
			t.check(idx)
		}

		if remaining := atomic.AddInt64(&t.reaping, -1); remaining == 0 {
			t.c.Broadcast()
		}
	}()
}

func (t *digests) check(idx int) {
	var (
		err    error
		digest metainfo.Hash
		p      *metainfo.Piece
	)

	if p = t.retrieve(idx); p == nil {
		t.complete(idx, fmt.Errorf("piece %d not found during digest", idx))
		return
	}

	if digest, err = t.compute(p); err != nil {
		t.complete(idx, err)
		return
	}

	if digest != p.Hash() {
		t.complete(idx, fmt.Errorf("piece %d digest mismatch %s != %s", idx, hex.EncodeToString(digest[:]), p.Hash().String()))
		return
	}

	t.complete(idx, nil)
}

func (t *digests) compute(p *metainfo.Piece) (ret metainfo.Hash, err error) {
	c := sha1.New()
	plen := p.Length()

	n, err := io.Copy(c, io.NewSectionReader(t.ReaderAt, p.Offset(), plen))
	if err != nil {
		return ret, errorsx.Wrapf(err, "piece %d digest failed", p.Offset())
	}

	if n != plen {
		return ret, fmt.Errorf("piece digest failed short copy %d: %d != %d", p.Offset(), n, plen)
	}

	copy(ret[:], c.Sum(nil))

	return ret, nil
}
