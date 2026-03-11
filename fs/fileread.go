//go:build !windows

package torrentfs

import (
	"context"
	"errors"
	"io"

	"github.com/anacrolix/missinggo/v2"
	"github.com/anacrolix/torrent"
)

// ErrDestroyed is returned by ReadFile when the filesystem has been destroyed.
var ErrDestroyed = errors.New("torrentfs: filesystem destroyed")

// ReadFile reads len(dest) bytes at offset off from torrent file f.
// It blocks until data is available, the context is cancelled, or the
// filesystem is destroyed. It returns (n, ErrDestroyed) if the filesystem is
// destroyed, (n, ctx.Err()) if the context is cancelled, or (n, nil) on success.
func ReadFile(ctx context.Context, tfs *TorrentFS, f *torrent.File, dest []byte, off int64) (int, error) {
	r := f.NewReader()
	defer r.Close()
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		return 0, err
	}
	readDone := make(chan struct{})
	ctx, cancel := context.WithCancel(ctx)
	var (
		readErr error
		n       int
	)
	doneFn := tfs.TrackBlockedRead()
	go func() {
		defer close(readDone)
		cr := missinggo.ContextedReader{R: r, Ctx: ctx}
		n, readErr = io.ReadFull(cr, dest)
		if readErr == io.ErrUnexpectedEOF {
			readErr = nil
		}
	}()
	defer func() {
		<-readDone
		doneFn()
	}()
	defer cancel()

	select {
	case <-readDone:
		return n, readErr
	case <-tfs.Destroyed():
		return 0, ErrDestroyed
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
