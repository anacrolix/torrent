package torrentfs

import (
	"context"
	"io"
	"log"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"github.com/anacrolix/missinggo"

	"github.com/anacrolix/torrent"
)

type fileHandle struct {
	fn fileNode
	r  torrent.Reader
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
	r := me.r
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
		log.Printf("reading %v bytes at %v", len(resp.Data), req.Offset)
		if true {
			n, readErr = io.ReadFull(r, resp.Data)
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
	return me.r.Close()
}
