package main

import (
	"math/rand"
	"os"

	"github.com/anacrolix/envpprof"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

var client *torrent.Client

var index = 0
var infoHashes = []string{
	"6853ab2b86b2cb6a3c778b8aafe3dffd94242321",
	"4d29c6c02c97caad937d8a9b66b0bb1b6f7cbbfe",
}

func init() {
	opts := storage.NewFileClientOpts{
		ClientBaseDir: "./temp",
		TorrentDirMaker: func(baseDir string, info *metainfo.Info, infoHash metainfo.Hash) string {
			return baseDir + "/" + infoHash.HexString()
		},
	}
	conf := torrent.NewDefaultClientConfig()
	conf.DefaultStorage = storage.NewFileOpts(opts)
	conf.ListenPort = rand.Intn(65535-49152) + 49152

	_client, err := torrent.NewClient(conf)
	if err != nil {
		panic(err)
	}

	client = _client
}

func main() {
	defer envpprof.Stop()
	if len(os.Args) > 1 {
		noServer()
		return
	}

	server()
}
