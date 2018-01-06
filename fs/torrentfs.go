package torrentfs

import (
	"expvar"
	"os"
	"strings"
	"sync"

	"bazil.org/fuse"
	fusefs "bazil.org/fuse/fs"
	"golang.org/x/net/context"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
)

const (
	defaultMode = 0555
)

var (
	torrentfsReadRequests        = expvar.NewInt("torrentfsReadRequests")
	torrentfsDelayedReadRequests = expvar.NewInt("torrentfsDelayedReadRequests")
	interruptedReads             = expvar.NewInt("interruptedReads")
)

type TorrentFS struct {
	Client       *torrent.Client
	destroyed    chan struct{}
	mu           sync.Mutex
	blockedReads int
	event        sync.Cond
}

var (
	_ fusefs.FSDestroyer = &TorrentFS{}

	_ fusefs.NodeForgetter      = rootNode{}
	_ fusefs.HandleReadDirAller = rootNode{}
	_ fusefs.HandleReadDirAller = dirNode{}
)

// Is a directory node that lists all torrents and handles destruction of the
// filesystem.
type rootNode struct {
	fs *TorrentFS
}

type node struct {
	path     string
	metadata *metainfo.Info
	FS       *TorrentFS
	t        *torrent.Torrent
}

type dirNode struct {
	node
}

var (
	_ fusefs.HandleReadDirAller = dirNode{}
)

func isSubPath(parent, child string) bool {
	if len(parent) == 0 {
		return len(child) > 0
	}
	if !strings.HasPrefix(child, parent) {
		return false
	}
	extra := child[len(parent):]
	if len(extra) == 0 {
		return false
	}
	// Not just a file with more stuff on the end.
	return extra[0] == '/'
}

func (dn dirNode) ReadDirAll(ctx context.Context) (des []fuse.Dirent, err error) {
	names := map[string]bool{}
	for _, fi := range dn.metadata.Files {
		if !isSubPath(dn.path, strings.Join(fi.Path, "/")) {
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

func (dn dirNode) Lookup(_ context.Context, name string) (fusefs.Node, error) {
	dir := false
	var file *torrent.File
	fullPath := dn.path + "/" + name
	for _, f := range dn.t.Files() {
		if f.DisplayPath() == fullPath {
			file = &f
		}
		if isSubPath(fullPath, f.DisplayPath()) {
			dir = true
		}
	}
	n := dn.node
	n.path = fullPath
	if dir && file != nil {
		panic("both dir and file")
	}
	if file != nil {
		return fileNode{n, file}, nil
	}
	if dir {
		return dirNode{n}, nil
	}
	return nil, fuse.ENOENT
}

func (dn dirNode) Attr(ctx context.Context, attr *fuse.Attr) error {
	attr.Mode = os.ModeDir | defaultMode
	return nil
}

func (rn rootNode) Lookup(ctx context.Context, name string) (_node fusefs.Node, err error) {
	for _, t := range rn.fs.Client.Torrents() {
		info := t.Info()
		if t.Name() != name || info == nil {
			continue
		}
		__node := node{
			metadata: info,
			FS:       rn.fs,
			t:        t,
		}
		if !info.IsDir() {
			_node = fileNode{__node, &t.Files()[0]}
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

func (rn rootNode) ReadDirAll(ctx context.Context) (dirents []fuse.Dirent, err error) {
	for _, t := range rn.fs.Client.Torrents() {
		info := t.Info()
		if info == nil {
			continue
		}
		dirents = append(dirents, fuse.Dirent{
			Name: info.Name,
			Type: func() fuse.DirentType {
				if !info.IsDir() {
					return fuse.DT_File
				} else {
					return fuse.DT_Dir
				}
			}(),
		})
	}
	return
}

func (rn rootNode) Attr(ctx context.Context, attr *fuse.Attr) error {
	attr.Mode = os.ModeDir
	return nil
}

// TODO(anacrolix): Why should rootNode implement this?
func (rn rootNode) Forget() {
	rn.fs.Destroy()
}

func (tfs *TorrentFS) Root() (fusefs.Node, error) {
	return rootNode{tfs}, nil
}

func (tfs *TorrentFS) Destroy() {
	tfs.mu.Lock()
	select {
	case <-tfs.destroyed:
	default:
		close(tfs.destroyed)
	}
	tfs.mu.Unlock()
}

func New(cl *torrent.Client) *TorrentFS {
	fs := &TorrentFS{
		Client:    cl,
		destroyed: make(chan struct{}),
	}
	fs.event.L = &fs.mu
	return fs
}
