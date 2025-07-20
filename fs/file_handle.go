//go:build !windows

package torrentfs

import (
	"context"
	"io"

	"github.com/anacrolix/fuse"
	"github.com/anacrolix/fuse/fs"
	"github.com/anacrolix/missinggo/v2"

	"github.com/anacrolix/torrent"
)

type fileHandle struct {
	fn fileNode
	tf *torrent.File
}

var _ interface {
	fs.HandleReader
	fs.HandleReleaser
} = fileHandle{}

func (me fileHandle) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	torrentfsReadRequests.Add(1)
	if req.Dir {
		panic("read on directory")
	}
	r := me.tf.NewReader()
	defer r.Close()
	pos, err := r.Seek(req.Offset, io.SeekStart)
	if err != nil {
		panic(err)
	}
	if pos != req.Offset {
		panic("seek failed")
	}
	resp.Data = resp.Data[:req.Size]
	readDone := make(chan struct{})
	ctx, cancel := context.WithCancel(ctx)
	var readErr error
	go func() {
		defer close(readDone)
		me.fn.FS.mu.Lock()
		me.fn.FS.blockedReads++
		me.fn.FS.event.Broadcast()
		me.fn.FS.mu.Unlock()
		var n int
		r := missinggo.ContextedReader{r, ctx}
		// log.Printf("reading %v bytes at %v", len(resp.Data), req.Offset)
		if true {
			// A user reported on that on freebsd 12.2, the system requires that reads are
			// completely filled. Their system only asks for 64KiB at a time. I've seen systems that
			// can demand up to 16MiB at a time, so this gets tricky. For now, I'll restore the old
			// behaviour from before 2a7352a, which nobody reported problems with.
			n, readErr = io.ReadFull(r, resp.Data)
			if readErr == io.ErrUnexpectedEOF {
				readErr = nil
			}
		} else {
			n, readErr = r.Read(resp.Data)
			if readErr == io.EOF {
				readErr = nil
			}
		}
		resp.Data = resp.Data[:n]
	}()
	defer func() {
		<-readDone
		me.fn.FS.mu.Lock()
		me.fn.FS.blockedReads--
		me.fn.FS.event.Broadcast()
		me.fn.FS.mu.Unlock()
	}()
	defer cancel()

	select {
	case <-readDone:
		return readErr
	case <-me.fn.FS.destroyed:
		return fuse.EIO
	case <-ctx.Done():
		return fuse.EINTR
	}
}

func (me fileHandle) Release(context.Context, *fuse.ReleaseRequest) error {
	return nil
}
