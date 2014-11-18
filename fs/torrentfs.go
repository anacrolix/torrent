package torrentfs

import (
	"expvar"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"bazil.org/fuse"
	fusefs "bazil.org/fuse/fs"
	"bitbucket.org/anacrolix/go.torrent"
)

const (
	defaultMode = 0555
)

var (
	torrentfsReadRequests        = expvar.NewInt("torrentfsReadRequests")
	torrentfsDelayedReadRequests = expvar.NewInt("torrentfsDelayedReadRequests")
	interruptedReads             = expvar.NewInt("interruptedReads")
)

type torrentFS struct {
	Client    *torrent.Client
	destroyed chan struct{}
	mu        sync.Mutex
}

var (
	_ fusefs.FSDestroyer = &torrentFS{}
	_ fusefs.FSIniter    = &torrentFS{}
)

func (fs *torrentFS) Init(req *fuse.InitRequest, resp *fuse.InitResponse, intr fusefs.Intr) fuse.Error {
	log.Print(req)
	log.Print(resp)
	resp.MaxReadahead = req.MaxReadahead
	resp.Flags |= fuse.InitAsyncRead
	return nil
}

var _ fusefs.NodeForgetter = rootNode{}

type rootNode struct {
	fs *torrentFS
}

type node struct {
	path     []string
	metadata *torrent.MetaInfo
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

func (n *node) fsPath() string {
	return "/" + strings.Join(append([]string{n.metadata.Name}, n.path...), "/")
}

func blockingRead(fs *torrentFS, ih torrent.InfoHash, off int64, p []byte, intr fusefs.Intr) (n int, err fuse.Error) {
	dataWaiter := fs.Client.DataWaiter(ih, off)
	select {
	case <-dataWaiter:
	case <-fs.destroyed:
		err = fuse.EIO
		return
	case <-intr:
		err = fuse.EINTR
		return
	}
	n, err = fs.Client.TorrentReadAt(ih, off, p)
	return
}

func readFull(fs *torrentFS, ih torrent.InfoHash, off int64, p []byte, intr fusefs.Intr) (n int, err fuse.Error) {
	for len(p) != 0 {
		var nn int
		nn, err = blockingRead(fs, ih, off, p, intr)
		if err != nil {
			break
		}
		n += nn
		off += int64(nn)
		p = p[nn:]
	}
	return
}

func (fn fileNode) Read(req *fuse.ReadRequest, resp *fuse.ReadResponse, intr fusefs.Intr) fuse.Error {
	torrentfsReadRequests.Add(1)
	started := time.Now()
	if req.Dir {
		panic("read on directory")
	}
	defer func() {
		ms := time.Now().Sub(started).Nanoseconds() / 1000000
		if ms < 20 {
			return
		}
		log.Printf("torrentfs read took %dms", ms)
	}()
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
	infoHash := fn.InfoHash
	torrentOff := fn.TorrentOffset + req.Offset
	go func() {
		if err := fn.FS.Client.PrioritizeDataRegion(infoHash, torrentOff, int64(size)); err != nil {
			log.Printf("error prioritizing %s: %s", fn.fsPath(), err)
		}
	}()
	n, err := readFull(fn.FS, infoHash, torrentOff, resp.Data, intr)
	if err != nil {
		return err
	}
	if n != size {
		panic(fmt.Sprintf("%d < %d", n, size))
	}
	return nil
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
		if t.Info.SingleFile() {
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
	for _, t := range me.fs.Client.Torrents() {
		if t.Info == nil {
			continue
		}
		dirents = append(dirents, fuse.Dirent{
			Name: t.Info.Name,
			Type: func() fuse.DirentType {
				if t.Info.SingleFile() {
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
