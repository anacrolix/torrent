package torrentfs

import (
	"context"
	"fmt"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
)

type fileHandle struct {
	fn fileNode
}

var _ fs.HandleReader = fileHandle{}

func (me fileHandle) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	torrentfsReadRequests.Add(1)
	if req.Dir {
		panic("read on directory")
	}
	size := req.Size
	fileLeft := int64(me.fn.size) - req.Offset
	if fileLeft < 0 {
		fileLeft = 0
	}
	if fileLeft < int64(size) {
		size = int(fileLeft)
	}
	resp.Data = resp.Data[:size]
	if len(resp.Data) == 0 {
		return nil
	}
	torrentOff := me.fn.TorrentOffset + req.Offset
	n, err := readFull(ctx, me.fn.FS, me.fn.t, torrentOff, resp.Data)
	if err != nil {
		return err
	}
	if n != size {
		panic(fmt.Sprintf("%d < %d", n, size))
	}
	return nil
}
