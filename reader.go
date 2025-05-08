package torrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/anacrolix/log"
	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/missinggo/v2/panicif"
)

// Accesses Torrent data via a Client. Reads block until the data is available. Seeks and readahead
// also drive Client behaviour. Not safe for concurrent use. There are Torrent, File and Piece
// constructors for this.
type Reader interface {
	// Set the context for reads. When done, reads should get cancelled so they don't get stuck
	// waiting for data.
	SetContext(context.Context)
	// Read/Seek and not ReadAt because we want to return data as soon as it's available, and
	// because we want a single read head.
	io.ReadSeekCloser
	// Deprecated: This prevents type asserting for optional interfaces because a wrapper is
	// required to adapt back to io.Reader.
	missinggo.ReadContexter
	// Configure the number of bytes ahead of a read that should also be prioritized in preparation
	// for further reads. Overridden by non-nil readahead func, see SetReadaheadFunc.
	SetReadahead(int64)
	// If non-nil, the provided function is called when the implementation needs to know the
	// readahead for the current reader. Calls occur during Reads and Seeks, and while the Client is
	// locked.
	SetReadaheadFunc(ReadaheadFunc)
	// Don't wait for pieces to complete and be verified. Read calls return as soon as they can when
	// the underlying chunks become available. May be deprecated, although BitTorrent v2 will mean
	// we can support this without piece hashing.
	SetResponsive()
}

// Piece range by piece index, [begin, end).
type pieceRange struct {
	begin, end pieceIndex
}

type ReadaheadContext struct {
	ContiguousReadStartPos int64
	CurrentPos             int64
}

// Returns the desired readahead for a Reader.
type ReadaheadFunc func(ReadaheadContext) int64

type reader struct {
	t *Torrent
	// Adjust the read/seek window to handle Readers locked to File extents and the like.
	offset, length int64

	storageReader storageReader

	// Function to dynamically calculate readahead. If nil, readahead is static.
	readaheadFunc ReadaheadFunc

	// This is not protected by a lock because you should be coordinating setting this. If you want
	// different contexts, you should have different Readers.
	ctx context.Context

	// Required when modifying pos and readahead.
	mu sync.Locker

	readahead, pos int64
	// Position that reads have continued contiguously from.
	contiguousReadStartPos int64
	// The cached piece range this reader wants downloaded. The zero value corresponds to nothing.
	// We cache this so that changes can be detected, and bubbled up to the Torrent only as
	// required.
	pieces pieceRange

	// Reads have been initiated since the last seek. This is used to prevent readaheads occurring
	// after a seek or with a new reader at the starting position.
	reading    bool
	responsive bool
}

func (r *reader) SetContext(ctx context.Context) {
	r.ctx = ctx
}

var _ io.ReadSeekCloser = (*reader)(nil)

func (r *reader) SetResponsive() {
	r.responsive = true
	r.t.cl.event.Broadcast()
}

// Disable responsive mode. TODO: Remove?
func (r *reader) SetNonResponsive() {
	r.responsive = false
	r.t.cl.event.Broadcast()
}

func (r *reader) SetReadahead(readahead int64) {
	r.mu.Lock()
	r.readahead = readahead
	r.readaheadFunc = nil
	r.posChanged()
	r.mu.Unlock()
}

func (r *reader) SetReadaheadFunc(f ReadaheadFunc) {
	r.mu.Lock()
	r.readaheadFunc = f
	r.posChanged()
	r.mu.Unlock()
}

// How many bytes are available to read. Max is the most we could require.
func (r *reader) available(off, max int64) (ret int64) {
	off += r.offset
	for max > 0 {
		req, ok := r.t.offsetRequest(off)
		if !ok {
			break
		}
		if !r.responsive && !r.t.pieceComplete(pieceIndex(req.Index)) {
			break
		}
		if !r.t.haveChunk(req) {
			break
		}
		len1 := int64(req.Length) - (off - r.t.requestOffset(req))
		max -= len1
		ret += len1
		off += len1
	}
	// Ensure that ret hasn't exceeded our original max.
	if max < 0 {
		ret += max
	}
	return
}

// Calculates the pieces this reader wants downloaded, ignoring the cached value at r.pieces.
func (r *reader) piecesUncached() (ret pieceRange) {
	ra := r.readahead
	if r.readaheadFunc != nil {
		ra = r.readaheadFunc(ReadaheadContext{
			ContiguousReadStartPos: r.contiguousReadStartPos,
			CurrentPos:             r.pos,
		})
	}
	if ra < 1 {
		// Needs to be at least 1, because [x, x) means we don't want
		// anything.
		ra = 1
	}
	if !r.reading {
		ra = 0
	}
	if ra > r.length-r.pos {
		ra = r.length - r.pos
	}
	ret.begin, ret.end = r.t.byteRegionPieces(r.torrentOffset(r.pos), ra)
	return
}

