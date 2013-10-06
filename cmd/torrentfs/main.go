package main

import (
	"bazil.org/fuse"
	fusefs "bazil.org/fuse/fs"
	"bitbucket.org/anacrolix/go.torrent"
	"flag"
	metainfo "github.com/nsf/libtorgo/torrent"
	"log"
	"os"
	"os/user"
	"path/filepath"
)

var (
	downloadDir string
	torrentPath string
	mountDir    string
)

const (
	defaultMode = 0555
)

func init() {
	flag.StringVar(&downloadDir, "downloadDir", "", "location to save torrent data")
	flag.StringVar(&torrentPath, "torrentPath", func() string {
		_user, err := user.Current()
		if err != nil {
			log.Fatal(err)
		}
		return filepath.Join(_user.HomeDir, ".config/transmission/torrents")
	}(), "torrent files in this location describe the contents of the mounted filesystem")
	flag.StringVar(&mountDir, "mountDir", "", "location the torrent contents are made available")
}

type TorrentFS struct {
	Client *torrent.Client
}

type rootNode struct {
	fs *TorrentFS
}

type node struct {
	path     []string
	metaInfo *metainfo.MetaInfo
	client   *torrent.Client
}

type fileNode struct {
	node
	size uint64
}

func (fn fileNode) Attr() (attr fuse.Attr) {
	attr.Size = fn.size
	attr.Mode = defaultMode
	return
}

type dirNode struct {
	node
}

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
	for _, fi := range dn.metaInfo.Files {
		if !isSubPath(dn.path, fi.Path) {
			continue
		}
		if fi.Path[len(dn.path)] != name {
			continue
		}
		__node := node{
			path:     append(dn.path, name),
			metaInfo: dn.metaInfo,
			client:   dn.client,
		}
		if len(fi.Path) == len(dn.path)+1 {
			_node = fileNode{
				node: __node,
				size: uint64(fi.Length),
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
				client:   me.fs.Client,
			}
			if isSingleFileTorrent(metaInfo) {
				_node = fileNode{__node, uint64(metaInfo.Files[0].Length)}
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

func (tfs *TorrentFS) Root() (fusefs.Node, fuse.Error) {
	return rootNode{tfs}, nil
}

func main() {
	flag.Parse()
	client := torrent.NewClient(downloadDir)
	torrentDir, err := os.Open(torrentPath)
	defer torrentDir.Close()
	if err != nil {
		log.Fatal(err)
	}
	names, err := torrentDir.Readdirnames(-1)
	if err != nil {
		log.Fatal(err)
	}
	for _, name := range names {
		metaInfo, err := metainfo.LoadFromFile(filepath.Join(torrentPath, name))
		if err != nil {
			log.Print(err)
		}
		err = client.AddTorrent(metaInfo)
		if err != nil {
			log.Print(err)
		}
	}
	conn, err := fuse.Mount(mountDir)
	if err != nil {
		log.Fatal(err)
	}
	fs := &TorrentFS{client}
	fusefs.Serve(conn, fs)
}
