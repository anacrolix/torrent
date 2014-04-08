package torrentfs

import (
	"log"
	"os"

	"bazil.org/fuse"
	fusefs "bazil.org/fuse/fs"
	"bitbucket.org/anacrolix/go.torrent"
	metainfo "github.com/nsf/libtorgo/torrent"
)

const (
	defaultMode = 0555
)

type torrentFS struct {
	Client *torrent.Client
}

var _ fusefs.NodeForgetter = rootNode{}

type rootNode struct {
	fs *torrentFS
}

type node struct {
	path     []string
	metaInfo *metainfo.MetaInfo
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
	data := make([]byte, func() int {
		_len := int64(fn.size) - req.Offset
		if int64(req.Size) < _len {
			return req.Size
		} else {
			// limit read to the end of the file
			return int(_len)
		}
	}())
	if len(data) == 0 {
		return nil
	}
	infoHash := torrent.BytesInfoHash(fn.metaInfo.InfoHash)
	torrentOff := fn.TorrentOffset + req.Offset
	log.Print(torrentOff, len(data), fn.TorrentOffset)
	if err := fn.FS.Client.PrioritizeDataRegion(infoHash, torrentOff, int64(len(data))); err != nil {
		panic(err)
	}
	for {
		dataWaiter := fn.FS.Client.DataWaiter()
		n, err := fn.FS.Client.TorrentReadAt(infoHash, torrentOff, data)
		switch err {
		case nil:
			resp.Data = data[:n]
			return nil
		case torrent.ErrDataNotReady:
			select {
			case <-dataWaiter:
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
	for _, fi := range dn.metaInfo.Files {
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
	for _, fi := range dn.metaInfo.Files {
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

func isSingleFileTorrent(mi *metainfo.MetaInfo) bool {
	return len(mi.Files) == 1 && mi.Files[0].Path == nil
}

func (me rootNode) Lookup(name string, intr fusefs.Intr) (_node fusefs.Node, err fuse.Error) {
	for _, _torrent := range me.fs.Client.Torrents() {
		metaInfo := _torrent.MetaInfo
		if metaInfo.Name == name {
			__node := node{
				metaInfo: metaInfo,
				FS:       me.fs,
				InfoHash: torrent.BytesInfoHash(metaInfo.InfoHash),
			}
			if isSingleFileTorrent(metaInfo) {
				_node = fileNode{__node, uint64(metaInfo.Files[0].Length), 0}
			} else {
				_node = dirNode{__node}
			}
			break
		}
	}
	if _node == nil {
		err = fuse.ENOENT
	}
	return
}

func (me rootNode) ReadDir(intr fusefs.Intr) (dirents []fuse.Dirent, err fuse.Error) {
	for _, _torrent := range me.fs.Client.Torrents() {
		metaInfo := _torrent.MetaInfo
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
func (rootNode) Forget() {
}

func (tfs *torrentFS) Root() (fusefs.Node, fuse.Error) {
	return rootNode{tfs}, nil
}

func New(cl *torrent.Client) *torrentFS {
	fs := &torrentFS{
		Client: cl,
	}
	return fs
}
