package torrentfs

import (
	"fmt"

	"bazil.org/fuse"
	fusefs "bazil.org/fuse/fs"
	"golang.org/x/net/context"
)

type fileNode struct {
	node
	size          uint64
	TorrentOffset int64
}

var _ fusefs.HandleReader = fileNode{}

func (fn fileNode) Attr(ctx context.Context, attr *fuse.Attr) error {
	attr.Size = fn.size
	attr.Mode = defaultMode
	return nil
}

func (fn fileNode) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	torrentfsReadRequests.Add(1)
	if req.Dir {
		panic("read on directory")
	}
	size := req.Size
	fileLeft := int64(fn.size) - req.Offset
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
	torrentOff := fn.TorrentOffset + req.Offset
	n, err := readFull(ctx, fn.FS, fn.t, torrentOff, resp.Data)
	if err != nil {
		return err
	}
	if n != size {
		panic(fmt.Sprintf("%d < %d", n, size))
	}
	return nil
}
