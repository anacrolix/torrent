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

func (me rootNode) ReadDir(intr fusefs.Intr) (dirents []fuse.Dirent, err fuse.Error) {
	for _, _torrent := range me.fs.Client.Torrents() {
		metaInfo := _torrent.MetaInfo
		dirents = append(dirents, fuse.Dirent{
			Name: metaInfo.Name,
			Type: func() fuse.DirentType {
				if len(metaInfo.Files) == 1 && metaInfo.Files[0].Path == nil {
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
		client.AddTorrent(metaInfo)
	}
	conn, err := fuse.Mount(mountDir)
	if err != nil {
		log.Fatal(err)
	}
	fs := &TorrentFS{client}
	fusefs.Serve(conn, fs)
}