func (r *reader) Read(b []byte) (n int, err error) {
	return r.read(b)
}

func (r *reader) read(b []byte) (n int, err error) {
	return r.readContext(r.ctx, b)
}

// Deprecated: Use SetContext and Read. TODO: I've realised this breaks the ability to pass through
// optional interfaces like io.WriterTo and io.ReaderFrom. Go sux. Context should be provided
// somewhere else.
func (r *reader) ReadContext(ctx context.Context, b []byte) (n int, err error) {
	r.ctx = ctx
	return r.Read(b)
}

// We still pass ctx here, although it's a reader field now.
func (r *reader) readContext(ctx context.Context, b []byte) (n int, err error) {
	if len(b) > 0 {
		r.reading = true
		// TODO: Rework reader piece priorities so we don't have to push updates in to the Client
		// and take the lock here.
		r.mu.Lock()
		r.posChanged()
		r.mu.Unlock()
	}
	n, err = r.readAt(ctx, b, r.pos)
	if n == 0 {
		if err == nil && len(b) > 0 {
			panic("expected error")
		} else {
			return
		}
	}

	r.mu.Lock()
	r.pos += int64(n)
	r.posChanged()
	r.mu.Unlock()
	if r.pos >= r.length {
		err = io.EOF
	} else if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return
}

var closedChan = make(chan struct{})

func init() {
	close(closedChan)
}

// Wait until some data should be available to read. Tickles the client if it isn't. Returns how
// much should be readable without blocking. `block` is whether to block if nothing is available,
// for successive reads for example.
func (r *reader) waitAvailable(
	ctx context.Context,
	pos, wanted int64,
	block bool,
) (avail int64, err error) {
	t := r.t
	for {
		t.cl.rLock()
		avail = r.available(pos, wanted)
		readerCond := t.piece(int((r.offset + pos) / t.info.PieceLength)).readerCond.Signaled()
		t.cl.rUnlock()
		if avail != 0 {
			return
		}
		var dontWait <-chan struct{}
		if !block || wanted == 0 {
			dontWait = closedChan
		}
		select {
		case <-readerCond:
			continue
		case <-r.t.closed.Done():
			err = errTorrentClosed
		case <-ctx.Done():
			err = ctx.Err()
		case <-r.t.dataDownloadDisallowed.On():
			err = errors.New("torrent data downloading disabled")
		case <-r.t.networkingEnabled.Off():
			err = errors.New("torrent networking disabled")
		case <-dontWait:
		}
		return
	}
}

// Adds the reader's torrent offset to the reader object offset (for example the reader might be
// constrained to a particular file within the torrent).
func (r *reader) torrentOffset(readerPos int64) int64 {
	return r.offset + readerPos
}

// Performs at most one successful read to torrent storage.
func (r *reader) readOnceAt(ctx context.Context, b []byte, pos int64) (n int, err error) {
	var avail int64
	avail, err = r.waitAvailable(ctx, pos, int64(len(b)), n == 0)
	if avail == 0 || err != nil {
		return
	}
	firstPieceIndex := pieceIndex(r.torrentOffset(pos) / r.t.info.PieceLength)
	firstPieceOffset := r.torrentOffset(pos) % r.t.info.PieceLength
	b1 := b[:min(int64(len(b)), avail)]
	// I think we can get EOF here due to the ReadAt contract. Previously we were forgetting to
	// return an error so it wasn't noticed. We now try again if there's a storage cap otherwise
	// convert it to io.UnexpectedEOF.
	r.initStorageReader()
	n, err = r.storageReader.ReadAt(b1, r.torrentOffset(pos))
	//n, err = r.t.readAt(b1, r.torrentOffset(pos))
	if n != 0 {
		err = nil
		return
	}
	panicif.Nil(err)
	if r.t.closed.IsSet() {
		err = fmt.Errorf("reading from closed torrent: %w", err)
		return
	}
	attrs := [...]any{
		"piece", firstPieceIndex,
		"offset", firstPieceOffset,
		"bytes", len(b1),
		"err", err,
	}
	if r.t.hasStorageCap() {
		r.slogger().Debug("error reading from capped storage", attrs[:]...)
	} else {
		r.slogger().Error("error reading", attrs[:]...)
	}
	return
}

