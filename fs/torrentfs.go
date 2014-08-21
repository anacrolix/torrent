package torrentfs

import (
	"log"
	"os"
	"strings"
	"sync"

	"bazil.org/fuse"
	fusefs "bazil.org/fuse/fs"
	"bitbucket.org/anacrolix/go.torrent"
	"github.com/anacrolix/libtorgo/metainfo"
)

const (
	defaultMode = 0555
)

type torrentFS struct {
	Client    *torrent.Client
	destroyed chan struct{}
	mu        sync.Mutex
}

var _ fusefs.FSDestroyer = &torrentFS{}

var _ fusefs.NodeForgetter = rootNode{}

type rootNode struct {
	fs *torrentFS
}

type node struct {
	path     []string
	metadata *metainfo.Info
	FS       *torrentFS
	InfoHash torrent.InfoHash
}

type fileNode struct {
	node
	size          uint64
	TorrentOffset int64
}

func (fn fileNode) Attr() (attr fuse.Attr) {
	attr.Size = fn.size
	attr.Mode = defaultMode
	return
}

func (fn fileNode) Read(req *fuse.ReadRequest, resp *fuse.ReadResponse, intr fusefs.Intr) fuse.Error {
	if req.Dir {
		panic("hodor")
	}
	size := req.Size
	if int64(fn.size)-req.Offset < int64(size) {
		size = int(int64(fn.size) - req.Offset)
	}
	if size < 0 {
		size = 0
	}
	infoHash := fn.InfoHash
	torrentOff := fn.TorrentOffset + req.Offset
	log.Print(torrentOff, size, fn.TorrentOffset)
	if err := fn.FS.Client.PrioritizeDataRegion(infoHash, torrentOff, int64(size)); err != nil {
		panic(err)
	}
	resp.Data = resp.Data[:size]
	for {
		dataWaiter := fn.FS.Client.DataWaiter()
		n, err := fn.FS.Client.TorrentReadAt(infoHash, torrentOff, resp.Data)
		switch err {
		case nil:
			resp.Data = resp.Data[:n]
			return nil
		case torrent.ErrDataNotReady:
			select {
			case <-dataWaiter:
			case <-fn.FS.destroyed:
				return fuse.EIO
			case <-intr:
				return fuse.EINTR
			}
		default:
			log.Print(err)
			return fuse.EIO
		}
	}
}

type dirNode struct {
	node
}

var (
	_ fusefs.HandleReadDirer = dirNode{}

	_ fusefs.HandleReader = fileNode{}
)

func isSubPath(parent, child []string) bool {
	if len(child) <= len(parent) {
		return false
	}
	for i := range parent {
		if parent[i] != child[i] {
			return false
		}
	}
	return true
}

func (dn dirNode) ReadDir(intr fusefs.Intr) (des []fuse.Dirent, err fuse.Error) {
	names := map[string]bool{}
	for _, fi := range dn.metadata.Files {
		if !isSubPath(dn.path, fi.Path) {
			continue
		}
		name := fi.Path[len(dn.path)]
		if names[name] {
			continue
		}
		names[name] = true
		de := fuse.Dirent{
			Name: name,
		}
		if len(fi.Path) == len(dn.path)+1 {
			de.Type = fuse.DT_File
		} else {
			de.Type = fuse.DT_Dir
		}
		des = append(des, de)
	}
	return
}

func (dn dirNode) Lookup(name string, intr fusefs.Intr) (_node fusefs.Node, err fuse.Error) {
	var torrentOffset int64
	for _, fi := range dn.metadata.Files {
		if !isSubPath(dn.path, fi.Path) {
			torrentOffset += fi.Length
			continue
		}
		if fi.Path[len(dn.path)] != name {
			torrentOffset += fi.Length
			continue
		}
		__node := dn.node
		__node.path = append(__node.path, name)
		if len(fi.Path) == len(dn.path)+1 {
			_node = fileNode{
				node:          __node,
				size:          uint64(fi.Length),
				TorrentOffset: torrentOffset,
			}
		} else {
			_node = dirNode{__node}
		}
		break
	}
	if _node == nil {
		err = fuse.ENOENT
	}
	return
}

func (dn dirNode) Attr() (attr fuse.Attr) {
	attr.Mode = os.ModeDir | defaultMode
	return
}

func isSingleFileTorrent(md *metainfo.Info) bool {
	return len(md.Files) == 0
}

func (me rootNode) Lookup(name string, intr fusefs.Intr) (_node fusefs.Node, err fuse.Error) {
	for _, t := range me.fs.Client.Torrents() {
		if t.Name() != name || t.Info == nil {
			continue
		}
		__node := node{
			metadata: t.Info,
			FS:       me.fs,
			InfoHash: t.InfoHash,
		}
		if isSingleFileTorrent(t.Info) {
			_node = fileNode{__node, uint64(t.Info.Length), 0}
		} else {
			_node = dirNode{__node}
		}
		break
	}
	if _node == nil {
		err = fuse.ENOENT
	}
	return
}

func (me rootNode) ReadDir(intr fusefs.Intr) (dirents []fuse.Dirent, err fuse.Error) {
	for _, _torrent := range me.fs.Client.Torrents() {
		metaInfo := _torrent.Info
		if metaInfo == nil {
			continue
		}
		dirents = append(dirents, fuse.Dirent{
			Name: metaInfo.Name,
			Type: func() fuse.DirentType {
				if isSingleFileTorrent(metaInfo) {
					return fuse.DT_File
				} else {
					return fuse.DT_Dir
				}
			}(),
		})
	}
	return
}

func (rootNode) Attr() fuse.Attr {
	return fuse.Attr{
		Mode: os.ModeDir,
	}
}

// TODO(anacrolix): Why should rootNode implement this?
func (me rootNode) Forget() {
	me.fs.Destroy()
}

func (tfs *torrentFS) Root() (fusefs.Node, fuse.Error) {
	return rootNode{tfs}, nil
}

func (me *torrentFS) Destroy() {
	me.mu.Lock()
	select {
	case <-me.destroyed:
	default:
		close(me.destroyed)
	}
	me.mu.Unlock()
}

func New(cl *torrent.Client) *torrentFS {
	fs := &torrentFS{
		Client:    cl,
		destroyed: make(chan struct{}),
	}
	return fs
}
