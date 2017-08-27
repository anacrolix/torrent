package torrentfs

import (
	"bazil.org/fuse"
	fusefs "bazil.org/fuse/fs"
	"golang.org/x/net/context"
)

type fileNode struct {
	node
	size          uint64
	TorrentOffset int64
}

var (
	_ fusefs.NodeOpener = fileNode{}
)

func (fn fileNode) Attr(ctx context.Context, attr *fuse.Attr) error {
	attr.Size = fn.size
	attr.Mode = defaultMode
	return nil
}

func (fn fileNode) Open(ctx context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fusefs.Handle, error) {
	return fileHandle{fn}, nil
}