// Performs at most one successful read to torrent storage. Try reading, first with the storage
// reader we already have, then after resetting it (in case data moved for
// completed/incomplete/promoted etc.). Then try resetting the piece completions. Then after all
// that if the storage is supposed to be flaky, try all over again. TODO: Filter errors and set log
// levels appropriately.
func (r *reader) readAt(ctx context.Context, b []byte, pos int64) (n int, err error) {
	if pos >= r.length {
		err = io.EOF
		return
	}
	n, err = r.readOnceAt(ctx, b, pos)
	if err == nil {
		return
	}
	r.slogger().Error("initial read failed", "err", err)

	err = r.clearStorageReader()
	if err != nil {
		err = fmt.Errorf("closing storage reader after first read failed: %w", err)
		return
	}
	r.storageReader = nil

	n, err = r.readOnceAt(ctx, b, pos)
	if err == nil {
		return
	}
	r.slogger().Error("read failed after reader reset", "err", err)

	r.updatePieceCompletion(pos)

	n, err = r.readOnceAt(ctx, b, pos)
	if err == nil {
		return
	}
	r.slogger().Error("read failed after completion resync", "err", err)

	if r.t.hasStorageCap() {
		// Ensure params weren't modified (Go sux). Recurse to detect infinite loops. TODO: I expect
		// only some errors should pass through here, this might cause us to get stuck if we retry
		// for any error.
		return r.readAt(ctx, b, pos)
	}

	// There should have been something available, avail != 0 here.
	if err == io.EOF {
		err = io.ErrUnexpectedEOF
	}
	return
}

// We pass pos in case we go ahead and implement multiple reads per ReadAt.
func (r *reader) updatePieceCompletion(pos int64) {
	firstPieceIndex := pieceIndex(r.torrentOffset(pos) / r.t.info.PieceLength)
	r.t.cl.lock()
	// I think there's a panic here caused by the Client being closed before obtaining this
	// lock. TestDropTorrentWithMmapStorageWhileHashing seems to tickle occasionally in CI.
	// Just add exceptions already.
	defer r.t.cl.unlock()
	if r.t.closed.IsSet() {
		// Can't update because Torrent's piece order is removed from Client.
		return
	}
	// TODO: Just reset pieces in the readahead window. This might help
	// prevent thrashing with small caches and file and piece priorities.
	if !r.t.updatePieceCompletion(firstPieceIndex) {
		r.logger().Levelf(log.Debug, "piece %d completion unchanged", firstPieceIndex)
	}
	// Update the rest of the piece completions in the readahead window, without alerting to
	// changes (since only the first piece, the one above, could have generated the read error
	// we're currently handling).
	if r.pieces.begin != firstPieceIndex {
		panic(fmt.Sprint(r.pieces.begin, firstPieceIndex))
	}
	for index := r.pieces.begin + 1; index < r.pieces.end; index++ {
		r.t.updatePieceCompletion(index)
	}
}

// Hodor
func (r *reader) Close() error {
	r.t.cl.lock()
	r.t.deleteReader(r)
	r.t.cl.unlock()
	return r.clearStorageReader()
}

func (r *reader) posChanged() {
	to := r.piecesUncached()
	from := r.pieces
	if to == from {
		return
	}
	r.pieces = to
	// log.Printf("reader pos changed %v->%v", from, to)
	r.t.readerPosChanged(from, to)
}

func (r *reader) Seek(off int64, whence int) (newPos int64, err error) {
	switch whence {
	case io.SeekStart:
		newPos = off
		r.mu.Lock()
	case io.SeekCurrent:
		r.mu.Lock()
		newPos = r.pos + off
	case io.SeekEnd:
		newPos = r.length + off
		r.mu.Lock()
	default:
		return 0, errors.New("bad whence")
	}
	if newPos != r.pos {
		r.reading = false
		r.pos = newPos
		r.contiguousReadStartPos = newPos
		r.posChanged()
	}
	r.mu.Unlock()
	return
}

func (r *reader) logger() log.Logger {
	return r.t.logger
}

// Implementation inspired by https://news.ycombinator.com/item?id=27019613.
func defaultReadaheadFunc(r ReadaheadContext) int64 {
	return r.CurrentPos - r.ContiguousReadStartPos
}

func (r *reader) slogger() *slog.Logger {
	return r.t.slogger()
}

func (r *reader) initStorageReader() {
	if r.storageReader == nil {
		r.storageReader = r.t.storageReader()
	}
}

func (r *reader) clearStorageReader() (err error) {
	if r.storageReader != nil {
		err = r.storageReader.Close()
		if err != nil {
			return
		}
	}
	r.storageReader = nil
	return
}
