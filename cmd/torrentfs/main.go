package main

import (
	"bazil.org/fuse"
	fusefs "bazil.org/fuse/fs"
	"bitbucket.org/anacrolix/go.torrent"
	"flag"
	metainfo "github.com/nsf/libtorgo/torrent"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/user"
	"path/filepath"
	"sync"
	"time"
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
	Client   *torrent.Client
	DataSubs map[chan torrent.DataSpec]struct{}
	sync.Mutex
}

func (tfs *TorrentFS) publishData() {
	for {
		spec := <-tfs.Client.DataReady
		tfs.Lock()
		for ds := range tfs.DataSubs {
			ds <- spec
		}
		tfs.Unlock()
	}
}

func (tfs *TorrentFS) SubscribeData() chan torrent.DataSpec {
	ch := make(chan torrent.DataSpec)
	tfs.Lock()
	tfs.DataSubs[ch] = struct{}{}
	tfs.Unlock()
	return ch
}

func (tfs *TorrentFS) UnsubscribeData(ch chan torrent.DataSpec) {
	go func() {
		for _ = range ch {
		}
	}()
	tfs.Lock()
	delete(tfs.DataSubs, ch)
	tfs.Unlock()
	close(ch)
}

type rootNode struct {
	fs *TorrentFS
}

type node struct {
	path     []string
	metaInfo *metainfo.MetaInfo
	FS       *TorrentFS
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
	dataSpecs := fn.FS.SubscribeData()
	defer fn.FS.UnsubscribeData(dataSpecs)
	data := make([]byte, func() int {
		_len := int64(fn.size) - req.Offset
		if int64(req.Size) < _len {
			return req.Size
		} else {
			// limit read to the end of the file
			return int(_len)
		}
	}())
	infoHash := torrent.BytesInfoHash(fn.metaInfo.InfoHash)
	torrentOff := fn.TorrentOffset + req.Offset
	fn.FS.Client.PrioritizeDataRegion(infoHash, torrentOff, int64(len(data)))
	for {
		n, err := fn.FS.Client.TorrentReadAt(infoHash, torrentOff, data)
		// log.Println(torrentOff, len(data), n, err)
		switch err {
		case nil:
			resp.Data = data[:n]
			return nil
		case torrent.ErrDataNotReady:
			select {
			case <-dataSpecs:
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

func (tfs *TorrentFS) Root() (fusefs.Node, fuse.Error) {
	return rootNode{tfs}, nil
}

func main() {
	pprofAddr := flag.String("pprofAddr", "", "pprof HTTP server bind address")
	testPeer := flag.String("testPeer", "", "the address for a test peer")
	flag.Parse()
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	if *pprofAddr != "" {
		go http.ListenAndServe(*pprofAddr, nil)
	}
	// defer profile.Start(profile.CPUProfile).Stop()
	client := &torrent.Client{
		DataDir:       downloadDir,
		DataReady:     make(chan torrent.DataSpec),
		HalfOpenLimit: 2,
	}
	client.Start()
	torrentDir, err := os.Open(torrentPath)
	defer torrentDir.Close()
	if err != nil {
		log.Fatal(err)
	}
	names, err := torrentDir.Readdirnames(-1)
	if err != nil {
		log.Fatal(err)
	}
	var testAddr *net.TCPAddr
	if *testPeer != "" {
		testAddr, err = net.ResolveTCPAddr("tcp4", *testPeer)
		if err != nil {
			log.Fatal(err)
		}
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
	fs := &TorrentFS{
		Client:   client,
		DataSubs: make(map[chan torrent.DataSpec]struct{}),
	}
	go fs.publishData()
	go func() {
		for {
		torrentLoop:
			for _, t := range client.Torrents() {
				client.Lock()
				for _, c := range t.Conns {
					if c.Socket.RemoteAddr().String() == testAddr.String() {
						client.Unlock()
						continue torrentLoop
					}
				}
				client.Unlock()
				if testAddr != nil {
					if err := client.AddPeers(t.InfoHash, []torrent.Peer{{
						IP:   testAddr.IP,
						Port: testAddr.Port,
					}}); err != nil {
						log.Print(err)
					}
				}
			}
			time.Sleep(10 * time.Second)
		}
	}()
	fusefs.Serve(conn, fs)
}
